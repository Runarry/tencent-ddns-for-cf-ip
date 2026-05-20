package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	LastSync  *SyncSnapshot `json:"last_sync,omitempty"`
	Records   []Record      `json:"records"`
	LastError string        `json:"last_error,omitempty"`
	NextRunAt *time.Time    `json:"next_run_at,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type SyncSnapshot struct {
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Success   bool      `json:"success"`
	Message   string    `json:"message,omitempty"`
	Created   int       `json:"created"`
	Updated   int       `json:"updated"`
	Deleted   int       `json:"deleted"`
	Kept      int       `json:"kept"`
}

type Record struct {
	RecordID      uint64    `json:"record_id"`
	Name          string    `json:"name"`
	FQDN          string    `json:"fqdn"`
	Type          string    `json:"type"`
	Value         string    `json:"value"`
	NodeID        string    `json:"nodeid"`
	LatencyMS     int64     `json:"latency_ms"`
	SpeedBPS      int64     `json:"speed_bps"`
	DownloadBytes int64     `json:"download_bytes"`
	DownloadMS    int64     `json:"download_ms"`
	TTFBMS        int64     `json:"ttfb_ms"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Store struct {
	path string
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func Empty() State {
	return State{Records: []Record{}}
}

func (s *Store) Load() (State, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Empty(), nil
	}
	if err != nil {
		return State{}, err
	}
	if len(data) == 0 {
		return Empty(), nil
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, err
	}
	if st.Records == nil {
		st.Records = []Record{}
	}
	return st, nil
}

func (s *Store) Save(st State) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	st.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(s.path)
		return os.Rename(tmp, s.path)
	}
	return nil
}
