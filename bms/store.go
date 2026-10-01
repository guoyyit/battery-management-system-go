package bms

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Store persists BMS state durably across crashes as a single JSON document.
type Store struct {
	path string
}

// PersistedState is the on-disk state envelope.
type PersistedState struct {
	Version      int           `json:"version"`
	PackSOC      float64       `json:"pack_soc"`
	CumulativeAh []float64     `json:"cumulative_ah"`
	CellSOC      []float64     `json:"cell_soc"`
	SimSOC       []float64     `json:"sim_soc"`
	SimTempC     float64       `json:"sim_temp_c"`
	BalancedAh   []float64     `json:"balanced_ah"`
	Switching    []bool        `json:"switching"`
	FaultLatched bool          `json:"fault_latched"`
	ActiveFaults []FaultType   `json:"active_faults"`
	Faults       []FaultRecord `json:"faults"`
	NextFaultID  int64         `json:"next_fault_id"`
}

const persistedStateVersion = 1

// NewStore constructs a store backed by the given file path.
func NewStore(path string) *Store { return &Store{path: path} }

// Save atomically writes state: encode to a temp file, fsync it, then rename
// over the real file. A crash at any instant leaves either the old complete
// file or the new complete file, never a torn document.
func (s *Store) Save(st PersistedState) error {
	if s.path == "" {
		return errors.New("empty store path")
	}
	st.Version = persistedStateVersion

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bms-state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path)
}

// Load reads state. The boolean is false (with no error) when no state file
// exists, meaning a cold start.
func (s *Store) Load() (PersistedState, bool, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return PersistedState{}, false, nil
	}
	if err != nil {
		return PersistedState{}, false, err
	}
	var st PersistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return PersistedState{}, false, err
	}
	if st.Version != persistedStateVersion {
		return PersistedState{}, false, errors.New("unsupported persisted state version")
	}
	return st, true, nil
}
