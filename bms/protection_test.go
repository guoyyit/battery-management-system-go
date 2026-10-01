package bms

import (
	"testing"
	"time"
)

func snapWith(now time.Time, vs []float64, current, temp float64) PackSnapshot {
	cells := make([]CellState, len(vs))
	for i, v := range vs {
		cells[i] = CellState{Index: i, VoltageV: v}
	}
	return PackSnapshot{
		Time:     now,
		Cells:    cells,
		CurrentA: current,
		TempC:    temp,
	}
}

func TestOverVoltageHysteresisAndLatch(t *testing.T) {
	cfg := DefaultConfig(4, 2.5)
	p := NewProtection(cfg)
	now := time.Unix(6_000_000, 0)
	dt := cfg.SampleInterval

	normal := []float64{3.8, 3.8, 3.8, 3.8}
	if ev := p.Evaluate(snapWith(now, normal, 0, 25)); len(ev) != 0 {
		t.Fatal("no events expected in normal conditions")
	}

	// Voltage crosses above 4.20 -> trips and latches.
	over := []float64{3.8, 4.25, 3.8, 3.8}
	ev := p.Evaluate(snapWith(now.Add(dt), over, 0, 25))
	if len(ev) != 1 || ev[0].Record.Type != FaultOverVoltage ||
		ev[0].Record.Action != FaultTriggered || ev[0].Record.CellIndex != 1 {
		t.Fatalf("unexpected trip events: %+v", ev)
	}
	if !p.Latched() {
		t.Fatal("expected latched after trip")
	}

	// Snapshot includes voltage/temperature data for traceability.
	r := ev[0].Record
	if r.VoltageV < 4.24 || r.TempC != 25 {
		t.Fatalf("fault snapshot missing data: %+v", r)
	}

	// Drop back into the hysteresis band (between 4.10 and 4.20): must NOT
	// re-trip or chatter; latch stays set.
	band := []float64{3.8, 4.15, 3.8, 3.8}
	if ev := p.Evaluate(snapWith(now.Add(2*dt), band, 0, 25)); len(ev) != 0 {
		t.Fatalf("chatter inside hysteresis band: %+v", ev)
	}
	if !p.Latched() {
		t.Fatal("latch must persist inside hysteresis band")
	}

	// Clear while the condition is still active -> rejected.
	if p.ClearFaults(now.Add(3*dt)) != nil {
		t.Fatal("clear must be rejected while over-voltage persists")
	}

	// Voltage recovers below 4.10 -> condition releases, but latch remains.
	ev = p.Evaluate(snapWith(now.Add(4*dt), normal, 0, 25))
	if len(ev) != 1 || ev[0].Record.Action != FaultCleared {
		t.Fatalf("expected condition-cleared event, got %+v", ev)
	}
	if !p.Latched() {
		t.Fatal("condition clearing must not remove the latch by itself")
	}

	// Explicit clear now succeeds.
	clearEv := p.ClearFaults(now.Add(5 * dt))
	if clearEv == nil || p.Latched() {
		t.Fatal("explicit clear should remove latch once safe")
	}
}

func TestOverCurrentLatches(t *testing.T) {
	cfg := DefaultConfig(4, 2.5)
	p := NewProtection(cfg)
	now := time.Unix(7_000_000, 0)

	p.Evaluate(snapWith(now, []float64{3.7, 3.7, 3.7, 3.7},
		cfg.OverCurrentDischarge+1, 25))
	ev := p.Evaluate(snapWith(now.Add(cfg.SampleInterval),
		[]float64{3.7, 3.7, 3.7, 3.7},
		cfg.OverCurrentDischarge+1, 25))
	if len(ev) != 1 || ev[0].Record.Type != FaultOverCurrent {
		t.Fatalf("expected over-current trip, got %+v", ev)
	}
	if !p.Latched() {
		t.Fatal("over-current must latch")
	}
}

func TestShortCircuitRequiresSustain(t *testing.T) {
	cfg := DefaultConfig(4, 2.5)
	cfg.ShortCircuitDuration = cfg.SampleInterval
	p := NewProtection(cfg)
	now := time.Unix(8_000_000, 0)

	// One short spike shorter than ShortCircuitDuration must not trip.
	p.Evaluate(snapWith(now, []float64{3.7, 3.7, 3.7, 3.7},
		-cfg.ShortCircuitCurrent-1, 25))
	if p.Latched() {
		t.Fatal("single short sample must not trip short-circuit")
	}

	// Sustained short trips.
	ev := p.Evaluate(snapWith(now.Add(cfg.SampleInterval),
		[]float64{3.7, 3.7, 3.7, 3.7}, -cfg.ShortCircuitCurrent-1, 25))
	found := false
	for _, e := range ev {
		if e.Record.Type == FaultShortCircuit {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected sustained short trip, got %+v", ev)
	}
}
