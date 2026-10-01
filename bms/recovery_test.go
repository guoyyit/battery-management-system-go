package bms

import (
	"testing"
	"time"
)

// TestCrashRecoveryPersistsSOCAndBalancing: run, force a durable snapshot,
// simulate a hard kill by dropping the BMS handle, then New() from the same
// directory. Recovered SOC and balanced charge must match the snapshot.
func TestCrashRecoveryPersistsSOCAndBalancing(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(0, 0)

	cfg := DefaultConfig()
	cfg.StateDir = dir
	cfg.PersistEvery = time.Hour // only explicit persistence in this test
	sys, err := New(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30*60; i++ {
		if _, err := sys.Step(time.Second, Inputs{Current: 20, AmbientTemp: 25}); err != nil {
			t.Fatal(err)
		}
	}
	// Rest to give balancing a window, then snapshot.
	for i := 0; i < 20*60; i++ {
		if _, err := sys.Step(time.Second, Inputs{Current: 0, AmbientTemp: 25}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sys.Persist(); err != nil {
		t.Fatal(err)
	}
	snapSOC := sys.Status().SOC
	snapBal := sys.Status().Balance.ChargeAh

	// "Kill" the process: create a brand-new BMS over the same state dir.
	sys2, err := New(cfg, now.Add(50*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	recSOC := sys2.Status().SOC
	for i := range snapSOC {
		if recSOC[i] != snapSOC[i] {
			t.Fatalf("recovered SOC cell %d = %.8f, want %.8f", i, recSOC[i], snapSOC[i])
		}
	}
	recBal := sys2.Status().Balance.ChargeAh
	for i := range snapBal {
		if recBal[i] != snapBal[i] {
			t.Fatalf("recovered balanced charge cell %d = %.8f, want %.8f",
				i, recBal[i], snapBal[i])
		}
	}
}

// TestCrashRecoveryPreservesLatchAndHistory: a latched short-circuit and its
// history survive a restart, and subsequent history keeps one continuous
// sequence (no duplicate, no missing records).
func TestCrashRecoveryPreservesLatchAndHistory(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(0, 0)
	cfg := DefaultConfig()
	cfg.StateDir = dir
	cfg.PersistEvery = 24 * time.Hour
	cfg.Protection.TripDebounce = time.Second
	sys, err := New(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	for i := 0; i < 3; i++ {
		clock = clock.Add(time.Second)
		if _, err := sys.Step(time.Second, Inputs{Current: -350, AmbientTemp: 25}); err != nil {
			t.Fatal(err)
		}
	}
	if !sys.Status().ContactorOpen {
		t.Fatal("precondition: short circuit latched")
	}
	before := sys.FaultPage(0, 0)
	if len(before) != 1 {
		t.Fatalf("precondition: one history record, got %d", len(before))
	}

	// Crash while latched; restart from the same directory.
	sys2, err := New(cfg, clock)
	if err != nil {
		t.Fatal(err)
	}
	if !sys2.Status().ContactorOpen {
		t.Fatal("latched fault must survive crash")
	}
	if len(sys2.Status().Faults) != 1 || sys2.Status().Faults[0].Type != FaultShortCircuit {
		t.Fatalf("recovered faults mismatch: %+v", sys2.Status().Faults)
	}
	after := sys2.FaultPage(0, 0)
	if len(after) != 1 || after[0] != before[0] {
		t.Fatalf("history duplicated/lost on recovery: before %+v after %+v", before, after)
	}

	// Explicit clear after recovery: sequence numbers continue, no gap/dup.
	clock = clock.Add(2 * time.Second)
	cleared, err := sys2.ClearFaults(clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared) != 1 {
		t.Fatalf("clear after recovery: %v", cleared)
	}
	full := sys2.FaultPage(0, 0)
	if len(full) != 2 || full[0].Seq != 1 || full[1].Seq != 2 {
		t.Fatalf("sequence not continuous after recovery: %+v", full)
	}
	if full[0].Tripped == full[1].Tripped {
		t.Fatalf("expected one trip and one clear record: %+v", full)
	}
}

// TestCrashRecoveryNoStateDir means persistence is optional: a nil store must
// not break stepping or closing.
func TestCrashRecoveryNoStateDir(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StateDir = ""
	sys, err := New(cfg, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := sys.Step(time.Second, Inputs{Current: 10, AmbientTemp: 25}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sys.Close(); err != nil {
		t.Fatal(err)
	}
}
