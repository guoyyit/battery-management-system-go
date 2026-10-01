package bms

import (
	"sync"
	"testing"
	"time"
)

func fastPersistCfg(t *testing.T) *Config {
	cfg := DefaultConfig()
	cfg.StateDir = t.TempDir()
	cfg.PersistEvery = time.Second
	cfg.Protection.TripDebounce = 2 * time.Second
	return cfg
}

// TestBMSOpensContactorAndStopsMetering: a sustained short-circuit latches,
// the contactor opens, applied current becomes zero and SOC stops moving even
// though large current keeps being requested.
func TestBMSOpensContactorAndStopsMetering(t *testing.T) {
	now := time.Unix(0, 0)
	cfg := fastPersistCfg(t)
	sys, err := New(cfg, now)
	if err != nil {
		t.Fatal(err)
	}

	var preSOC float64
	dt := time.Second
	for i := 0; i < 5; i++ {
		st, err := sys.Step(dt, Inputs{Current: -350, AmbientTemp: 25})
		if err != nil {
			t.Fatal(err)
		}
		preSOC = st.SOC[0]
	}
	st := sys.Status()
	if !st.ContactorOpen || len(st.Faults) == 0 {
		t.Fatalf("short circuit should latch: %+v", st.Faults)
	}
	if st.Limit.AppliedCurrentA != 0 {
		t.Fatalf("applied current must be zero when open: %v", st.Limit.AppliedCurrentA)
	}

	for i := 0; i < 30; i++ {
		st, err = sys.Step(dt, Inputs{Current: -350, AmbientTemp: 25})
		if err != nil {
			t.Fatal(err)
		}
		if !st.ContactorOpen || st.Limit.AppliedCurrentA != 0 {
			t.Fatalf("contactor must stay open with zero current at tick %d", i)
		}
	}
	postSOC := sys.Status().SOC[0]
	if postSOC != preSOC {
		t.Fatalf("SOC moved while latched: before %.6f after %.6f", preSOC, postSOC)
	}

	history := sys.FaultPage(0, 0)
	if len(history) != 1 || !history[0].Tripped || history[0].Type != FaultShortCircuit {
		t.Fatalf("fault history mismatch: %+v", history)
	}
}

// TestBMSClearFaultIsExplicit verifies that removing the cause does not
// auto-clear; only ClearFaults (with hysteresis satisfied) unlatches.
func TestBMSClearFaultIsExplicit(t *testing.T) {
	now := time.Unix(0, 0)
	cfg := fastPersistCfg(t)
	sys, err := New(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	dt := time.Second
	for i := 0; i < 4; i++ {
		if _, err := sys.Step(dt, Inputs{Current: -350, AmbientTemp: 25}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := sys.Step(dt, Inputs{Current: 0, AmbientTemp: 25}); err != nil {
			t.Fatal(err)
		}
	}
	if !sys.Status().ContactorOpen {
		t.Fatal("fault must stay latched after cause disappears")
	}
	cleared, err := sys.ClearFaults(now.Add(7 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared) != 1 || cleared[0] != FaultShortCircuit {
		t.Fatalf("explicit clear should unlatch: %v", cleared)
	}
	if sys.Status().ContactorOpen {
		t.Fatal("contactor should close after clear")
	}
	history := sys.FaultPage(0, 0)
	if len(history) != 2 || history[1].Tripped {
		t.Fatalf("expected trip then clear records, got %+v", history)
	}
}

// TestBMSConcurrentConsistency hammers Step from one goroutine and reads
// Status / FaultPage from others; every observed snapshot must be self
// consistent (contact open implies zero applied current).
// TestBMSFaultForcesBalancingOff first creates a resting window with active
// balancing, then injects a short circuit: on the latched tick and after,
// every bypass switch must be open.
func TestBMSFaultForcesBalancingOff(t *testing.T) {
	now := time.Unix(0, 0)
	cfg := fastPersistCfg(t)
	cfg.Protection.TripDebounce = time.Second
	cfg.Balance.Mode = BalanceBySOC
	cfg.Balance.SOCThreshold = 0.01
	cfg.Balance.Hysteresis = 0.0
	cfg.Balance.MinSOC = 0
	sys, err := New(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	sawBalancing := false
	clock := now
	for i := 0; i < 120; i++ {
		clock = clock.Add(time.Second)
		st, err := sys.Step(time.Second, Inputs{Current: 0, AmbientTemp: 25})
		if err != nil {
			t.Fatal(err)
		}
		for _, on := range st.Balance.Active {
			if on {
				sawBalancing = true
			}
		}
	}
	if !sawBalancing {
		t.Fatal("precondition: rest window should activate balancing")
	}
	for i := 0; i < 5; i++ {
		clock = clock.Add(time.Second)
		st, err := sys.Step(time.Second, Inputs{Current: -350, AmbientTemp: 25})
		if err != nil {
			t.Fatal(err)
		}
		if !st.ContactorOpen {
			continue
		}
		for j, on := range st.Balance.Active {
			if on || st.Balance.CurrentA[j] != 0 {
				t.Fatalf("bypass cell %d active/currenting while fault latched: %+v",
					j, st.Balance)
			}
		}
	}
}

// TestBMSConcurrentConsistency hammers Step from one goroutine and reads
// Status / FaultPage from others; every observed snapshot must be self
// consistent (contact open implies zero applied current).
func TestBMSConcurrentConsistency(t *testing.T) {
	now := time.Unix(0, 0)
	cfg := fastPersistCfg(t)
	sys, err := New(cfg, now)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		clock := now
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			clock = clock.Add(time.Second)
			cur := 30.0
			if i%20 < 5 {
				cur = -30
			}
			if _, err := sys.Step(time.Second, Inputs{Current: cur, AmbientTemp: 25}); err != nil {
				t.Errorf("step: %v", err)
				return
			}
		}
	}()

	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				st := sys.Status()
				if len(st.SOC) != cfg.Model.SeriesCount || len(st.Balance.Active) != cfg.Model.SeriesCount {
					t.Errorf("torn snapshot: %+v", st)
					return
				}
				if st.ContactorOpen && st.Limit.AppliedCurrentA != 0 {
					t.Errorf("inconsistent: open contactor with applied current %v",
						st.Limit.AppliedCurrentA)
					return
				}
				_ = sys.FaultPage(0, 10)
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}
