package bms

import (
	"math"
	"math/rand"
	"time"
)

// Simulator produces locally generated noisy pack measurements.
// Current sign convention: I > 0 charges the pack.
type Simulator struct {
	cfg Config
	rng *rand.Rand

	trueSOC    []float64
	requestedA float64
	drift      float64
	ambientC   float64
	tempC      float64
	switching  []bool

	shortCircuit bool
}

const (
	simInternalResistanceOhm = 0.05
	simNoiseCurrentA         = 0.02
	simNoiseVoltageV         = 0.002
	simDriftRandWalkA        = 0.0008
	simDriftMaxA             = 0.08
	simThermalGain           = 0.014
	simThermalCoolingPerSec  = 0.02
	simCoulombEfficiency     = 0.995
)

// NewSimulator constructs a deterministic simulator. Initial SOC applies to
// every cell. Results are reproducible for a given cfg.Seed.
func NewSimulator(cfg Config, initialSOC float64) *Simulator {
	s := &Simulator{
		cfg:       cfg,
		rng:       rand.New(rand.NewSource(cfg.Seed)),
		trueSOC:   make([]float64, cfg.CellCount),
		switching: make([]bool, cfg.CellCount),
		ambientC:  25.0,
		tempC:     25.0,
	}
	for i := range s.trueSOC {
		s.trueSOC[i] = clampF(initialSOC, 0, 1)
	}
	return s
}

// SetRequest configures the pack current applied by the battery model.
func (s *Simulator) SetRequest(currentA float64) { s.requestedA = currentA }

// SetAmbient sets the ambient temperature used by the thermal model.
func (s *Simulator) SetAmbient(celsius float64) { s.ambientC = celsius }

// SetBalancing tells the simulator which cells have shunt switches closed.
func (s *Simulator) SetBalancing(switching []bool) {
	copy(s.switching, switching)
}

// InjectShortCircuit simulates a hard pack short-circuit event.
func (s *Simulator) InjectShortCircuit(on bool) { s.shortCircuit = on }

// SetCellSOC overrides the ground-truth SOC of one cell (test hook).
func (s *Simulator) SetCellSOC(index int, soc float64) {
	s.trueSOC[index] = clampF(soc, 0, 1)
}

// TrueSOC returns a copy of the ground-truth per-cell SOC.
func (s *Simulator) TrueSOC() []float64 {
	out := make([]float64, len(s.trueSOC))
	copy(out, s.trueSOC)
	return out
}

// RestoreTrueSOC re-installs ground-truth SOC after a crash.
func (s *Simulator) RestoreTrueSOC(socs []float64) {
	copy(s.trueSOC, socs)
}

// Sample advances the model by one SampleInterval and returns a noisy frame.
func (s *Simulator) Sample(now time.Time) Measurement {
	dt := s.cfg.SampleInterval.Seconds()

	// Measured current carries white noise, a slow random-walk drift and a
	// deterministic slow sinusoidal bias.
	s.drift += s.rng.NormFloat64() * simDriftRandWalkA
	s.drift = clampF(s.drift, -simDriftMaxA, simDriftMaxA)
	slowSine := 0.02 * math.Sin(float64(now.UnixNano())/float64(5*time.Minute))

	trueCurrent := s.requestedA
	if s.shortCircuit {
		trueCurrent = -s.cfg.ShortCircuitCurrent
	}
	measuredCurrent := trueCurrent + s.drift + slowSine +
		s.rng.NormFloat64()*simNoiseCurrentA

	// Ground-truth coulomb counting; a closed balancing shunt removes charge
	// only from its own cell. Charging carries a small efficiency loss.
	for i := range s.trueSOC {
		shuntA := 0.0
		if s.switching[i] {
			shuntA = s.cfg.MaxBalanceCurrentA
		}
		dSOC := (trueCurrent - shuntA) * dt / 3600.0 / s.cfg.CapacityAh
		if trueCurrent > 0 {
			dSOC *= simCoulombEfficiency
		}
		s.trueSOC[i] = clampF(s.trueSOC[i]+dSOC, 0, 1)
	}

	// First-order thermal model driven by I^2 heating.
	heat := simThermalGain * trueCurrent * trueCurrent
	cool := simThermalCoolingPerSec * (s.tempC - s.ambientC)
	s.tempC += dt * (heat - cool)

	cells := make([]float64, s.cfg.CellCount)
	temps := make([]float64, s.cfg.CellCount)
	for i := range cells {
		ocv := s.cfg.OCVTable.Voltage(s.trueSOC[i])
		v := ocv + trueCurrent*simInternalResistanceOhm
		if s.switching[i] {
			// Closed shunt loads the cell; terminal voltage reads lower.
			v -= s.cfg.MaxBalanceCurrentA * simInternalResistanceOhm
		}
		cells[i] = v + s.rng.NormFloat64()*simNoiseVoltageV
		temps[i] = s.tempC + s.rng.NormFloat64()*0.15
	}

	return Measurement{
		Time:          now,
		CurrentA:      measuredCurrent,
		CellVoltagesV: cells,
		TempsC:        temps,
	}
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
