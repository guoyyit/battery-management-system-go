package bms

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCrashRecoveryPreservesState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bms_state.json")
	cfg := testConfig(t, path)

	// Phase 1: run with discharge current; balancing and SOC move.
	b1, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b1.RequestCurrent(-1.0)
	now := time.Unix(11_000_000, 0)
	for i := 0; i < 200; i++ {
		now = now.Add(cfg.SampleInterval)
		b1.Step(now)
	}

	snap1 := b1.Snapshot()
	if snap1.PackSOC >= 0.5 {
		t.Fatalf("expected SOC to drop, got %.3f", snap1.PackSOC)
	}

	// Simulate an abrupt kill: no Stop(), but periodic saves already ran.
	soc1 := snap1.PackSOC

	// Phase 2: restart from the same path.
	b2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	snap2 := b2.Snapshot()
	if absF(snap2.PackSOC-soc1) > 0.02 {
		t.Fatalf("SOC after restart = %.3f, want ~%.3f", snap2.PackSOC, soc1)
	}

	// Continue running; estimator keeps working from restored state.
	b2.RequestCurrent(-0.5)
	for i := 0; i < 100; i++ {
		now = now.Add(cfg.SampleInterval)
		b2.Step(now)
	}
	snap3 := b2.Snapshot()
	if snap3.PackSOC >= snap2.PackSOC {
		t.Fatalf("SOC should keep decreasing after recovery: %.3f -> %.3f",
			snap2.PackSOC, snap3.PackSOC)
	}
}

func TestCrashRecoveryPreservesLatchAndFaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bms_state.json")
	cfg := testConfig(t, path)
	cfg.OverVoltage = 4.10
	cfg.OverVoltageHysteresis = 0.30

	b1, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(12_000_000, 0)
	for i := 0; i < 30; i++ {
		now = now.Add(cfg.SampleInterval)
		b1.Step(now)
	}
	b1.sim.SetCellSOC(3, 1.0)
	for i := 0; i < 40; i++ {
		now = now.Add(cfg.SampleInterval)
		b1.Step(now)
		if b1.Snapshot().FaultLatched {
			break
		}
	}
	if !b1.Snapshot().FaultLatched {
		t.Fatal("expected latch before crash")
	}
	faultsBefore := b1.FaultHistory(0, 100)

	// Crash and restart: latch and fault history must survive exactly.
	b2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !b2.Snapshot().FaultLatched {
		t.Fatal("latch must survive a crash")
	}
	faultsAfter := b2.FaultHistory(0, 100)
	if faultsAfter.Total != faultsBefore.Total {
		t.Fatalf("fault count changed across crash: %d -> %d",
			faultsBefore.Total, faultsAfter.Total)
	}
	for i := range faultsBefore.Records {
		a, c := faultsBefore.Records[i], faultsAfter.Records[i]
		if a.ID != c.ID || a.Type != c.Type || a.Action != c.Action {
			t.Fatalf("fault %d duplicated/dropped across crash", i)
		}
	}

	// Keep stepping while latched: no new duplicate trip records appear.
	for i := 0; i < 20; i++ {
		now = now.Add(cfg.SampleInterval)
		b2.Step(now)
	}
	if got := b2.FaultHistory(0, 100).Total; got != faultsBefore.Total {
		t.Fatalf("latched stepping created/duplicated faults: %d -> %d",
			faultsBefore.Total, got)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st := PersistedState{
		Version:      persistedStateVersion,
		PackSOC:      0.42,
		CellSOC:      []float64{0.4, 0.44},
		BalancedAh:   []float64{0.01, 0},
		Switching:    []bool{true, false},
		FaultLatched: true,
		ActiveFaults: []FaultType{FaultOverVoltage},
		NextFaultID:  9,
	}
	s := NewStore(path)
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load()
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.PackSOC != 0.42 || !got.FaultLatched || got.NextFaultID != 9 {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	if _, ok, err := NewStore(filepath.Join(t.TempDir(), "missing.json")).Load(); err != nil || ok {
		t.Fatalf("missing file should be cold start: ok=%v err=%v", ok, err)
	}
}
