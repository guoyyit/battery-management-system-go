package bms

import "time"

// Rest current band: |I| below this (amperes) counts as resting.
const restCurrentThresholdA = 0.05

// SOCEstimator combines coulomb counting with open-circuit-voltage correction.
// Coulomb counting runs every sample; OCV correction only runs after the pack
// has rested (|I| small) for RestForOCV, and is applied as a bounded, gradual
// blend so a single noisy reading cannot move the estimate far.
type SOCEstimator struct {
	cfg Config

	soc       []float64
	integAh   []float64
	restSince time.Time
	resting   bool
	lastTime  time.Time
}

// NewSOCEstimator constructs an estimator starting at the given SOC.
func NewSOCEstimator(cfg Config, soc float64) *SOCEstimator {
	e := &SOCEstimator{cfg: cfg}
	e.soc = make([]float64, cfg.CellCount)
	e.integAh = make([]float64, cfg.CellCount)
	for i := range e.soc {
		e.soc[i] = clampF(soc, 0, 1)
	}
	return e
}

// SOC returns the pack (mean cell) SOC estimate in [0,1].
func (e *SOCEstimator) SOC() float64 {
	return meanF(e.soc)
}

// CellSOC returns a copy of per-cell SOC estimates.
func (e *SOCEstimator) CellSOC() []float64 {
	out := make([]float64, len(e.soc))
	copy(out, e.soc)
	return out
}

// IntegratedAh returns cumulative net charge integrated per cell (Ah).
func (e *SOCEstimator) IntegratedAh() []float64 {
	out := make([]float64, len(e.integAh))
	copy(out, e.integAh)
	return out
}

// Resting reports whether the pack has rested long enough for OCV correction.
func (e *SOCEstimator) Resting(now time.Time) bool {
	return e.resting
}

// Observe integrates one sample and, when rested, applies OCV correction.
func (e *SOCEstimator) Observe(m Measurement) {
	if e.lastTime.IsZero() {
		e.lastTime = m.Time
		return
	}
	dt := m.Time.Sub(e.lastTime).Seconds()
	e.lastTime = m.Time
	if dt <= 0 {
		return
	}

	// Coulomb counting: dSOC = I*dt / (3600 * CapacityAh).
	for i := range e.soc {
		dAh := m.CurrentA * dt / 3600.0
		e.integAh[i] += dAh
		e.soc[i] = clampF(e.soc[i]+dAh/e.cfg.CapacityAh, 0, 1)
	}

	if absF(m.CurrentA) <= restCurrentThresholdA {
		if e.restSince.IsZero() {
			e.restSince = m.Time
		}
		if !e.resting && m.Time.Sub(e.restSince) >= e.cfg.RestForOCV {
			e.resting = true
		}
	} else {
		e.restSince = time.Time{}
		e.resting = false
	}

	if e.resting {
		e.applyOCV(m, e.cfg.OCVCorrectionGain, true)
	}
}

// ForceOCVCorrect performs an immediate, full OCV correction from a resting
// measurement. Used at boot so a restart after standing converges to the
// open-circuit SOC instead of trusting stale integrated charge.
func (e *SOCEstimator) ForceOCVCorrect(m Measurement) {
	e.lastTime = m.Time
	e.restSince = m.Time
	e.resting = true
	e.applyOCV(m, 1.0, false)
}

// applyOCV blends each cell toward the OCV-table SOC. Gradual corrections are
// bounded per call by MaxOCVCorrectionV so one noisy resting sample cannot
// move the estimate far; boot-time force correction is unbounded.
func (e *SOCEstimator) applyOCV(m Measurement, gain float64, bounded bool) {
	maxDSOC := e.cfg.MaxOCVCorrectionV / e.ocvSpanVolts()
	for i := 0; i < len(e.soc) && i < len(m.CellVoltagesV); i++ {
		target := e.cfg.OCVTable.SOC(m.CellVoltagesV[i])
		delta := gain * (target - e.soc[i])
		if bounded {
			delta = clampF(delta, -maxDSOC, maxDSOC)
		}
		e.soc[i] = clampF(e.soc[i]+delta, 0, 1)
	}
}

// ocvSpanVolts converts the voltage-based correction cap into SOC space.
func (e *SOCEstimator) ocvSpanVolts() float64 {
	pts := e.cfg.OCVTable.Points
	span := pts[len(pts)-1].VoltageV - pts[0].VoltageV
	if span <= 0 {
		return 1
	}
	return span
}

// Restore loads SOC and integrated-charge state after a crash.
func (e *SOCEstimator) Restore(cellSOC, integAh []float64) {
	copy(e.soc, cellSOC)
	copy(e.integAh, integAh)
}

func meanF(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
