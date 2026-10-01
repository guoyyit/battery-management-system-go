package bms

import "time"

import "errors"

// Config describes a configurable series-connected battery pack.
type Config struct {
	CellCount      int
	CapacityAh     float64
	SampleInterval time.Duration

	// Voltage thresholds (volts per cell).
	OverVoltage  float64
	UnderVoltage float64

	// Hysteresis bands around the voltage thresholds.
	OverVoltageHysteresis  float64
	UnderVoltageHysteresis float64

	// Temperature thresholds (degrees Celsius).
	OverTemperature float64
	OverTempHyst    float64

	// Pack current thresholds (amperes, positive = charge).
	OverCurrentCharge    float64
	OverCurrentDischarge float64
	OverCurrentHyst      float64

	ShortCircuitCurrent  float64
	ShortCircuitDuration time.Duration

	// Passive balancing parameters.
	BalanceDeltaV      float64
	BalanceDeltaSOC    float64
	MaxBalanceCurrentA float64

	// Temperature derating curves.
	ChargeDerating    TemperatureDerating
	DischargeDerating TemperatureDerating

	// Open-circuit-voltage correction.
	OCVTable          OCVTable
	RestForOCV        time.Duration
	OCVCorrectionGain float64
	MaxOCVCorrectionV float64

	// Persistence.
	StorePath string

	// Simulation.
	Seed int64
}

// TemperatureDerating maps temperature (Celsius) to a current fraction [0,1].
type TemperatureDerating struct {
	Points []DeratingPoint
}

// DeratingPoint is one point on a temperature derating curve.
type DeratingPoint struct {
	TemperatureC float64
	Fraction     float64
}

// DefaultConfig returns a usable configuration for an N-cell pack.
// Cell model: generic Li-ion cell, 2.5 Ah, 3.0 V empty / 4.2 V full.
func DefaultConfig(cellCount int, capacityAh float64) Config {
	return Config{
		CellCount:      cellCount,
		CapacityAh:     capacityAh,
		SampleInterval: 100 * time.Millisecond,

		OverVoltage:            4.20,
		UnderVoltage:           3.00,
		OverVoltageHysteresis:  0.10,
		UnderVoltageHysteresis: 0.10,

		OverTemperature: 60.0,
		OverTempHyst:    5.0,

		OverCurrentCharge:    2.0 * capacityAh,
		OverCurrentDischarge: 3.0 * capacityAh,
		OverCurrentHyst:      0.2 * capacityAh,

		ShortCircuitCurrent:  6.0 * capacityAh,
		ShortCircuitDuration: 5 * time.Millisecond,

		BalanceDeltaV:      0.030,
		BalanceDeltaSOC:    0.02,
		MaxBalanceCurrentA: 0.05 * capacityAh,

		ChargeDerating: TemperatureDerating{Points: []DeratingPoint{
			{TemperatureC: 0, Fraction: 0.0},
			{TemperatureC: 10, Fraction: 1.0},
			{TemperatureC: 45, Fraction: 1.0},
			{TemperatureC: 55, Fraction: 0.0},
		}},
		DischargeDerating: TemperatureDerating{Points: []DeratingPoint{
			{TemperatureC: -20, Fraction: 0.0},
			{TemperatureC: -10, Fraction: 0.5},
			{TemperatureC: 0, Fraction: 1.0},
			{TemperatureC: 55, Fraction: 1.0},
			{TemperatureC: 60, Fraction: 0.0},
		}},

		OCVTable:          defaultOCVTable(),
		RestForOCV:        10 * time.Minute,
		OCVCorrectionGain: 0.1,
		MaxOCVCorrectionV: 0.30,

		StorePath: "bms_state.json",
		Seed:      1,
	}
}

func defaultOCVTable() OCVTable {
	return OCVTable{Points: []OCVPoint{
		{SOC: 0.00, VoltageV: 3.000},
		{SOC: 0.10, VoltageV: 3.480},
		{SOC: 0.20, VoltageV: 3.580},
		{SOC: 0.30, VoltageV: 3.660},
		{SOC: 0.40, VoltageV: 3.720},
		{SOC: 0.50, VoltageV: 3.780},
		{SOC: 0.60, VoltageV: 3.840},
		{SOC: 0.70, VoltageV: 3.900},
		{SOC: 0.80, VoltageV: 3.980},
		{SOC: 0.90, VoltageV: 4.080},
		{SOC: 1.00, VoltageV: 4.200},
	}}
}

// Validate checks the configuration for internal consistency.
func (c Config) Validate() error {
	if c.CellCount <= 0 {
		return errors.New("CellCount must be positive")
	}
	if c.CapacityAh <= 0 {
		return errors.New("CapacityAh must be positive")
	}
	if c.SampleInterval <= 0 {
		return errors.New("SampleInterval must be positive")
	}
	if !(c.UnderVoltage < c.OverVoltage) {
		return errors.New("UnderVoltage must be below OverVoltage")
	}
	if c.OverVoltageHysteresis < 0 || c.UnderVoltageHysteresis < 0 {
		return errors.New("hysteresis must be non-negative")
	}
	if c.OverTemperature <= -40 || c.OverTempHyst < 0 {
		return errors.New("invalid temperature threshold settings")
	}
	if c.OverCurrentCharge < 0 || c.OverCurrentDischarge < 0 {
		return errors.New("over-current thresholds must be non-negative")
	}
	if c.BalanceDeltaV < 0 || c.MaxBalanceCurrentA < 0 {
		return errors.New("balance parameters must be non-negative")
	}
	if c.OCVCorrectionGain < 0 || c.OCVCorrectionGain > 1 {
		return errors.New("OCVCorrectionGain must be in [0,1]")
	}
	if err := c.OCVTable.validate(); err != nil {
		return err
	}
	if err := c.ChargeDerating.validate(); err != nil {
		return err
	}
	return c.DischargeDerating.validate()
}

func (d TemperatureDerating) validate() error {
	if len(d.Points) < 2 {
		return errors.New("derating curve needs at least two points")
	}
	for i, p := range d.Points {
		if p.Fraction < 0 || p.Fraction > 1 {
			return errors.New("derating fraction must be in [0,1]")
		}
		if i > 0 && p.TemperatureC <= d.Points[i-1].TemperatureC {
			return errors.New("derating temperatures must be strictly increasing")
		}
	}
	return nil
}

func (t OCVTable) validate() error {
	if len(t.Points) < 2 {
		return errors.New("OCV table needs at least two points")
	}
	for i, p := range t.Points {
		if p.SOC < 0 || p.SOC > 1 {
			return errors.New("OCV table SOC must be in [0,1]")
		}
		if i > 0 {
			prev := t.Points[i-1]
			if p.SOC <= prev.SOC || p.VoltageV <= prev.VoltageV {
				return errors.New("OCV table must be strictly increasing in SOC and voltage")
			}
		}
	}
	return nil
}
