package bms

import "time"

// FaultType identifies a protection condition.
type FaultType int

const (
	FaultNone FaultType = iota
	FaultOvervoltage
	FaultUndervoltage
	FaultOvertemp
	FaultOvercurrentCharge
	FaultOvercurrentDischarge
	FaultShortCircuit
)

// String names the fault for logs and history.
func (f FaultType) String() string {
	switch f {
	case FaultOvervoltage:
		return "overvoltage"
	case FaultUndervoltage:
		return "undervoltage"
	case FaultOvertemp:
		return "overtemp"
	case FaultOvercurrentCharge:
		return "overcurrent-charge"
	case FaultOvercurrentDischarge:
		return "overcurrent-discharge"
	case FaultShortCircuit:
		return "short-circuit"
	default:
		return "none"
	}
}

// ProtectionConfig holds thresholds, debounce and clear (hysteresis) levels.
type ProtectionConfig struct {
	CellOverV       float64
	CellOverVClear  float64
	CellUnderV      float64
	CellUnderVClear float64
	OvertempC       float64
	OvertempClearC  float64
	OCPChargeA      float64
	OCPDischargeA   float64
	ShortCircuitA   float64

	// TripDebounce suppresses short threshold crossings (jitter, noise).
	TripDebounce time.Duration
}

// ActiveFault is a latched trip with its triggering snapshot.
type ActiveFault struct {
	Type        FaultType
	Cell        int
	Voltage     float64
	Temperature float64
	Current     float64
	At          time.Time
}

type candidate struct {
	ftype FaultType
	cell  int
	v, t  float64
}

// Protection evaluates conditions and latches trips.
type Protection struct {
	cfg     ProtectionConfig
	latched []ActiveFault
	timers  map[FaultType]time.Duration
}

// NewProtection restores latched faults (nil = none).
func NewProtection(cfg ProtectionConfig, latched []ActiveFault) *Protection {
	cp := append([]ActiveFault(nil), latched...)
	return &Protection{
		cfg:     cfg,
		latched: cp,
		timers:  make(map[FaultType]time.Duration),
	}
}

// Evaluate consumes one debounce sample. Newly latched trips are returned;
// the returned open flag requests the contactors open immediately.
func (p *Protection) Evaluate(dt time.Duration, m Measurement) (newTrips []ActiveFault, open bool) {
	cands := p.detect(m)
	pending := map[FaultType]candidate{}
	for _, c := range cands {
		pending[c.ftype] = c
	}
	for _, ft := range []FaultType{
		FaultOvervoltage,
		FaultUndervoltage,
		FaultOvertemp,
		FaultOvercurrentCharge,
		FaultOvercurrentDischarge,
		FaultShortCircuit,
	} {
		if p.has(ft) {
			continue
		}
		if c, ok := pending[ft]; ok {
			p.timers[ft] += dt
			if p.timers[ft] >= p.cfg.TripDebounce {
				trip := ActiveFault{
					Type:        ft,
					Cell:        c.cell,
					Voltage:     c.v,
					Temperature: c.t,
					Current:     m.CurrentSensed,
					At:          m.Time,
				}
				p.latched = append(p.latched, trip)
				newTrips = append(newTrips, trip)
			}
		} else {
			p.timers[ft] = 0
		}
	}
	return newTrips, len(p.latched) > 0
}

// detect returns every currently violated (not yet hysteresis-cleared)
// condition from this sample.
func (p *Protection) detect(m Measurement) []candidate {
	var out []candidate
	for i, v := range m.CellVoltage {
		temp := 0.0
		if i < len(m.Temperature) {
			temp = m.Temperature[i]
		}
		if v >= p.cfg.CellOverV {
			out = append(out, candidate{FaultOvervoltage, i, v, temp})
		}
		if v <= p.cfg.CellUnderV {
			out = append(out, candidate{FaultUndervoltage, i, v, temp})
		}
		if temp >= p.cfg.OvertempC {
			out = append(out, candidate{FaultOvertemp, i, v, temp})
		}
	}
	switch cur := m.CurrentSensed; {
	case cur <= -p.cfg.ShortCircuitA:
		out = append(out, candidate{FaultShortCircuit, -1, worstV(m), worstT(m)})
	case cur <= -p.cfg.OCPChargeA:
		out = append(out, candidate{FaultOvercurrentCharge, -1, worstV(m), worstT(m)})
	}
	if m.CurrentSensed >= p.cfg.OCPDischargeA {
		out = append(out, candidate{FaultOvercurrentDischarge, -1, worstV(m), worstT(m)})
	}
	return out
}

func worstV(m Measurement) float64 {
	v := 0.0
	if len(m.CellVoltage) > 0 {
		v = m.CellVoltage[0]
		for _, x := range m.CellVoltage[1:] {
			if x > v {
				v = x
			}
		}
	}
	return v
}

func worstT(m Measurement) float64 {
	t := 0.0
	if len(m.Temperature) > 0 {
		t = m.Temperature[0]
		for _, x := range m.Temperature[1:] {
			if x > t {
				t = x
			}
		}
	}
	return t
}

// Tripped returns all currently latched faults.
func (p *Protection) Tripped() []ActiveFault {
	out := make([]ActiveFault, len(p.latched))
	copy(out, p.latched)
	return out
}

// IsTripped reports whether any fault is latched.
func (p *Protection) IsTripped() bool { return len(p.latched) > 0 }

// Clear unlatches faults whose hysteresis clear conditions currently hold and
// returns the fault types that were cleared. Latching means this only happens
// on this explicit call, never automatically.
func (p *Protection) Clear(m Measurement) []FaultType {
	var cleared []FaultType
	kept := p.latched[:0]
	for _, f := range p.latched {
		if p.clearOK(f, m) {
			cleared = append(cleared, f.Type)
			delete(p.timers, f.Type)
			continue
		}
		kept = append(kept, f)
	}
	p.latched = append([]ActiveFault(nil), kept...)
	return cleared
}

func (p *Protection) clearOK(f ActiveFault, m Measurement) bool {
	switch f.Type {
	case FaultOvervoltage:
		for _, v := range m.CellVoltage {
			if v >= p.cfg.CellOverVClear {
				return false
			}
		}
		return true
	case FaultUndervoltage:
		for _, v := range m.CellVoltage {
			if v <= p.cfg.CellUnderVClear {
				return false
			}
		}
		return true
	case FaultOvertemp:
		for _, t := range m.Temperature {
			if t >= p.cfg.OvertempClearC {
				return false
			}
		}
		return true
	case FaultOvercurrentCharge, FaultOvercurrentDischarge, FaultShortCircuit:
		return m.CurrentSensed > -p.cfg.OCPChargeA && m.CurrentSensed < p.cfg.OCPDischargeA
	}
	return false
}

func (p *Protection) has(ft FaultType) bool {
	for _, f := range p.latched {
		if f.Type == ft {
			return true
		}
	}
	return false
}
