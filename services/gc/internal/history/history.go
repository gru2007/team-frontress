// Package history keeps the public match archive independently of live lobbies.
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Member struct {
	SteamID string `json:"steam_id"`
	Name    string `json:"name"`
	Team    string `json:"team"`
	Left    bool   `json:"left"`
}

type PlayerResult struct {
	SteamID string `json:"steam_id"`
	Team    uint32 `json:"team"`
	Score   uint32 `json:"score"`
	Kills   uint32 `json:"kills"`
	Deaths  uint32 `json:"deaths"`
	Damage  uint32 `json:"damage"`
	Healing uint32 `json:"healing"`
}

type Result struct {
	WinningTeam uint32         `json:"winning_team"`
	RedScore    uint32         `json:"red_score"`
	BlueScore   uint32         `json:"blue_score"`
	Duration    uint32         `json:"duration"`
	Players     []PlayerResult `json:"players"`
}

// Match contains no session tokens, server keys or private connection addresses.
// All IDs are strings so JavaScript never rounds a SteamID or match ID.
type Match struct {
	MatchID     string     `json:"match_id"`
	Group       int32      `json:"match_group"`
	GroupName   string     `json:"group_name"`
	Map         string     `json:"map"`
	State       string     `json:"state"`
	Region      string     `json:"region"`
	Campaign    string     `json:"campaign,omitempty"`
	UniformSwap bool       `json:"uniform_swap"`
	FormedAt    time.Time  `json:"formed_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	Members     []Member   `json:"members"`
	Result      *Result    `json:"result,omitempty"`
}

type Page struct {
	Matches []Match `json:"matches"`
	Total   int     `json:"total"`
	Offset  int     `json:"offset"`
	Limit   int     `json:"limit"`
}

// Store has one writer (the GC loop). Readers get detached copies. Each match
// is an atomic file replacement; ending a lobby does not remove its archive.
type Store struct {
	mu      sync.RWMutex
	dir     string
	matches map[string]Match
}

func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, matches: map[string]Match{}}
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
		var m Match
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("history %s: %w", entry.Name(), err)
		}
		if !validID(m.MatchID) || entry.Name() != m.MatchID+".json" {
			return nil, fmt.Errorf("history: invalid match file %q", entry.Name())
		}
		s.matches[m.MatchID] = m
	}
	return s, nil
}

func validID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}

func clone(m Match) Match {
	m.Members = append([]Member{}, m.Members...)
	if m.Result != nil {
		r := *m.Result
		r.Players = append([]PlayerResult{}, r.Players...)
		m.Result = &r
	}
	if m.StartedAt != nil {
		at := *m.StartedAt
		m.StartedAt = &at
	}
	if m.EndedAt != nil {
		at := *m.EndedAt
		m.EndedAt = &at
	}
	return m
}

func (s *Store) Put(m Match) error {
	if !validID(m.MatchID) {
		return fmt.Errorf("history: invalid match ID")
	}
	if s.dir != "" {
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(s.dir, ".match-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		_, err = f.Write(raw)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(f.Name(), filepath.Join(s.dir, m.MatchID+".json")); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.matches[m.MatchID] = clone(m)
	s.mu.Unlock()
	return nil
}

func (s *Store) Get(id string) (Match, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.matches[id]
	return clone(m), ok
}

func (s *Store) List(steamID, state string, offset, limit int) Page {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if offset < 0 {
		offset = 0
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	matches := make([]Match, 0)
	for _, m := range s.matches {
		if state != "" && m.State != state {
			continue
		}
		if steamID != "" {
			found := false
			for _, member := range m.Members {
				if member.SteamID == steamID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		matches = append(matches, m)
	}
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].FormedAt.Equal(matches[j].FormedAt) {
			return matches[i].FormedAt.After(matches[j].FormedAt)
		}
		return matches[i].MatchID > matches[j].MatchID
	})
	p := Page{Matches: []Match{}, Total: len(matches), Offset: offset, Limit: limit}
	if offset >= len(matches) {
		return p
	}
	end := offset + limit
	if end > len(matches) {
		end = len(matches)
	}
	for _, m := range matches[offset:end] {
		p.Matches = append(p.Matches, clone(m))
	}
	return p
}

// Reconcile closes archive records whose live snapshot was lost in a crash.
func (s *Store) Reconcile(active map[string]bool, now time.Time) error {
	s.mu.RLock()
	var interrupted []Match
	for _, m := range s.matches {
		if (m.State == "setup" || m.State == "run") && !active[m.MatchID] {
			interrupted = append(interrupted, clone(m))
		}
	}
	s.mu.RUnlock()
	for _, m := range interrupted {
		m.State, m.Reason, m.EndedAt = "ended", "live state unavailable after coordinator restart", &now
		if err := s.Put(m); err != nil {
			return err
		}
	}
	return nil
}
