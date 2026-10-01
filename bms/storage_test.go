package bms

import (
	"testing"
	"time"
)

func TestFileStoreSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := s.Load(); err != nil || st != nil {
		t.Fatalf("fresh store load = %v, %v; want nil, nil", st, err)
	}

	want := &PersistedState{
		SavedAt:     time.Unix(123, 0),
		SOC:         []float64{0.4, 0.42},
		BalActive:   []bool{true, false},
		BalChargeAh: []float64{0.01, 0},
		TrippedFaults: []ActiveFault{
			{Type: FaultOvervoltage, Cell: 1, Voltage: 3.7, At: time.Unix(100, 0)},
		},
		FaultRecords: []FaultRecord{
			{Seq: 1, Type: FaultOvervoltage, Tripped: true, At: time.Unix(100, 0)},
		},
		NextFaultSeq: 2,
	}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schema = %d", got.SchemaVersion)
	}
	if len(got.SOC) != 2 || got.SOC[0] != 0.4 || got.BalActive[0] != true {
		t.Fatalf("state mismatch: %+v", got)
	}
	if len(got.TrippedFaults) != 1 || got.TrippedFaults[0].Type != FaultOvervoltage {
		t.Fatalf("latched faults mismatch: %+v", got.TrippedFaults)
	}
	if len(got.FaultRecords) != 1 || got.NextFaultSeq != 2 {
		t.Fatalf("fault log mismatch: %+v", got)
	}
}

func TestFileStoreAtomicReplaceLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	for i := 0; i < 5; i++ {
		if err := s.Save(&PersistedState{SOC: []float64{float64(i) / 10}}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := readDirNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		if name != "state.json" {
			t.Fatalf("unexpected leftover file after atomic save: %s", name)
		}
	}
}
