package bms

import (
	"math"
	"math/rand/v2"
	"time"
)

// CellParams describes per-cell manufacturing spread.
type CellParams struct {
	CapacityAh    float64
	ResistanceOhm float64
	VocBias       float64
}

// ModelConfig configures the simulated battery pack.
type ModelConfig struct {
	SeriesCount       int
	NominalCapacityAh float64
	InitialSOC        []float64

	CellResistanceOhm float64
	Cells             []CellParams

	// Sensor imperfections.
	CurrentNoiseStd     float64
	CurrentDriftPerHour float64
	VoltageNoiseStd     float64
	TempNoiseStd        float64

	// Thermal model (simple lumped parameters).
	InternalHeatPerWatt float64
	CoolingPerSecond    float64

	OCV  OCVTable
	Seed int64
}

// Inputs are the externally commanded conditions for one simulation step.
type Inputs struct {
	// Current follows the discharge-positive convention (A).
	Current float64
	// AmbientTemp is the environment temperature in degrees Celsius.
	AmbientTemp float64
}

// Measurement is one noisy sample of the pack.
type Measurement struct {
	Time          time.Time
	CellVoltage   []float64
	PackVoltage   float64
	CurrentRaw    float64
	CurrentSensed float64
	Temperature   []float64
}

// Model simulates a series-connected cell pack with noisy sensors.
//
// Physics: every cell has its own capacity, internal resistance and OCV bias.
// Current follows the discharge-positive convention; per-cell SOC integrates
// pack current plus the local bypass (balancing) discharge. Terminal voltage
// is OCV minus the resistive drop of the current actually flowing through the
// cell. The current sensor adds white noise and a mean-reverting slow drift,
// matching the requirement of "noise plus gradual drift" inputs.
type Model struct {
	cfg      ModelConfig
	params   []CellParams
	soc      []float64
	tempC    []float64
	rng      *rand.Rand
	drift    float64
	now      time.Time
	balOn    []bool
	balCurr  []float64
	spare    float64
	hasSpare bool
}

const secondsPerHour = 3600.0

// NewModel builds a model. Initial SOC comes from the configuration.
func NewModel(cfg ModelConfig, now time.Time) *Model {
	n := cfg.SeriesCount
	params := make([]CellParams, n)
	copy(params, cfg.Cells)
	if len(params) < n {
		params = append(params, make([]CellParams, n-len(params))...)
	}
	socSpread := []float64{0.03, 0.01, -0.01, -0.03}
	soc := make([]float64, n)
	for i := 0; i < n; i++ {
		p := &params[i]
		if p.CapacityAh <= 0 {
			p.CapacityAh = cfg.NominalCapacityAh
		}
		if p.ResistanceOhm <= 0 {
			p.ResistanceOhm = cfg.CellResistanceOhm
		}
		switch {
		case i < len(cfg.InitialSOC):
			soc[i] = cfg.InitialSOC[i]
		case i < len(socSpread):
			soc[i] = clampF(0.5+socSpread[i], 0, 1)
		default:
			soc[i] = 0.5
		}
	}
	temp := make([]float64, n)
	for i := range temp {
		temp[i] = 25
	}
	return &Model{
		cfg:    cfg,
		params: params,
		soc:    soc,
		tempC:  temp,
		rng: rand.New(rand.NewPCG(
			uint64(uint32(cfg.Seed))+1,
			uint64(uint32(cfg.Seed>>32))+0x9e3779b9,
		)),
		now:     now,
		balOn:   make([]bool, n),
		balCurr: make([]float64, n),
	}
}

// Reset overwrites true cell SOC (used on crash recovery).
func (m *Model) Reset(soc []float64, now time.Time) {
	for i := 0; i < m.cfg.SeriesCount && i < len(soc); i++ {
		m.soc[i] = clampF(soc[i], 0, 1)
	}
	m.now = now
}

// Step advances the physics by dt and returns a noisy measurement.
func (m *Model) Step(dt time.Duration, in Inputs, now time.Time) Measurement {
	n := m.cfg.SeriesCount
	h := dt.Seconds() / secondsPerHour

	// Slow drift: Ornstein-Uhlenbeck process, mean reverting to zero with a
	// multi-hour time constant; white sensor noise is added separately below.
	// Its stationary RMS equals CurrentDriftPerHour (interpreted as amperes).
	const driftTauSeconds = 2 * secondsPerHour
	theta := dt.Seconds() / driftTauSeconds
	diffusion := m.cfg.CurrentDriftPerHour * math.Sqrt(2*dt.Seconds()/driftTauSeconds)
	if diffusion > 0 {
		m.drift += -theta*m.drift + diffusion*m.gauss()
	}

	volt := make([]float64, n)
	packV := 0.0
	for i := 0; i < n; i++ {
		balI := 0.0
		if m.balOn[i] {
			balI = m.balCurr[i]
		}
		// Discharge through the bypass resistor additionally drains that cell.
		m.soc[i] -= (in.Current + balI) * h / m.params[i].CapacityAh
		m.soc[i] = clampF(m.soc[i], 0, 1)

		cellI := in.Current
		if m.balOn[i] {
			cellI += balI
		}
		ocv := m.cfg.OCV.VocAtSOC(m.soc[i]) + m.params[i].VocBias
		v := ocv - m.params[i].ResistanceOhm*cellI
		volt[i] = v + m.cfg.VoltageNoiseStd*m.gauss()
		packV += volt[i]
	}

	// Lumped thermal model: I^2 R heating, Newton cooling toward ambient.
	tAmb := in.AmbientTemp
	if tAmb == 0 {
		tAmb = 25
	}
	for i := 0; i < n; i++ {
		heat := m.cfg.InternalHeatPerWatt * in.Current * in.Current
		dT := heat - m.cfg.CoolingPerSecond*(m.tempC[i]-tAmb)
		m.tempC[i] += dT * dt.Seconds()
	}
	temps := make([]float64, n)
	for i := range temps {
		temps[i] = m.tempC[i] + m.cfg.TempNoiseStd*m.gauss()
	}

	sensed := in.Current + m.drift + m.cfg.CurrentNoiseStd*m.gauss()
	m.now = now
	return Measurement{
		Time:          now,
		CellVoltage:   volt,
		PackVoltage:   packV,
		CurrentRaw:    in.Current,
		CurrentSensed: sensed,
		Temperature:   temps,
	}
}

// TrueSOC returns the simulated ground-truth per-cell SOC.
func (m *Model) TrueSOC() []float64 {
	out := make([]float64, len(m.soc))
	copy(out, m.soc)
	return out
}

// setBypass informs the model which bypass switches conduct and how much
// current they draw, so balancing physically drains the corresponding cells.
func (m *Model) setBypass(active []bool, currentA []float64) {
	for i := 0; i < len(m.balOn) && i < len(active); i++ {
		m.balOn[i] = active[i]
		if i < len(currentA) {
			m.balCurr[i] = currentA[i]
		}
	}
}

func (m *Model) gauss() float64 {
	if m.rng == nil {
		return 0
	}
	if m.hasSpare {
		m.hasSpare = false
		return m.spare
	}
	// Box-Muller transform over the deterministic PRNG.
	u1 := 1 - m.rng.Float64()
	u2 := 1 - m.rng.Float64()
	r := math.Sqrt(-2 * math.Log(u1))
	m.spare = r * math.Sin(2*math.Pi*u2)
	m.hasSpare = true
	return r * math.Cos(2*math.Pi*u2)
}

func clampF(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
