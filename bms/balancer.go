package bms

// Balancer decides per-cell passive balancing switches. Cells above the pack
// minimum by more than the configured voltage (or SOC) delta are shunted;
// balancing stops when the gap closes, giving natural hysteresis and avoiding
// chatter near the threshold.
type Balancer struct {
	cfg Config

	switching  []bool
	balancedAh []float64
	voltage    []float64
}

// NewBalancer constructs a passive balancer.
func NewBalancer(cfg Config) *Balancer {
	return &Balancer{
		cfg:        cfg,
		switching:  make([]bool, cfg.CellCount),
		balancedAh: make([]float64, cfg.CellCount),
		voltage:    make([]float64, cfg.CellCount),
	}
}

// Decide recomputes switch states from the latest effective cell measurements.
// It also accumulates charge removed by each closed shunt.
func (b *Balancer) Decide(m Measurement, socs []float64) {
	eff := b.EffectiveVoltages(m)
	b.voltage = eff

	minV := eff[0]
	minSOC := socs[0]
	for i := 1; i < len(eff); i++ {
		if eff[i] < minV {
			minV = eff[i]
		}
		if socs[i] < minSOC {
			minSOC = socs[i]
		}
	}

	dt := b.cfg.SampleInterval.Seconds()
	for i := range b.switching {
		highOnVoltage := eff[i]-minV >= b.cfg.BalanceDeltaV
		highOnSOC := socs[i]-minSOC >= b.cfg.BalanceDeltaSOC
		shouldBalance := (highOnVoltage || highOnSOC)

		// Hysteresis: a running shunt stays on until the gap shrinks to half
		// the trigger delta, preventing rapid on/off at the boundary.
		if b.switching[i] {
			releaseV := eff[i]-minV < b.cfg.BalanceDeltaV/2
			releaseSOC := socs[i]-minSOC < b.cfg.BalanceDeltaSOC/2
			if releaseV || releaseSOC {
				shouldBalance = false
			} else {
				shouldBalance = true
			}
		}

		if shouldBalance && !b.switching[i] {
			b.switching[i] = true
		} else if !shouldBalance && b.switching[i] {
			b.switching[i] = false
		}

		if b.switching[i] {
			b.balancedAh[i] += b.cfg.MaxBalanceCurrentA * dt / 3600.0
		}
	}
}

// Switching reports which cells are currently balancing (copy).
func (b *Balancer) Switching() []bool {
	out := make([]bool, len(b.switching))
	copy(out, b.switching)
	return out
}

// BalancedAh reports cumulative charge removed per cell (copy).
func (b *Balancer) BalancedAh() []float64 {
	out := make([]float64, len(b.balancedAh))
	copy(out, b.balancedAh)
	return out
}

// EffectiveVoltages returns cell voltage samples corrected for the voltage
// depression caused by a closed balancing shunt, so measurements taken while
// balancing remain valid for decisions and protection.
func (b *Balancer) EffectiveVoltages(m Measurement) []float64 {
	out := make([]float64, len(m.CellVoltagesV))
	copy(out, m.CellVoltagesV)
	drop := b.cfg.MaxBalanceCurrentA * simInternalResistanceOhm
	for i := range out {
		if i < len(b.switching) && b.switching[i] {
			out[i] += drop
		}
	}
	return out
}

// Restore loads cumulative balancing state after a crash.
func (b *Balancer) Restore(switching []bool, balancedAh []float64) {
	copy(b.switching, switching)
	copy(b.balancedAh, balancedAh)
}
