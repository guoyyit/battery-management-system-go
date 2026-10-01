package bms

import (
	"math"
	"testing"
)

func TestTemperatureDeratingBoundaries(t *testing.T) {
	cfg := DefaultConfig().Limits
	l := NewCurrentLimiter(cfg)
	temps := func(x float64) []float64 { return []float64{x, x, x, x} }
	mid := []float64{0.5, 0.5, 0.5, 0.5}
	volts := []float64{3.3, 3.3, 3.3, 3.3}

	cases := []struct {
		name          string
		temp          float64
		wantCharge    float64
		wantDischarge float64
	}{
		{"plateau", 25, 100, 100},
		{"mid-temperature linear interpolation", 32.5, 90, 94},
		{"charge cut-off hot", 50, 0, 80},
		{"charge cut-off cold", -10, 0, 60},
		{"discharge cut-off", 60, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := l.Apply(-999, temps(tc.temp), mid, volts)
			if math.Abs(-r.ChargeLimitA-r.AppliedCurrentA) > 1e-9 {
				t.Fatalf("charge limit %.3f, applied %.3f", r.ChargeLimitA, r.AppliedCurrentA)
			}
			if math.Abs(r.ChargeLimitA-tc.wantCharge) > 1e-9 {
				t.Fatalf("charge factor at %.1fC: limit %.2f want %.2f",
					tc.temp, r.ChargeLimitA, tc.wantCharge)
			}
			r = l.Apply(999, temps(tc.temp), mid, volts)
			if math.Abs(r.DischargeLimitA-tc.wantDischarge) > 1e-9 {
				t.Fatalf("discharge factor at %.1fC: limit %.2f want %.2f",
					tc.temp, r.DischargeLimitA, tc.wantDischarge)
			}
		})
	}
}

func TestLimiterClampsAndReportsReasons(t *testing.T) {
	cfg := DefaultConfig().Limits
	l := NewCurrentLimiter(cfg)
	temps := []float64{25, 25, 25, 25}
	mid := []float64{0.5, 0.5, 0.5, 0.5}
	volts := []float64{3.3, 3.3, 3.3, 3.3}

	r := l.Apply(250, temps, mid, volts)
	if r.AppliedCurrentA != cfg.MaxDischargeA {
		t.Fatalf("discharge request not clamped: %.1f", r.AppliedCurrentA)
	}
	if !r.Limited {
		t.Fatalf("over-request must be marked limited")
	}

	r = l.Apply(-250, temps, mid, volts)
	if r.AppliedCurrentA != -cfg.MaxChargeA {
		t.Fatalf("charge request not clamped: %.1f", r.AppliedCurrentA)
	}
}

func TestLimiterSOCTaper(t *testing.T) {
	cfg := DefaultConfig().Limits
	l := NewCurrentLimiter(cfg)
	temps := []float64{25, 25, 25, 25}
	volts := []float64{3.3, 3.3, 3.3, 3.3}

	r := l.Apply(-999, temps, []float64{0.975, 0.5, 0.5, 0.5}, volts)
	if math.Abs(r.ChargeLimitA-50) > 1e-9 {
		t.Fatalf("high-SOC charge taper: %.2f want 50", r.ChargeLimitA)
	}
	r = l.Apply(999, temps, []float64{0.025, 0.5, 0.5, 0.5}, volts)
	if math.Abs(r.DischargeLimitA-50) > 1e-9 {
		t.Fatalf("low-SOC discharge taper: %.2f want 50", r.DischargeLimitA)
	}
	r = l.Apply(-999, temps, []float64{1, 1, 1, 1}, volts)
	if r.ChargeLimitA != 0 {
		t.Fatalf("full pack must block charge: %.2f", r.ChargeLimitA)
	}
}

func contains(xs []string, sub string) bool {
	for _, x := range xs {
		if len(x) >= len(sub) && x[:len(sub)] == sub {
			return true
		}
	}
	return false
}
