package bms

import (
	"math"
	"testing"
	"time"
)

func testModelConfig(seed int64) ModelConfig {
	cfg := DefaultConfig().Model
	cfg.Seed = seed
	return cfg
}

// TestSOCAccuracyUnderNoiseAndDrift coulomb-counts a multi-hour
// discharge/rest/charge mission. Drift must accumulate error during cycling
// and the rest-triggered OCV correction must pull the estimate back within 5%.
func TestSOCAccuracyUnderNoiseAndDrift(t *testing.T) {
	now := time.Unix(0, 0)
	cfg := testModelConfig(7)
	m := NewModel(cfg, now)

	ecfg := DefaultConfig().SOC
	// Fast rest qualification keeps the test short while exercising the same
	// correction loop used in production.
	ecfg.RestDuration = 2 * time.Minute
	est := NewEstimator(ecfg, m.TrueSOC(), cfg.SeriesCount)

	dt := time.Second
	schedule := []struct {
		dur     time.Duration
		current float64
	}{
		{2 * time.Hour, 30},
		{30 * time.Minute, 0},
		{90 * time.Minute, -30},
		{30 * time.Minute, 0},
		{2 * time.Hour, 50},
		{20 * time.Minute, 0},
	}

	var meas Measurement
	clock := now
	for _, ph := range schedule {
		for elapsed := time.Duration(0); elapsed < ph.dur; elapsed += dt {
			clock = clock.Add(dt)
			meas = m.Step(dt, Inputs{Current: ph.current, AmbientTemp: 25}, clock)
			est.Update(dt, meas, nil, nil)
		}
	}

	if !est.Resting() {
		t.Fatalf("expected resting state, restElapsed=%s", est.RestElapsed())
	}
	truth := m.TrueSOC()
	for i, s := range est.SOC() {
		if err := math.Abs(s - truth[i]); err > 0.05 {
			t.Fatalf("cell %d SOC %.4f vs truth %.4f: error %.1f%% > 5%%",
				i, s, truth[i], 100*err)
		}
	}
}

// TestSOCReproducibleWithSeed checks that the same seed yields the same
// estimator trajectory (fixed random seed requirement).
func TestSOCReproducibleWithSeed(t *testing.T) {
	run := func() []float64 {
		now := time.Unix(0, 0)
		cfg := testModelConfig(123)
		m := NewModel(cfg, now)
		ecfg := DefaultConfig().SOC
		est := NewEstimator(ecfg, m.TrueSOC(), cfg.SeriesCount)
		for i := 0; i < 3600; i++ {
			meas := m.Step(time.Second, Inputs{Current: 25, AmbientTemp: 25}, now.Add(time.Duration(i+1)*time.Second))
			est.Update(time.Second, meas, nil, nil)
		}
		return est.SOC()
	}
	a, b := run(), run()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-reproducible trajectory cell %d: %.12f != %.12f", i, a[i], b[i])
		}
	}
}

// TestSOCConvergesOnRestWithInjectedBias models "power cycled after rest":
// the estimator starts 10 percentage points off the real SOC and must be
// pulled to the OCV truth purely by resting with zero current.
func TestSOCConvergesOnRestWithInjectedBias(t *testing.T) {
	now := time.Unix(0, 0)
	cfg := testModelConfig(3)
	m := NewModel(cfg, now)
	ecfg := DefaultConfig().SOC
	ecfg.RestDuration = 30 * time.Second

	truth := m.TrueSOC()
	biased := append([]float64(nil), truth...)
	for i := range biased {
		biased[i] = clampF(biased[i]-0.10, 0, 1)
	}
	est := NewEstimator(ecfg, biased, cfg.SeriesCount)

	for i := 0; i < 20*60; i++ {
		clock := now.Add(time.Duration(i+1) * time.Second)
		meas := m.Step(time.Second, Inputs{Current: 0, AmbientTemp: 25}, clock)
		est.Update(time.Second, meas, nil, nil)
	}
	for i, s := range est.SOC() {
		if err := math.Abs(s - truth[i]); err > 0.01 {
			t.Fatalf("cell %d failed OCV convergence: %.4f vs truth %.4f (%.2f%%)",
				i, s, truth[i], 100*err)
		}
	}
}
