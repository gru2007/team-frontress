// Package reports keeps private moderation reports separately from public match history.
package reports

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrCooldown = errors.New("report rate limit")

type Report struct {
	Reporter string    `json:"reporter"`
	Target   string    `json:"target"`
	Reason   int32     `json:"reason"`
	MatchID  string    `json:"match_id,omitempty"`
	At       time.Time `json:"at"`
}

// Store belongs to the GC loop. A successful Add means the report is on disk.
type Store struct {
	dir    string
	latest map[string]time.Time
	recent map[string][]time.Time
}

func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, latest: map[string]time.Time{}, recent: map[string][]time.Time{}}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var r Report
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("report %s: %w", entry.Name(), err)
		}
		s.remember(r)
	}
	return s, nil
}

func (s *Store) remember(r Report) {
	key := r.Reporter + "/" + r.Target
	if r.At.After(s.latest[key]) {
		s.latest[key] = r.At
	}
	s.recent[r.Reporter] = append(s.recent[r.Reporter], r.At)
}

func (s *Store) Add(r Report) error {
	if at, ok := s.latest[r.Reporter+"/"+r.Target]; ok && r.At.Sub(at) < 5*time.Minute {
		return ErrCooldown
	}
	recent := s.recent[r.Reporter][:0]
	for _, at := range s.recent[r.Reporter] {
		if r.At.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	s.recent[r.Reporter] = recent
	if len(recent) >= 5 {
		return ErrCooldown
	}
	if s.dir != "" {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(s.dir, ".report-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		_, writeErr := f.Write(raw)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
		if err := os.Rename(f.Name(), f.Name()+".json"); err != nil {
			return err
		}
	}
	s.remember(r)
	return nil
}
