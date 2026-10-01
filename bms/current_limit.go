package bms

// Reasons reported when a current request is constrained.
const (
	ReasonNone             = ""
	ReasonLowTempCharge    = "charge_disabled_low_temperature"
	ReasonHighTemp         = "temperature_derating"
	ReasonSOCFull          = "soc_full"
	ReasonSOCLow           = "soc_empty"
	ReasonCellOverVoltage  = "cell_over_voltage_derating"
	ReasonCellUnderVoltage = "cell_under_voltage_derating"
)

// CurrentLimiter computes dynamic charge/discharge current limits from
// temperature (configurable derating curves), SOC and extreme cell voltages.
type CurrentLimiter struct {
	cfg Config
}

// NewCurrentLimiter constructs a limiter.
func NewCurrentLimiter(cfg Config) *CurrentLimiter {
	return &CurrentLimiter{cfg: cfg}
}

// LimitResult describes the applied current and why it was limited.
type LimitResult struct {
	Requested float64
	Applied   float64
	LimitA    float64
	Reason    string
}

// Apply clamps a request (positive charge, negative discharge) to the dynamic
// limit. Zero charge is permitted below 0 C (Li-ion charging restriction).
func (l *CurrentLimiter) Apply(requestA float64, soc float64, tempC float64,
	minCellV, maxCellV float64) LimitResult {
	base := l.baseCurrent()

	if requestA > 0 {
		fraction := l.cfg.ChargeDerating.FractionAt(tempC)
		limit := base * fraction
		reason := ReasonNone
		if fraction <= 0 {
			reason = ReasonLowTempCharge
		} else if fraction < 1 {
			reason = ReasonHighTemp
		}

		// SOC taper above 90 %; hard stop at the over-voltage region.
		if soc > 0.9 {
			taper := clampF(10*(1-soc), 0, 1)
			if taper < fraction {
				limit = base * taper
			}
			if reason == ReasonNone && taper < 1 {
				reason = ReasonSOCFull
			}
		}
		if maxCellV >= l.cfg.OverVoltage-0.05 {
			limit = 0
			reason = ReasonCellOverVoltage
		}

		applied := requestA
		if applied > limit {
			applied = limit
		}
		return LimitResult{Requested: requestA, Applied: applied, LimitA: limit, Reason: reason}
	}

	if requestA < 0 {
		fraction := l.cfg.DischargeDerating.FractionAt(tempC)
		limit := base * fraction
		reason := ReasonNone
		if fraction < 1 {
			reason = ReasonHighTemp
		}

		if soc < 0.1 {
			taper := clampF(soc/0.1, 0, 1)
			if taper < fraction {
				limit = base * taper
			}
			if reason == ReasonNone && taper < 1 {
				reason = ReasonSOCLow
			}
		}
		if minCellV <= l.cfg.UnderVoltage+0.05 {
			limit = 0
			reason = ReasonCellUnderVoltage
		}

		applied := requestA
		if -applied > limit {
			applied = -limit
		}
		return LimitResult{Requested: requestA, Applied: applied, LimitA: limit, Reason: reason}
	}

	return LimitResult{Requested: 0, Applied: 0, LimitA: base}
}

func (l *CurrentLimiter) baseCurrent() float64 {
	// Rated current is the larger of the two over-current trip thresholds.
	base := l.cfg.OverCurrentDischarge
	if l.cfg.OverCurrentCharge > base {
		base = l.cfg.OverCurrentCharge
	}
	return base
}

// FractionAt returns the allowed current fraction at a temperature using
// piecewise-linear interpolation; outside the curve it is clamped to the end
// points (which are expected to be 0 at the non-survivable ends).
func (d TemperatureDerating) FractionAt(tempC float64) float64 {
	pts := d.Points
	if tempC <= pts[0].TemperatureC {
		return pts[0].Fraction
	}
	last := pts[len(pts)-1]
	if tempC >= last.TemperatureC {
		return last.Fraction
	}
	for i := 1; i < len(pts); i++ {
		if tempC <= pts[i].TemperatureC {
			lo, hi := pts[i-1], pts[i]
			frac := (tempC - lo.TemperatureC) / (hi.TemperatureC - lo.TemperatureC)
			return lo.Fraction + frac*(hi.Fraction-lo.Fraction)
		}
	}
	return last.Fraction
}
