package bms

// OCVTable is a piecewise-linear open-circuit-voltage vs SOC curve.
// SOC points must be strictly increasing in [0,1]; voltage in volts.
type OCVTable struct {
	SOC     []float64
	Voltage []float64
}

// DefaultOCVTable returns a representative LFP-ish 3.0..3.65 V curve.
func DefaultOCVTable() OCVTable {
	return OCVTable{
		SOC: []float64{
			0.00, 0.02, 0.05, 0.10, 0.20, 0.30, 0.40, 0.50,
			0.60, 0.70, 0.80, 0.90, 0.95, 0.98, 1.00,
		},
		Voltage: []float64{
			3.000, 3.080, 3.150, 3.220, 3.255, 3.270, 3.282, 3.290,
			3.298, 3.310, 3.325, 3.345, 3.370, 3.400, 3.450,
		},
	}
}

// VocAtSOC maps SOC to open-circuit voltage by linear interpolation.
func (t OCVTable) VocAtSOC(soc float64) float64 {
	if len(t.SOC) == 0 {
		return 0
	}
	if soc <= t.SOC[0] {
		return t.Voltage[0]
	}
	last := len(t.SOC) - 1
	if soc >= t.SOC[last] {
		return t.Voltage[last]
	}
	lo, hi := 0, last
	for hi-lo > 1 {
		mid := int(uint(lo+hi) >> 1)
		if t.SOC[mid] <= soc {
			lo = mid
		} else {
			hi = mid
		}
	}
	frac := (soc - t.SOC[lo]) / (t.SOC[hi] - t.SOC[lo])
	return t.Voltage[lo] + frac*(t.Voltage[hi]-t.Voltage[lo])
}

// SOCAtVoc is the inverse mapping, clamped to [0,1].
func (t OCVTable) SOCAtVoc(voc float64) float64 {
	if len(t.Voltage) == 0 {
		return 0
	}
	if voc <= t.Voltage[0] {
		return t.SOC[0]
	}
	last := len(t.Voltage) - 1
	if voc >= t.Voltage[last] {
		return t.SOC[last]
	}
	lo, hi := 0, last
	for hi-lo > 1 {
		mid := int(uint(lo+hi) >> 1)
		if t.Voltage[mid] <= voc {
			lo = mid
		} else {
			hi = mid
		}
	}
	frac := (voc - t.Voltage[lo]) / (t.Voltage[hi] - t.Voltage[lo])
	return t.SOC[lo] + frac*(t.SOC[hi]-t.SOC[lo])
}
