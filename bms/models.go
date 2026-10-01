package bms

import "time"

// CurrentSign: I > 0 means charging, I < 0 means discharging.

// Measurement is one sampled frame of pack inputs.
type Measurement struct {
	Time          time.Time
	CurrentA      float64
	CellVoltagesV []float64
	TempsC        []float64
}

// OCVPoint maps a state-of-charge fraction to an open-circuit cell voltage.
type OCVPoint struct {
	SOC      float64
	VoltageV float64
}

// OCVTable is a monotonic SOC -> open-circuit-voltage lookup table.
type OCVTable struct {
	Points []OCVPoint
}

// SOC maps a resting open-circuit cell voltage to a SOC fraction [0,1]
// using piecewise-linear interpolation, clamped to the table ends.
func (t OCVTable) SOC(voltageV float64) float64 {
	pts := t.Points
	if voltageV <= pts[0].VoltageV {
		return pts[0].SOC
	}
	last := pts[len(pts)-1]
	if voltageV >= last.VoltageV {
		return last.SOC
	}
	for i := 1; i < len(pts); i++ {
		if voltageV <= pts[i].VoltageV {
			lo, hi := pts[i-1], pts[i]
			frac := (voltageV - lo.VoltageV) / (hi.VoltageV - lo.VoltageV)
			return lo.SOC + frac*(hi.SOC-lo.SOC)
		}
	}
	return last.SOC
}

// Voltage maps a SOC fraction to the corresponding open-circuit cell voltage,
// using piecewise-linear interpolation, clamped to the table ends.
func (t OCVTable) Voltage(soc float64) float64 {
	pts := t.Points
	if soc <= pts[0].SOC {
		return pts[0].VoltageV
	}
	last := pts[len(pts)-1]
	if soc >= last.SOC {
		return last.VoltageV
	}
	for i := 1; i < len(pts); i++ {
		if soc <= pts[i].SOC {
			lo, hi := pts[i-1], pts[i]
			frac := (soc - lo.SOC) / (hi.SOC - lo.SOC)
			return lo.VoltageV + frac*(hi.VoltageV-lo.VoltageV)
		}
	}
	return last.VoltageV
}

// CellState is the externally visible state of one cell.
type CellState struct {
	Index            int
	VoltageV         float64
	SOC              float64
	Balancing        bool
	BalancedAh       float64
	BalanceResistorV float64
}

// PackSnapshot is a self-consistent read-only view of the whole system.
type PackSnapshot struct {
	Time           time.Time
	Cells          []CellState
	PackSOC        float64
	CurrentA       float64
	AppliedCurrent float64
	LimitA         float64
	LimitReason    string
	TempC          float64
	FaultLatched   bool
	ActiveFaults   []FaultType
	CumulativeAh   float64
}

// FaultType identifies a protection fault category.
type FaultType string

const (
	FaultOverVoltage     FaultType = "over_voltage"
	FaultUnderVoltage    FaultType = "under_voltage"
	FaultOverTemperature FaultType = "over_temperature"
	FaultOverCurrent     FaultType = "over_current"
	FaultShortCircuit    FaultType = "short_circuit"
	FaultLatchReset      FaultType = "latch_reset"
)

// FaultRecord stores one fault transition for traceability.
type FaultRecord struct {
	ID        int64
	Time      time.Time
	Type      FaultType
	Action    FaultAction
	CellIndex int
	VoltageV  float64
	TempC     float64
	CurrentA  float64
	SOC       float64
	Message   string
}

// FaultAction is whether a fault was triggered or cleared.
type FaultAction string

const (
	FaultTriggered FaultAction = "triggered"
	FaultCleared   FaultAction = "cleared"
)

// FaultPage is one time-ordered page of fault history.
type FaultPage struct {
	Records  []FaultRecord
	Total    int
	Page     int
	PageSize int
}
