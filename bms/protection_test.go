package bms

import (
	"testing"
	"time"
)

func protectionTestCfg() ProtectionConfig {
	c := DefaultConfig().Protection
	c.TripDebounce = 500 * time.Millisecond
	return c
}

func measAt(now time.Time, volts, temps []float64, current float64) Measurement {
	return Measurement{
		Time:          now,
		CellVoltage:   volts,
		Temperature:   temps,
		CurrentSensed: current,
	}
}

// TestProtectionDebounceAndLatch: an over-voltage excursion shorter than the
// debounce never trips; a long one latches and survives recovery below the
// trip threshold (hysteresis), clearing only at the explicit clear level.
func TestProtectionDebounceAndLatch(t *testing.T) {
	now := time.Unix(0, 0)
	cfg := protectionTestCfg()
	p := NewProtection(cfg, nil)
	dt := 200 * time.Millisecond

	normal := []float64{3.3, 3.3, 3.3, 3.3}
	temps := []float64{25, 25, 25, 25}

	// Two short spikes must not trip.
	for i := 0; i < 2; i++ {
		now = now.Add(dt)
		if trips, _ := p.Evaluate(dt, measAt(now, []float64{3.7, 3.3, 3.3, 3.3}, temps, 0)); len(trips) != 0 {
			t.Fatal("sub-debounce over-voltage must not trip")
		}
		now = now.Add(dt)
		if trips, _ := p.Evaluate(dt, measAt(now, normal, temps, 0)); len(trips) != 0 {
			t.Fatal("normal sample after spike must not trip")
		}
	}

	// Sustained over-voltage latches.
	for i := 0; i < 4; i++ {
		now = now.Add(dt)
		trips, open := p.Evaluate(dt, measAt(now, []float64{3.7, 3.3, 3.3, 3.3}, temps, 0))
		if len(trips) == 1 && i == 3 {
			f := trips[0]
			if f.Type != FaultOvervoltage || f.Cell != 0 || f.Voltage != 3.7 {
				t.Fatalf("wrong trip snapshot: %+v", f)
			}
		}
		if i == 3 && (!open || !p.IsTripped()) {
			t.Fatal("contactor must open after latch")
		}
	}

	// Chatter between trip and clear thresholds: above clear level keeps it
	// latched, and re-crossing the trip level must not append a second trip.
	before := len(p.Tripped())
	for i := 0; i < 5; i++ {
		now = now.Add(dt)
		v := 3.50
		if i%2 == 0 {
			v = 3.68
		}
		trips, _ := p.Evaluate(dt, measAt(now, []float64{float64(v), 3.3, 3.3, 3.3}, temps, 0))
		if len(trips) != 0 {
			t.Fatal("already-latched fault must not trip again")
		}
	}
	if len(p.Tripped()) != before {
		t.Fatal("latch set changed during chatter")
	}

	// Explicit clear while still above the clear level must be refused.
	now = now.Add(dt)
	if cleared := p.Clear(measAt(now, []float64{3.5, 3.3, 3.3, 3.3}, temps, 0)); len(cleared) != 0 {
		t.Fatal("clear above hysteresis level must be refused")
	}
	if !p.IsTripped() {
		t.Fatal("fault must remain latched after refused clear")
	}

	// Once every cell is below the clear level, explicit clear succeeds.
	now = now.Add(dt)
	if cleared := p.Clear(measAt(now, []float64{3.4, 3.3, 3.3, 3.3}, temps, 0)); len(cleared) != 1 || cleared[0] != FaultOvervoltage {
		t.Fatalf("clear below hysteresis level must unlatch, got %v", cleared)
	}
	if p.IsTripped() {
		t.Fatal("fault must be unlatched after valid clear")
	}
}

// TestProtectionOvertempAndOvercurrent covers the remaining trip families.
func TestProtectionOvertempAndOvercurrent(t *testing.T) {
	now := time.Unix(0, 0)
	cfg := protectionTestCfg()
	dt := 300 * time.Millisecond
	volts := []float64{3.3, 3.3, 3.3, 3.3}

	p := NewProtection(cfg, nil)
	for i := 0; i < 3; i++ {
		now = now.Add(dt)
		trips, _ := p.Evaluate(dt, measAt(now, volts, []float64{25, 62, 25, 25}, 0))
		if i == 1 {
			if len(trips) != 1 || trips[0].Type != FaultOvertemp || trips[0].Cell != 1 {
				t.Fatalf("expected overtemp cell 1 trip, got %+v", trips)
			}
		} else if len(trips) != 0 {
			t.Fatalf("overtemp must trip exactly once; step %d trips %+v", i, trips)
		}
	}

	p2 := NewProtection(cfg, nil)
	now = now.Add(time.Second)
	trips, open := p2.Evaluate(time.Second, measAt(now, volts, []float64{25, 25, 25, 25}, -320))
	if len(trips) != 1 || trips[0].Type != FaultShortCircuit || !open {
		t.Fatalf("expected immediate short-circuit latch, got %+v open=%v", trips, open)
	}
}
