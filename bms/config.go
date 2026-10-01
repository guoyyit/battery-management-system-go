package bms

import (
	"fmt"
	"time"
)

// Config aggregates every subsystem configuration of the battery manager.
type Config struct {
	Model      ModelConfig
	SOC        EstimatorConfig
	Balance    BalancerConfig
	Limits     LimitConfig
	Protection ProtectionConfig

	// StateDir is where snapshots and the fault history are persisted.
	StateDir string
	// PersistEvery bounds the amount of metered charge lost on crash.
	PersistEvery time.Duration
	// MaxStep is the largest integration step accepted by BMS.Step.
	MaxStep time.Duration
}

// DefaultConfig returns a configuration for a 4S 100 Ah pack.
func DefaultConfig() *Config {
	ocv := DefaultOCVTable()
	return &Config{
		Model: ModelConfig{
			SeriesCount:         4,
			NominalCapacityAh:   100,
			CellResistanceOhm:   0.002,
			CurrentNoiseStd:     0.1,
			CurrentDriftPerHour: 0.3,
			VoltageNoiseStd:     0.002,
			TempNoiseStd:        0.1,
			InternalHeatPerWatt: 0.00012,
			CoolingPerSecond:    0.002,
			OCV:                 ocv,
			Seed:                1,
		},
		SOC: EstimatorConfig{
			CapacityAh:            100,
			OCV:                   ocv,
			RestCurrentThresholdA: 2,
			RestDuration:          10 * time.Minute,
			OCVCorrectionRate:     0.02,
			MaxCorrectPerRest:     0.15,
			BypassResistorOhm:     1.6,
		},
		Balance: BalancerConfig{
			Mode:              BalanceByVoltage,
			VoltageThresholdV: 0.015,
			SOCThreshold:      0.01,
			Hysteresis:        0.005,
			MaxBalanceA:       2,
			MaxConcurrent:     2,
			ArmCurrentA:       3,
			BypassResistorOhm: 1.6,
			MinSOC:            0.05,
		},
		Limits: LimitConfig{
			MaxChargeA:    100,
			MaxDischargeA: 100,
			ChargeTempCurve: []DeratingPoint{
				{TempC: -10, ChargeFactor: 0},
				{TempC: 0, ChargeFactor: 0.3},
				{TempC: 10, ChargeFactor: 0.7},
				{TempC: 25, ChargeFactor: 1},
				{TempC: 40, ChargeFactor: 0.8},
				{TempC: 45, ChargeFactor: 0.3},
				{TempC: 50, ChargeFactor: 0},
			},
			DischargeTempCurve: []DeratingPoint{
				{TempC: -20, DischargeFactor: 0.3},
				{TempC: -10, DischargeFactor: 0.6},
				{TempC: 0, DischargeFactor: 0.9},
				{TempC: 25, DischargeFactor: 1},
				{TempC: 50, DischargeFactor: 0.8},
				{TempC: 55, DischargeFactor: 0.3},
				{TempC: 60, DischargeFactor: 0},
			},
			CellHardOverV:  3.45,
			CellHardUnderV: 3.00,
			VoltageBandV:   0.10,
			SOCTaperHigh:   0.95,
			SOCTaperLow:    0.05,
		},
		Protection: ProtectionConfig{
			CellOverV:       3.65,
			CellOverVClear:  3.45,
			CellUnderV:      2.80,
			CellUnderVClear: 3.00,
			OvertempC:       60,
			OvertempClearC:  50,
			OCPChargeA:      110,
			OCPDischargeA:   120,
			ShortCircuitA:   300,
			TripDebounce:    500 * time.Millisecond,
		},
		StateDir:     "bms-state",
		PersistEvery: 5 * time.Second,
		MaxStep:      time.Second,
	}
}

// Validate checks cross-field invariants.
func (c *Config) Validate() error {
	if c.Model.SeriesCount <= 0 {
		return fmt.Errorf("model: series count must be positive")
	}
	n := c.Model.SeriesCount
	if c.Model.NominalCapacityAh <= 0 {
		return fmt.Errorf("model: nominal capacity must be positive")
	}
	if len(c.Model.InitialSOC) > 0 && len(c.Model.InitialSOC) != n {
		return fmt.Errorf("model: initial SOC length %d != %d", len(c.Model.InitialSOC), n)
	}
	if c.SOC.CapacityAh <= 0 {
		return fmt.Errorf("soc: capacity must be positive")
	}
	if c.SOC.RestDuration <= 0 || c.SOC.OCVCorrectionRate <= 0 || c.SOC.OCVCorrectionRate > 1 {
		return fmt.Errorf("soc: rest duration and correction gain must be valid")
	}
	if c.Balance.MaxBalanceA < 0 || c.Balance.MaxConcurrent < 0 ||
		c.Balance.BypassResistorOhm <= 0 || c.Balance.Hysteresis < 0 {
		return fmt.Errorf("balance: invalid balancer parameters")
	}
	if c.Limits.MaxChargeA < 0 || c.Limits.MaxDischargeA < 0 || c.Limits.VoltageBandV <= 0 {
		return fmt.Errorf("limits: invalid current limits")
	}
	if err := validateDerating(c.Limits.ChargeTempCurve); err != nil {
		return fmt.Errorf("limits: charge curve: %w", err)
	}
	if err := validateDerating(c.Limits.DischargeTempCurve); err != nil {
		return fmt.Errorf("limits: discharge curve: %w", err)
	}
	p := c.Protection
	if !(p.CellOverVClear < p.CellOverV) || !(p.CellUnderVClear > p.CellUnderV) {
		return fmt.Errorf("protection: voltage clear levels must provide hysteresis")
	}
	if !(p.OvertempClearC < p.OvertempC) {
		return fmt.Errorf("protection: overtemp clear level must be below trip")
	}
	if p.ShortCircuitA > 0 && p.ShortCircuitA <= p.OCPChargeA {
		return fmt.Errorf("protection: short-circuit threshold must exceed charge OCP")
	}
	if c.MaxStep <= 0 {
		return fmt.Errorf("max step must be positive")
	}
	return nil
}

func validateDerating(curve []DeratingPoint) error {
	for i, p := range curve {
		if p.ChargeFactor < 0 || p.ChargeFactor > 1 ||
			p.DischargeFactor < 0 || p.DischargeFactor > 1 {
			return fmt.Errorf("factor out of [0,1]")
		}
		if i > 0 && curve[i-1].TempC >= p.TempC {
			return fmt.Errorf("temperature knots must be strictly increasing")
		}
	}
	return nil
}
