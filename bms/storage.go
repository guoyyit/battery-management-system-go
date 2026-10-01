package bms

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// PersistedState is the crash-recovery snapshot.
type PersistedState struct {
	SchemaVersion int       `json:"schema_version"`
	SavedAt       time.Time `json:"saved_at"`

	SOC []float64 `json:"soc"`

	BalActive   []bool    `json:"bal_active"`
	BalChargeAh []float64 `json:"bal_charge_ah"`

	TrippedFaults []ActiveFault `json:"tripped_faults"`

	FaultRecords []FaultRecord `json:"fault_records"`
	NextFaultSeq int64         `json:"next_fault_seq"`
}

// CurrentSchemaVersion is bumped on incompatible on-disk layout changes.
const CurrentSchemaVersion = 1

// Store persists BMS state durably.
type Store interface {
	Load() (*PersistedState, error)
	Save(state *PersistedState) error
}

// FileStore atomically writes one JSON state file.
type FileStore struct {
	path string
}

// NewFileStore creates/opens state.json inside dir.
func NewFileStore(dir string) (*FileStore, error) {
	if dir == "" {
		return nil, errors.New("storage: empty state directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{path: filepath.Join(dir, "state.json")}, nil
}

// Load reads the latest snapshot; a missing file means a fresh system.
func (s *FileStore) Load() (*PersistedState, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var st PersistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Save atomically replaces the snapshot: write temp, fsync, rename, fsync dir.
func (s *FileStore) Save(state *PersistedState) error {
	state.SchemaVersion = CurrentSchemaVersion
	if state.SavedAt.IsZero() {
		state.SavedAt = time.Now()
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		cleanup()
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
