package bms

import "time"

// Protection evaluates thresholds with hysteresis and latches faults.
// A fault trips on the high threshold and only releases (internally) below the
// threshold minus its hysteresis band. The contactor stays open (latched)
// after a trip until ClearFaults is explicitly called with all conditions safe.
type Protection struct {
	cfg Config

	active        map[FaultType]bool
	latched       bool
	shortSince    time.Time
	overcurrSince time.Time
	nextFaultID   int64
}

// NewProtection constructs a protection evaluator.
func NewProtection(cfg Config) *Protection {
	return &Protection{
		cfg:    cfg,
		active: map[FaultType]bool{},
	}
}

// ProtectionEvent is one fault transition produced during evaluation.
type ProtectionEvent struct {
	Record FaultRecord
}

// Evaluate checks one snapshot and returns newly triggered/cleared events.
// "cleared" means the underlying condition released below the hysteresis band;
// the latch itself is only removed via ClearFaults.
func (p *Protection) Evaluate(snap PackSnapshot) []ProtectionEvent {
	var events []ProtectionEvent

	trip := func(ft FaultType, cell int, v, temp float64, msg string) {
		if !p.active[ft] {
			p.active[ft] = true
			p.latched = true
			p.nextFaultID++
			events = append(events, ProtectionEvent{Record: FaultRecord{
				ID:        p.nextFaultID,
				Time:      snap.Time,
				Type:      ft,
				Action:    FaultTriggered,
				CellIndex: cell,
				VoltageV:  v,
				TempC:     temp,
				CurrentA:  snap.CurrentA,
				SOC:       snap.PackSOC,
				Message:   msg,
			}})
		}
	}
	release := func(ft FaultType, cell int, v, temp float64, msg string) {
		if p.active[ft] {
			p.active[ft] = false
			p.nextFaultID++
			events = append(events, ProtectionEvent{Record: FaultRecord{
				ID:        p.nextFaultID,
				Time:      snap.Time,
				Type:      ft,
				Action:    FaultCleared,
				CellIndex: cell,
				VoltageV:  v,
				TempC:     temp,
				CurrentA:  snap.CurrentA,
				SOC:       snap.PackSOC,
				Message:   msg,
			}})
		}
	}

	maxV, maxCell := -1e9, 0
	minV, minCell := 1e9, 0
	for _, c := range snap.Cells {
		if c.VoltageV > maxV {
			maxV, maxCell = c.VoltageV, c.Index
		}
		if c.VoltageV < minV {
			minV, minCell = c.VoltageV, c.Index
		}
	}

	// Over / under voltage with hysteresis.
	if p.active[FaultOverVoltage] {
		if maxV <= p.cfg.OverVoltage-p.cfg.OverVoltageHysteresis {
			release(FaultOverVoltage, maxCell, maxV, snap.TempC, "max cell voltage back below release threshold")
		}
	} else if maxV > p.cfg.OverVoltage {
		trip(FaultOverVoltage, maxCell, maxV, snap.TempC, "cell voltage above over-voltage threshold")
	}
	if p.active[FaultUnderVoltage] {
		if minV >= p.cfg.UnderVoltage+p.cfg.UnderVoltageHysteresis {
			release(FaultUnderVoltage, minCell, minV, snap.TempC, "min cell voltage back above release threshold")
		}
	} else if minV < p.cfg.UnderVoltage {
		trip(FaultUnderVoltage, minCell, minV, snap.TempC, "cell voltage below under-voltage threshold")
	}

	// Over-temperature (max sensor) with hysteresis.
	if p.active[FaultOverTemperature] {
		if snap.TempC <= p.cfg.OverTemperature-p.cfg.OverTempHyst {
			release(FaultOverTemperature, -1, maxV, snap.TempC, "temperature back below release threshold")
		}
	} else if snap.TempC > p.cfg.OverTemperature {
		trip(FaultOverTemperature, -1, maxV, snap.TempC, "temperature above threshold")
	}

	// Over-current: sustained |I| beyond the directional threshold.
	ocOver := snap.CurrentA > p.cfg.OverCurrentCharge ||
		-snap.CurrentA > p.cfg.OverCurrentDischarge
	if ocOver {
		if p.overcurrSince.IsZero() {
			p.overcurrSince = snap.Time
		}
		// One interval of persistence filters single-sample noise spikes.
		if snap.Time.Sub(p.overcurrSince) >= p.cfg.SampleInterval {
			if p.active[FaultOverCurrent] {
				if absF(snap.CurrentA) <= overCurrentRelease(p.cfg) {
					release(FaultOverCurrent, -1, maxV, snap.TempC, "current back below release threshold")
				}
			} else {
				trip(FaultOverCurrent, -1, maxV, snap.TempC, "current beyond over-current threshold")
			}
		}
	} else {
		p.overcurrSince = time.Time{}
		if p.active[FaultOverCurrent] && absF(snap.CurrentA) <= overCurrentRelease(p.cfg) {
			release(FaultOverCurrent, -1, maxV, snap.TempC, "current back below release threshold")
		}
	}

	// Short circuit: very large current sustained for ShortCircuitDuration.
	if absF(snap.CurrentA) >= p.cfg.ShortCircuitCurrent {
		if p.shortSince.IsZero() {
			p.shortSince = snap.Time
		}
		if !p.active[FaultShortCircuit] &&
			snap.Time.Sub(p.shortSince) >= p.cfg.ShortCircuitDuration {
			trip(FaultShortCircuit, -1, maxV, snap.TempC, "short-circuit current sustained")
		}
	} else {
		p.shortSince = time.Time{}
		if p.active[FaultShortCircuit] {
			release(FaultShortCircuit, -1, maxV, snap.TempC, "short-circuit current removed")
		}
	}

	return events
}

func overCurrentRelease(cfg Config) float64 {
	rel := cfg.OverCurrentCharge - cfg.OverCurrentHyst
	if d := cfg.OverCurrentDischarge - cfg.OverCurrentHyst; d < rel {
		rel = d
	}
	if rel < 0 {
		rel = 0
	}
	return rel
}

// Latched reports whether the charge/discharge contactor must stay open.
func (p *Protection) Latched() bool { return p.latched }

// ActiveFaults reports the currently active fault types (sorted for stability).
func (p *Protection) ActiveFaults() []FaultType {
	out := make([]FaultType, 0, len(p.active))
	for _, ft := range []FaultType{
		FaultOverVoltage, FaultUnderVoltage, FaultOverTemperature,
		FaultOverCurrent, FaultShortCircuit,
	} {
		if p.active[ft] {
			out = append(out, ft)
		}
	}
	return out
}

// ClearFaults explicitly clears the latch. It succeeds only when no fault
// condition remains active; a "cleared" recovery record is returned as an
// event so the latch removal is traceable.
func (p *Protection) ClearFaults(now time.Time) []ProtectionEvent {
	if !p.latched || len(p.ActiveFaults()) != 0 {
		return nil
	}
	p.latched = false
	p.nextFaultID++
	return []ProtectionEvent{{Record: FaultRecord{
		ID:        p.nextFaultID,
		Time:      now,
		Type:      FaultLatchReset,
		Action:    FaultCleared,
		CellIndex: -1,
		Message:   "fault latch explicitly cleared",
	}}}
}

// Restore re-installs persisted latch/active-fault state after a crash.
func (p *Protection) Restore(latched bool, active []FaultType, nextID int64) {
	p.latched = latched
	p.nextFaultID = nextID
	for k := range p.active {
		delete(p.active, k)
	}
	for _, ft := range active {
		p.active[ft] = true
	}
}
