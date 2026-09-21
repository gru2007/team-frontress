// Package players keeps the small amount of history a restriction needs.
//
// Matchmaking itself is stateless across a restart, deliberately. Restrictions
// are not: "five matches before ranked" and "no queue for half an hour after
// you abandon one" are claims about the past, and a queue that forgets them on
// every deploy is not enforcing anything.
//
// So this is the same shape as the war's event log and for the same reason:
// state is a fold over an append-only JSONL file. Delete the file to forget
// everyone. Leave File empty and the records live in memory, which is fine for
// a LAN and honest about what it loses.
package players

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// Record is what the coordinator remembers about one player.
type Record struct {
	SteamID wire.SteamID `json:"steam_id"`
	Name    string       `json:"name,omitempty"`
	// Matches is finished matches the player was actually present for.
	Matches int `json:"matches"`
	Wins    int `json:"wins"`
	Losses  int `json:"losses"`
	// Abandons is matches the player was assigned to and never showed up for,
	// or left before the end of.
	Abandons    int       `json:"abandons"`
	LastAbandon time.Time `json:"last_abandon,omitempty"`
	LastMatch   time.Time `json:"last_match,omitempty"`
}

// event is one line of the log. A record is the fold of every event about a
// SteamID, so a new field here must be additive: old lines still have to
// replay.
type event struct {
	Type    string       `json:"t"`
	At      time.Time    `json:"at"`
	SteamID wire.SteamID `json:"id"`
	Name    string       `json:"name,omitempty"`
	MatchID string       `json:"match_id,omitempty"`
	// Result is "win", "loss" or "draw" for a played match.
	Result string `json:"result,omitempty"`
}

const (
	evPlayed   = "played"
	evAbandon  = "abandon"
	resultWin  = "win"
	resultLoss = "loss"
)

// Store is the record set. The zero value is not usable; call New.
type Store struct {
	mu      sync.Mutex
	records map[wire.SteamID]*Record
	f       *os.File
	now     func() time.Time
}

// New opens a store. An empty path keeps everything in memory.
func New(path string) (*Store, error) {
	s := &Store{records: map[wire.SteamID]*Record{}, now: time.Now}
	if path == "" {
		return s, nil
	}
	if err := s.replay(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("players: %w", err)
	}
	s.f = f
	return s, nil
}

// UseClock replaces the clock events are stamped with. Tests step time with
// it; nothing in production calls it.
func (s *Store) UseClock(f func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = f
}

// Close flushes the log.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

func (s *Store) replay(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("players: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var ev event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return fmt.Errorf("players: %s:%d: %w", path, line, err)
		}
		s.apply(ev)
	}
	return sc.Err()
}

// apply folds one event into the records. Callers hold no lock during replay
// (nothing else exists yet); append takes it.
func (s *Store) apply(ev event) {
	if ev.SteamID == "" {
		return
	}
	r, ok := s.records[ev.SteamID]
	if !ok {
		r = &Record{SteamID: ev.SteamID}
		s.records[ev.SteamID] = r
	}
	if ev.Name != "" {
		r.Name = ev.Name
	}
	switch ev.Type {
	case evPlayed:
		r.Matches++
		r.LastMatch = ev.At
		switch ev.Result {
		case resultWin:
			r.Wins++
		case resultLoss:
			r.Losses++
		}
	case evAbandon:
		r.Abandons++
		r.LastAbandon = ev.At
	}
}

func (s *Store) append(ev event) {
	s.apply(ev)
	if s.f == nil {
		return
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	// A failed write loses one event, not the process: the records in memory
	// are already correct and the next restart is the only thing that suffers.
	_, _ = s.f.Write(raw)
}

// Played records that a player finished a match.
func (s *Store) Played(p wire.AssignedPlayer, matchID, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.append(event{Type: evPlayed, At: s.now(), SteamID: p.SteamID, Name: p.Name, MatchID: matchID, Result: result})
}

// Abandoned records that a player was in a match's roster and was not there
// when it ended.
func (s *Store) Abandoned(p wire.AssignedPlayer, matchID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.append(event{Type: evAbandon, At: s.now(), SteamID: p.SteamID, Name: p.Name, MatchID: matchID})
}

// Get returns a copy of a player's record. An unknown player is the zero
// record, which is what a first-time player is.
func (s *Store) Get(id wire.SteamID) Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[id]; ok {
		return *r
	}
	return Record{SteamID: id}
}

// Known is how many players the store has ever seen.
func (s *Store) Known() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}
