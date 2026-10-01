package bms

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testConfig(t *testing.T, path string) Config {
	t.Helper()
	cfg := DefaultConfig(4, 2.5)
	cfg.StorePath = path
	cfg.Seed = 1234
	return cfg
}

func runSteps(b *BMS, start time.Time, n int) time.Time {
	now := start
	for i := 0; i < n; i++ {
		now = now.Add(b.cfg.SampleInterval)
		b.Step(now)
	}
	return now
}

func TestFaultTripStopsCurrentAndIsSelfConsistent(t *testing.T) {
	cfg := testConfig(t, filepath.Join(t.TempDir(), "state.json"))
	cfg.OverVoltage = 4.15
	cfg.OverVoltageHysteresis = 0.05
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	b.RequestCurrent(1.0)
	now := time.Unix(9_000_000, 0)
	now = runSteps(b, now, 5)

	// Drive a cell above the over-voltage threshold (OCV at 100% = 4.20).
	b.sim.SetCellSOC(2, 1.0)
	var tripped bool
	for i := 0; i < 40; i++ {
		now = now.Add(cfg.SampleInterval)
		b.Step(now)
		snap := b.Snapshot()
		if snap.FaultLatched {
			tripped = true
			if snap.AppliedCurrent != 0 || snap.CurrentA != 0 {
				t.Fatalf("current must be zero while latched: applied=%.3f measured=%.3f",
					snap.AppliedCurrent, snap.CurrentA)
			}
			if len(snap.ActiveFaults) == 0 {
				t.Fatal("latched snapshot must expose active faults")
			}
			break
		}
	}
	if !tripped {
		t.Fatal("over-voltage fault never tripped")
	}

	// Latch cannot be cleared while the over-voltage condition persists.
	if b.ClearFaults() {
		t.Fatal("clear rejected while fault condition persists")
	}

	// Fault history is present and ordered.
	page := b.FaultHistory(0, 10)
	if page.Total == 0 {
		t.Fatal("fault history empty after trip")
	}
	for i := 1; i < len(page.Records); i++ {
		if page.Records[i].ID <= page.Records[i-1].ID {
			t.Fatal("fault records not ordered by ID")
		}
	}
}

func TestFaultHistoryPaging(t *testing.T) {
	log := NewFaultLog()
	for i := int64(1); i <= 25; i++ {
		log.Append(FaultRecord{ID: i, Type: FaultOverCurrent, Action: FaultTriggered})
	}
	p0 := log.Page(0, 10)
	if len(p0.Records) != 10 || p0.Total != 25 || p0.Records[0].ID != 1 ||
		p0.Records[9].ID != 10 {
		t.Fatalf("page 0 wrong: %+v", p0)
	}
	p2 := log.Page(2, 10)
	if len(p2.Records) != 5 || p2.Records[0].ID != 21 {
		t.Fatalf("page 2 wrong: %+v", p2)
	}
}

func TestConcurrentSamplingAndQueries(t *testing.T) {
	cfg := testConfig(t, filepath.Join(t.TempDir(), "state.json"))
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b.RequestCurrent(1.5)

	start := time.Unix(10_000_000, 0)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			b.Step(start.Add(time.Duration(i) * cfg.SampleInterval))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			s := b.Snapshot()
			if s.FaultLatched && (s.AppliedCurrent != 0 || s.CurrentA != 0) {
				t.Errorf("inconsistent snapshot: latched with current flowing")
				return
			}
			if len(s.Cells) != cfg.CellCount {
				t.Errorf("partial cell snapshot: %d", len(s.Cells))
				return
			}
		}
	}()
	wg.Wait()
}
