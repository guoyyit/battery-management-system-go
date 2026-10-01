package bms

// DeratingPoint is one knot of a temperature derating curve.
// Factor is the fraction of nominal current allowed at TempC.
type DeratingPoint struct {
	TempC           float64
	ChargeFactor    float64
	DischargeFactor float64
}

// LimitConfig configures dynamic charge/discharge current limits.
type LimitConfig struct {
	MaxChargeA    float64
	MaxDischargeA float64

	ChargeTempCurve    []DeratingPoint
	DischargeTempCurve []DeratingPoint

	// Voltage-based tapering near the cell limits (V).
	CellHardOverV  float64
	CellHardUnderV float64
	VoltageBandV   float64

	// SOC-based taper bounds: below SOCTaperLow discharge tapers to zero,
	// above SOCTaperHigh charge tapers to zero.
	SOCTaperHigh float64
	SOCTaperLow  float64
}

// LimitResult is the outcome of applying the limits to a request.
type LimitResult struct {
	ChargeLimitA    float64
	DischargeLimitA float64
	AppliedCurrentA float64
	Limited         bool
	Reasons         []string
}

// CurrentLimiter computes charge/discharge envelopes.
type CurrentLimiter struct {
	cfg LimitConfig
}

// NewCurrentLimiter builds a limiter.
func NewCurrentLimiter(cfg LimitConfig) *CurrentLimiter { return &CurrentLimiter{cfg: cfg} }

// Apply clamps requestedCurrent (discharge-positive) to the allowed envelope.
// The envelope is the most restrictive across temperatures, SOC and cell
// voltages. Every binding constraint contributes a human-readable reason.
func (l *CurrentLimiter) Apply(requestedCurrent float64, temps, soc, voltages []float64) LimitResult {
	res := LimitResult{
		ChargeLimitA:    l.cfg.MaxChargeA,
		DischargeLimitA: l.cfg.MaxDischargeA,
	}

	// Temperature derating: worst (hottest) cell, piecewise-linear curves.
	if len(temps) > 0 {
		tWorst := temps[0]
		for _, t := range temps[1:] {
			if t > tWorst {
				tWorst = t
			}
		}
		if f := deratingFactor(l.cfg.ChargeTempCurve, tWorst, true); f < 1 {
			res.ChargeLimitA = l.cfg.MaxChargeA * f
			if f <= 0 {
				res.Reasons = append(res.Reasons, "charge disabled by temperature derating")
			} else {
				res.Reasons = append(res.Reasons, "charge current limited by temperature derating")
			}
		}
		if f := deratingFactor(l.cfg.DischargeTempCurve, tWorst, false); f < 1 {
			res.DischargeLimitA = l.cfg.MaxDischargeA * f
			if f <= 0 {
				res.Reasons = append(res.Reasons, "discharge disabled by temperature derating")
			} else {
				res.Reasons = append(res.Reasons, "discharge current limited by temperature derating")
			}
		}
	}

	// SOC-based taper.
	for _, s := range soc {
		if band := 1 - l.cfg.SOCTaperHigh; band > 0 && s > l.cfg.SOCTaperHigh {
			if f := (1 - s) / band; f < 1 && l.cfg.MaxChargeA*f < res.ChargeLimitA {
				res.ChargeLimitA = l.cfg.MaxChargeA * f
				reason := "charge current limited by high SOC taper"
				if f <= 0 {
					reason = "charge disabled: SOC at high taper bound"
				}
				res.Reasons = append(res.Reasons, reason)
			}
		}
		if band := l.cfg.SOCTaperLow; band > 0 && s < l.cfg.SOCTaperLow {
			if f := s / band; f < 1 && l.cfg.MaxDischargeA*f < res.DischargeLimitA {
				res.DischargeLimitA = l.cfg.MaxDischargeA * f
				reason := "discharge current limited by low SOC taper"
				if f <= 0 {
					reason = "discharge disabled: SOC at low taper bound"
				}
				res.Reasons = append(res.Reasons, reason)
			}
		}
	}

	// Voltage-based taper near hard cell limits.
	for _, v := range voltages {
		if d := l.cfg.CellHardOverV - v; d < l.cfg.VoltageBandV {
			f := clampF(d/l.cfg.VoltageBandV, 0, 1)
			if limit := l.cfg.MaxChargeA * f; limit < res.ChargeLimitA {
				res.ChargeLimitA = limit
				if f <= 0 {
					res.Reasons = append(res.Reasons, "charge disabled: cell at hard over-voltage")
				} else {
					res.Reasons = append(res.Reasons, "charge current limited by cell voltage taper")
				}
			}
		}
		if d := v - l.cfg.CellHardUnderV; d < l.cfg.VoltageBandV {
			f := clampF(d/l.cfg.VoltageBandV, 0, 1)
			if limit := l.cfg.MaxDischargeA * f; limit < res.DischargeLimitA {
				res.DischargeLimitA = limit
				if f <= 0 {
					res.Reasons = append(res.Reasons, "discharge disabled: cell at hard under-voltage")
				} else {
					res.Reasons = append(res.Reasons, "discharge current limited by cell voltage taper")
				}
			}
		}
	}

	// Apply the envelope to the signed request.
	switch {
	case requestedCurrent > res.DischargeLimitA:
		res.AppliedCurrentA = res.DischargeLimitA
		res.Limited = true
	case requestedCurrent < -res.ChargeLimitA:
		res.AppliedCurrentA = -res.ChargeLimitA
		res.Limited = true
	default:
		res.AppliedCurrentA = requestedCurrent
	}
	return res
}

// deratingFactor interpolates a temperature curve and clamps beyond the ends.
func deratingFactor(curve []DeratingPoint, tempC float64, charge bool) float64 {
	if len(curve) == 0 {
		return 1
	}
	f := func(p DeratingPoint) float64 {
		if charge {
			return p.ChargeFactor
		}
		return p.DischargeFactor
	}
	if tempC <= curve[0].TempC {
		return f(curve[0])
	}
	last := len(curve) - 1
	if tempC >= curve[last].TempC {
		return f(curve[last])
	}
	for i := 1; i <= last; i++ {
		if tempC <= curve[i].TempC {
			a, b := curve[i-1], curve[i]
			frac := (tempC - a.TempC) / (b.TempC - a.TempC)
			return f(a) + frac*(f(b)-f(a))
		}
	}
	return 1
}
