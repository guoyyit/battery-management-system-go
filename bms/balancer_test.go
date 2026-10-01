package bms

import (
	"testing"
	"time"
)

func TestBalancerTriggersAndCompensatesVoltage(t *testing.T) {
	cfg := DefaultConfig(4, 2.5)
	sim := NewSimulator(cfg, 0.5)
	sim.trueSOC[0] = 0.90 // cell 0 is the high cell
	sim.trueSOC[1] = 0.50
	sim.trueSOC[2] = 0.50
	sim.trueSOC[3] = 0.50

	bal := NewBalancer(cfg)
	now := time.Unix(3_000_000, 0)

	m := sim.Sample(now)
	rawHigh := m.CellVoltagesV[0]

	// Force the high cell's shunt closed, as the balancer would decide.
	bal.switching[0] = true
	sim.SetBalancing(bal.Switching())
	m2 := sim.Sample(now.Add(cfg.SampleInterval))

	rawWhileBalancing := m2.CellVoltagesV[0]
	if rawWhileBalancing >= rawHigh {
		t.Fatalf("expected shunt-loaded raw voltage %.4f < %.4f",
			rawWhileBalancing, rawHigh)
	}

	eff := bal.EffectiveVoltages(m2)
	if eff[0] < rawHigh-0.01 {
		t.Fatalf("effective voltage while balancing %.4f should stay near %.4f",
			eff[0], rawHigh)
	}
}

func TestBalancerDecideAndHysteresis(t *testing.T) {
	cfg := DefaultConfig(3, 2.5)
	bal := NewBalancer(cfg)

	mk := func(vs ...float64) Measurement {
		return Measurement{
			Time:          time.Unix(4_000_000, 0),
			CellVoltagesV: vs,
			TempsC:        []float64{25, 25, 25},
		}
	}
	socs := []float64{0.5, 0.5, 0.5}

	// Cell 0 above the delta -> shunt closes.
	bal.Decide(mk(4.00, 3.90, 3.90), socs)
	if !bal.Switching()[0] || bal.Switching()[1] {
		t.Fatalf("unexpected switches: %v", bal.Switching())
	}

	// Gap shrinks below half the trigger delta -> releases (hysteresis).
	bal.Decide(mk(3.912, 3.90, 3.90), socs)
	if bal.Switching()[0] {
		t.Fatalf("shunt should release inside hysteresis band: %v", bal.Switching())
	}
}

func TestBalancerAccumulatesCharge(t *testing.T) {
	cfg := DefaultConfig(2, 2.5)
	bal := NewBalancer(cfg)
	m := Measurement{
		Time:          time.Unix(5_000_000, 0),
		CellVoltagesV: []float64{4.10, 3.80},
		TempsC:        []float64{25, 25},
	}
	for i := 0; i < 100; i++ {
		bal.Decide(m, []float64{0.9, 0.4})
		m = Measurement{
			Time:          m.Time.Add(cfg.SampleInterval),
			CellVoltagesV: []float64{4.10, 3.80},
			TempsC:        []float64{25, 25},
		}
	}
	got := bal.BalancedAh()[0]
	if got <= 0 {
		t.Fatalf("expected positive balanced charge, got %f", got)
	}
}
