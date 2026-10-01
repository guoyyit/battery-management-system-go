package bms

import (
	"math"
	"sort"
	"sync"
	"time"
)

// BalanceMode selects the balancing trigger signal.
type BalanceMode int

const (
	// BalanceByVoltage triggers on open-circuit-reconstructed voltage spread.
	BalanceByVoltage BalanceMode = iota
	// BalanceBySOC triggers on estimated-SOC spread.
	BalanceBySOC
)

// BalancerConfig configures passive balancing.
type BalancerConfig struct {
	Mode BalanceMode

	VoltageThresholdV float64
	SOCThreshold      float64
	Hysteresis        float64

	// MaxBalanceA bounds the per-cell bypass current.
	MaxBalanceA float64
	// MaxConcurrent bounds how many cells may balance simultaneously.
	MaxConcurrent int

	// Balancing only arms while pack current is below this magnitude.
	ArmCurrentA float64

	// BypassResistorOhm is used both to estimate the bypass current and to
	// reconstruct the open-circuit voltage from the loaded terminal sample.
	BypassResistorOhm float64

	// MinSOC stops bypassing a nearly empty cell so it cannot be over-drained.
	MinSOC float64
}

// BalanceStatus is the observable balancer state.
type BalanceStatus struct {
	Active   []bool
	CurrentA []float64
	ChargeAh []float64
}

// Balancer decides per-cell bypass switch state.
type Balancer struct {
	mu  sync.RWMutex
	cfg BalancerConfig
	n   int

	active   []bool
	currentA []float64
	chargeAh []float64

	signal []float64
}

// NewBalancer builds a balancer restored from active/chargeAh (nil = fresh).
func NewBalancer(cfg BalancerConfig, cellCount int, active []bool, chargeAh []float64) *Balancer {
	b := &Balancer{
		cfg:      cfg,
		n:        cellCount,
		active:   make([]bool, cellCount),
		currentA: make([]float64, cellCount),
		chargeAh: make([]float64, cellCount),
		signal:   make([]float64, cellCount),
	}
	for i := 0; i < cellCount && i < len(active); i++ {
		b.active[i] = active[i]
	}
	for i := 0; i < cellCount && i < len(chargeAh); i++ {
		b.chargeAh[i] = chargeAh[i]
	}
	return b
}

// Update runs one decision step and returns the post-step status.
func (b *Balancer) Update(dt time.Duration, m Measurement, soc []float64, contactorOpen bool) BalanceStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	threshold := b.cfg.VoltageThresholdV
	if b.cfg.Mode == BalanceBySOC {
		threshold = b.cfg.SOCThreshold
	}

	// Reconstruct the per-cell trigger signal first. Existing bypass current
	// is known from the previous decision, so the loaded terminal voltage of
	// an active cell is corrected by I_bal * R_bypass.
	minSig := math.Inf(1)
	for i := 0; i < b.n; i++ {
		var s float64
		switch b.cfg.Mode {
		case BalanceBySOC:
			if i < len(soc) {
				s = soc[i]
			}
		default:
			if i < len(m.CellVoltage) {
				s = m.CellVoltage[i]
				if b.active[i] {
					s += b.currentA[i] * b.cfg.BypassResistorOhm
				}
			}
		}
		b.signal[i] = s
		if s < minSig {
			minSig = s
		}
	}

	armed := !contactorOpen && math.Abs(m.CurrentSensed) <= b.cfg.ArmCurrentA

	// Re-evaluate every switch with hysteresis: an already active cell stays
	// on until within threshold-Hysteresis of the minimum, which prevents
	// chattering near the threshold.
	want := make([]bool, b.n)
	if armed {
		var cands []struct {
			idx int
			sig float64
		}
		for i := 0; i < b.n; i++ {
			dev := b.signal[i] - minSig
			limit := threshold
			if b.active[i] {
				limit -= b.cfg.Hysteresis
			}
			if i < len(soc) && soc[i] <= b.cfg.MinSOC {
				continue
			}
			if dev >= limit {
				cands = append(cands, struct {
					idx int
					sig float64
				}{i, b.signal[i]})
			}
		}
		sort.Slice(cands, func(x, y int) bool { return cands[x].sig > cands[y].sig })
		k := b.cfg.MaxConcurrent
		if k <= 0 || k > b.n {
			k = b.n
		}
		if len(cands) > k {
			cands = cands[:k]
		}
		for _, c := range cands {
			want[c.idx] = true
		}
	}
	// Commit switch state and compute bypass currents from the reconstructed
	// cell voltage: I = Voc / R_bypass, capped by MaxBalanceA.
	for i := 0; i < b.n; i++ {
		b.active[i] = want[i]
		if !want[i] {
			b.currentA[i] = 0
			continue
		}
		voc := b.signal[i]
		ib := 0.0
		if b.cfg.BypassResistorOhm > 0 {
			ib = voc / b.cfg.BypassResistorOhm
		}
		if ib > b.cfg.MaxBalanceA {
			ib = b.cfg.MaxBalanceA
		}
		b.currentA[i] = ib
		b.chargeAh[i] += ib * dt.Seconds() / secondsPerHour
	}

	return BalanceStatus{
		Active:   append([]bool(nil), b.active...),
		CurrentA: append([]float64(nil), b.currentA...),
		ChargeAh: append([]float64(nil), b.chargeAh...),
	}
}

// Status returns the current state without modifying it.
func (b *Balancer) Status() BalanceStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return BalanceStatus{
		Active:   append([]bool(nil), b.active...),
		CurrentA: append([]float64(nil), b.currentA...),
		ChargeAh: append([]float64(nil), b.chargeAh...),
	}
}
