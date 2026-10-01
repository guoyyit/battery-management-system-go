package bms

import (
	"sync"
	"testing"
	"time"
)

// TestBalancerTriggersAndCapsCurrent verifies SOC-spread triggering, the
// per-cell current cap and cumulative balanced charge accounting.
func TestBalancerTriggersAndCapsCurrent(t *testing.T) {
	now := time.Unix(0, 0)
	cfg := DefaultConfig()
	bcfg := cfg.Balance
	bcfg.Mode = BalanceBySOC
	bcfg.SOCThreshold = 0.02
	bcfg.MaxBalanceA = 2
	bcfg.MaxConcurrent = 2
	bcfg.ArmCurrentA = 3
	b := NewBalancer(bcfg, 4, nil, nil)

	soc := []float64{0.53, 0.51, 0.49, 0.47}
	meas := Measurement{
		Time:        now,
		CellVoltage: []float64{3.30, 3.29, 3.285, 3.28},
	}
	st := b.Update(time.Second, meas, soc, false)

	active := 0
	for i, on := range st.Active {
		if on {
			active++
			if st.CurrentA[i] <= 0 || st.CurrentA[i] > bcfg.MaxBalanceA+1e-9 {
				t.Fatalf("cell %d bypass current %.3f outside (0,%.1f]",
					i, st.CurrentA[i], bcfg.MaxBalanceA)
			}
			if st.ChargeAh[i] <= 0 {
				t.Fatalf("cell %d balanced charge not accumulated: %v", i, st.ChargeAh)
			}
		}
	}
	if active != 2 {
		t.Fatalf("expected 2 active cells (highest SOC), got %d: %v", active, st.Active)
	}
	if !st.Active[0] || !st.Active[1] {
		t.Fatalf("highest-SOC cells must balance, got %v", st.Active)
	}
}

// TestBalancerVoltageSampleValidWhileBalancing checks that an actively
// balancing cell's reconstructed OCV stays usable: the bypass voltage drop is
// exactly I*R, so feeding a loaded sample back reproduces the true OCV.
func TestBalancerVoltageSampleValidWhileBalancing(t *testing.T) {
	bcfg := DefaultConfig().Balance
	bcfg.Mode = BalanceByVoltage
	bcfg.VoltageThresholdV = 0.01
	bcfg.Hysteresis = 0.0
	b := NewBalancer(bcfg, 3, nil, nil)

	ocvs := []float64{3.35, 3.30, 3.28}
	meas := Measurement{CellVoltage: append([]float64(nil), ocvs...)}
	st := b.Update(time.Second, meas, nil, false)

	for i, on := range st.Active {
		if !on {
			continue
		}
		loaded := ocvs[i] - st.CurrentA[i]*bcfg.BypassResistorOhm
		meas2 := Measurement{CellVoltage: []float64{loaded, ocvs[1], ocvs[2]}}
		st2 := b.Update(time.Second, meas2, nil, false)
		if !st2.Active[i] {
			t.Fatalf("cell %d dropped out despite valid I*R-compensated sample", i)
		}
	}
}

// TestBalancerInhibitedUnderCurrentAndFault verifies switches stay open while
// pack current exceeds the arm threshold or the contactor is open.
func TestBalancerInhibitedUnderCurrentAndFault(t *testing.T) {
	bcfg := DefaultConfig().Balance
	bcfg.Mode = BalanceBySOC
	bcfg.MaxConcurrent = 4
	b := NewBalancer(bcfg, 4, nil, nil)
	soc := []float64{0.9, 0.5, 0.5, 0.1}
	meas := Measurement{CurrentSensed: 50, CellVoltage: []float64{3.4, 3.3, 3.3, 3.1}}

	if st := b.Update(time.Second, meas, soc, false); anyActive(st.Active) {
		t.Fatalf("balancer armed above ArmCurrentA: %v", st.Active)
	}
	meas.CurrentSensed = 0
	if st := b.Update(time.Second, meas, soc, true); anyActive(st.Active) {
		t.Fatalf("balancer armed while contactor open: %v", st.Active)
	}
}

// TestBalancerConcurrentQueries exercises Update concurrent with Status
// queries; it must never race or expose torn state.
func TestBalancerConcurrentQueries(t *testing.T) {
	bcfg := DefaultConfig().Balance
	bcfg.Mode = BalanceBySOC
	b := NewBalancer(bcfg, 4, nil, nil)
	soc := []float64{0.55, 0.52, 0.49, 0.46}
	meas := Measurement{CellVoltage: []float64{3.31, 3.30, 3.29, 3.28}}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					st := b.Status()
					if len(st.Active) != 4 || len(st.ChargeAh) != 4 {
						t.Errorf("torn balance status")
						return
					}
				}
			}
		}()
	}
	for i := 0; i < 500; i++ {
		b.Update(time.Second, meas, soc, i%7 == 0)
	}
	close(stop)
	wg.Wait()
}

func anyActive(a []bool) bool {
	for _, on := range a {
		if on {
			return true
		}
	}
	return false
}
