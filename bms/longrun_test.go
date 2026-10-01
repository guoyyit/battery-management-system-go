package bms

import (
	"math"
	"testing"
	"time"
)

func TestSOCLongRunAccuracyWithinFivePercent(t *testing.T) {
	cfg := DefaultConfig(4, 2.5)
	cfg.Seed = 99
	cfg.RestForOCV = 5 * time.Second

	sim := NewSimulator(cfg, 0.9)
	est := NewSOCEstimator(cfg, 0.9)
	now := time.Unix(20_000_000, 0)

	step := func(currentA float64) {
		now = now.Add(cfg.SampleInterval)
		sim.SetRequest(currentA)
		est.Observe(sim.Sample(now))
	}

	// Alternating duty cycle with rests, 2 simulated hours per phase.
	phases := []struct {
		currentA float64
		duration time.Duration
	}{
		{-2.0, 20 * time.Minute},
		{0, cfg.RestForOCV + 3*time.Second},
		{1.5, 20 * time.Minute},
		{0, cfg.RestForOCV + 3*time.Second},
		{-2.5, 15 * time.Minute},
		{0, cfg.RestForOCV + 3*time.Second},
	}
	for _, ph := range phases {
		for d := time.Duration(0); d < ph.duration; d += cfg.SampleInterval {
			step(ph.currentA)
		}
	}

	got := est.SOC()
	truth := meanF(sim.TrueSOC())
	if math.Abs(got-truth) > 0.05 {
		t.Fatalf("long-run SOC error %.1f%% > 5%% (got=%.3f truth=%.3f)",
			100*math.Abs(got-truth), got, truth)
	}
	t.Logf("long-run SOC: estimate=%.3f truth=%.3f error=%.2f%%",
		got, truth, 100*math.Abs(got-truth))
}
