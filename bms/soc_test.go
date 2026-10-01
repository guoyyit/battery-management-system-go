package bms

import (
	"math"
	"testing"
	"time"
)

func TestSOCEstimateWithNoiseAndDrift(t *testing.T) {
	cfg := DefaultConfig(4, 2.5)
	cfg.Seed = 42
	// Shorten rest so the test exercises OCV correction within the run.
	cfg.RestForOCV = 2 * time.Second

	sim := NewSimulator(cfg, 0.5)
	est := NewSOCEstimator(cfg, 0.5)

	now := time.Unix(1_000_000, 0)
	dt := cfg.SampleInterval

	// Discharge 1 A for 30 simulated minutes, then rest.
	run := func(currentA float64, duration time.Duration) {
		steps := int(duration / dt)
		sim.SetRequest(currentA)
		for i := 0; i < steps; i++ {
			now = now.Add(dt)
			m := sim.Sample(now)
			est.Observe(m)
		}
	}

	run(-1.0, 30*time.Minute)

	// Rest long enough for OCV correction to converge.
	sim.SetRequest(0)
	for i := 0; i < int((cfg.RestForOCV+2*time.Second)/dt); i++ {
		now = now.Add(dt)
		m := sim.Sample(now)
		est.Observe(m)
	}

	got := est.SOC()
	// Ground truth after 0.5 Ah discharge at ~99.5% charge efficiency:
	// 0.5 - (0.5*0.995)/2.5 = 0.301.
	const want = 0.301
	if math.Abs(got-want) > 0.05 {
		t.Fatalf("SOC after discharge+rest = %.4f, want within 5%% of %.3f", got, want)
	}
}

func TestSOCForceOCVConvergesAtBoot(t *testing.T) {
	cfg := DefaultConfig(4, 2.5)
	cfg.Seed = 7
	sim := NewSimulator(cfg, 0.8)
	// Start estimator deliberately wrong, as after a long power-off.
	est := NewSOCEstimator(cfg, 0.3)

	now := time.Unix(2_000_000, 0)
	m := sim.Sample(now)
	est.ForceOCVCorrect(m)

	if math.Abs(est.SOC()-0.8) > 0.05 {
		t.Fatalf("boot OCV SOC = %.3f, want ~0.80", est.SOC())
	}
}

func TestOCVTableRoundTrip(t *testing.T) {
	cfg := DefaultConfig(1, 1.0)
	for _, soc := range []float64{0, 0.15, 0.5, 0.83, 1} {
		v := cfg.OCVTable.Voltage(soc)
		back := cfg.OCVTable.SOC(v)
		if math.Abs(back-soc) > 1e-9 {
			t.Fatalf("OCV round trip at %.2f gave %.4f", soc, back)
		}
	}
}
