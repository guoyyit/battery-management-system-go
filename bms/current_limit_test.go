package bms

import (
	"math"
	"testing"
)

func TestDeratingBoundaries(t *testing.T) {
	cfg := DefaultConfig(4, 2.5)
	l := NewCurrentLimiter(cfg)

	cases := []struct {
		name     string
		tempC    float64
		fraction float64
	}{
		{"charge at 0C disabled", 0, 0},
		{"charge at 5C half", 5, 0.5},
		{"charge at 10C full", 10, 1},
		{"charge at 45C full", 45, 1},
		{"charge at 50C half", 50, 0.5},
		{"charge at 55C disabled", 55, 0},
		{"charge below curve clamps", -10, 0},
		{"charge above curve clamps", 80, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.ChargeDerating.FractionAt(tc.tempC)
			if math.Abs(got-tc.fraction) > 1e-9 {
				t.Fatalf("fraction at %.0fC = %.2f, want %.2f",
					tc.tempC, got, tc.fraction)
			}
		})
	}

	res := l.Apply(100, 0.5, 5, 3.7, 3.7)
	if res.Applied != res.LimitA {
		t.Fatalf("applied %.3f != limit %.3f", res.Applied, res.LimitA)
	}
	if res.LimitA != 0.5*cfg.OverCurrentDischarge {
		t.Fatalf("limit = %.3f, want half base", res.LimitA)
	}
	if res.Reason == ReasonNone {
		t.Fatal("expected a limiting reason")
	}
}

func TestLimitZeroAtColdCharge(t *testing.T) {
	cfg := DefaultConfig(4, 2.5)
	l := NewCurrentLimiter(cfg)
	res := l.Apply(5, 0.5, -5, 3.7, 3.7)
	if res.Applied != 0 || res.Reason != ReasonLowTempCharge {
		t.Fatalf("cold charge: applied=%.3f reason=%q", res.Applied, res.Reason)
	}
}

func TestDischargeLimitNearEmpty(t *testing.T) {
	cfg := DefaultConfig(4, 2.5)
	l := NewCurrentLimiter(cfg)
	res := l.Apply(-5, 0.05, 25, 3.02, 3.8)
	if res.Applied != 0 || res.Reason != ReasonCellUnderVoltage {
		t.Fatalf("near-empty discharge: applied=%.3f reason=%q",
			res.Applied, res.Reason)
	}
}
