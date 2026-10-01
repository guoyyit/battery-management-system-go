package bms

import (
	"math"
	"time"
)

// EstimatorConfig configures coulomb counting with OCV correction.
type EstimatorConfig struct {
	CapacityAh float64
	OCV        OCVTable

	// Rest detection.
	RestCurrentThresholdA float64
	RestDuration          time.Duration

	// OCVCorrectionRate is the per-second exponential blending rate toward
	// the OCV-implied SOC once the pack qualifies as resting (e.g. 0.02 ->
	// a ~50 s convergence time constant).
	OCVCorrectionRate float64
	// MaxCorrectPerRest bounds how far a single rest episode may move each
	// cell's estimate, rejecting a single bad OCV sample.
	MaxCorrectPerRest float64

	// BypassResistorOhm reconstructs the open-circuit voltage from a loaded
	// terminal sample while a balancing bypass switch is closed.
	BypassResistorOhm float64
}

// Estimator tracks per-cell SOC via coulomb counting and OCV correction.
type Estimator struct {
	cfg           EstimatorConfig
	soc           []float64
	restElapsed   time.Duration
	resting       bool
	wasResting    bool
	restApplied   []float64
	lastCorrected bool
}

// NewEstimator seeds the estimator; missing cells default to 50%.
func NewEstimator(cfg EstimatorConfig, initialSOC []float64, cellCount int) *Estimator {
	soc := make([]float64, cellCount)
	for i := range soc {
		soc[i] = 0.5
	}
	for i := 0; i < cellCount && i < len(initialSOC); i++ {
		soc[i] = clampF(initialSOC[i], 0, 1)
	}
	return &Estimator{
		cfg:         cfg,
		soc:         soc,
		restApplied: make([]float64, cellCount),
	}
}

// Update integrates current and applies OCV correction while resting.
// balancing[i] tells whether cell i is being discharged by its bypass resistor.
func (e *Estimator) Update(dt time.Duration, m Measurement, balancing []bool, balanceCurrent []float64) (soc []float64, corrected bool) {
	n := len(e.soc)
	h := dt.Seconds() / secondsPerHour
	cur := m.CurrentSensed

	// Rest detection: sensed current magnitude below threshold for RestDuration.
	if math.Abs(cur) <= e.cfg.RestCurrentThresholdA {
		e.restElapsed += dt
	} else {
		e.restElapsed = 0
	}
	e.resting = e.restElapsed >= e.cfg.RestDuration

	// Reset the per-episode correction budget when a new rest begins.
	if e.resting && !e.wasResting {
		for i := range e.restApplied {
			e.restApplied[i] = 0
		}
	}
	e.wasResting = e.resting

	// Coulomb counting. Balancing discharge only removes charge from the cell
	// whose bypass switch is closed.
	for i := 0; i < n; i++ {
		balI := 0.0
		if i < len(balancing) && balancing[i] && i < len(balanceCurrent) {
			balI = balanceCurrent[i]
		}
		e.soc[i] -= (cur + balI) * h / e.cfg.CapacityAh
		e.soc[i] = clampF(e.soc[i], 0, 1)
	}

	// OCV correction. The bypass resistive drop is added back so that the
	// terminal sample of a balancing cell still maps to a valid open-circuit
	// voltage.
	e.lastCorrected = false
	if e.resting {
		alpha := clampF(e.cfg.OCVCorrectionRate*dt.Seconds(), 0, 1)
		for i := 0; i < n && i < len(m.CellVoltage); i++ {
			voc := m.CellVoltage[i]
			if i < len(balancing) && balancing[i] && i < len(balanceCurrent) && e.cfg.BypassResistorOhm > 0 {
				voc += balanceCurrent[i] * e.cfg.BypassResistorOhm
			}
			target := e.cfg.OCV.SOCAtVoc(voc)
			delta := alpha * (target - e.soc[i])

			// Bound the total movement within this rest episode.
			room := e.cfg.MaxCorrectPerRest - math.Abs(e.restApplied[i])
			if room <= 0 {
				continue
			}
			if math.Abs(delta) > room {
				if delta > 0 {
					delta = room
				} else {
					delta = -room
				}
			}
			e.soc[i] = clampF(e.soc[i]+delta, 0, 1)
			e.restApplied[i] += delta
		}
		corrected = true
		e.lastCorrected = true
	}

	return e.SOC(), corrected
}

// SOC returns the current per-cell estimate.
func (e *Estimator) SOC() []float64 {
	out := make([]float64, len(e.soc))
	copy(out, e.soc)
	return out
}

// Resting reports whether the pack currently satisfies the rest condition.
func (e *Estimator) Resting() bool { return e.resting }

// RestElapsed reports how long the pack has been continuously resting.
func (e *Estimator) RestElapsed() time.Duration { return e.restElapsed }
