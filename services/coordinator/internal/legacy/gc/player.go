package gc

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/greyline-frontress/coordinator/internal/hostelect"
)

// Player is the coordinator's durable record for one account. It survives
// restarts so that host reputation and abandon penalties actually mean
// something — a player who abandons and reconnects must not come back clean.
type Player struct {
	SteamID     uint64            `json:"steam_id"`
	History     hostelect.History `json:"history"`
	BannedUntil time.Time         `json:"banned_until,omitzero"`
	BanReason   string            `json:"ban_reason,omitempty"`
	Matches     int               `json:"matches"`
	Abandons    int               `json:"abandons"`
	LastSeen    time.Time         `json:"last_seen,omitzero"`
	LastPop     uint32            `json:"last_pop,omitempty"`
}

// Banned reports whether the player is currently serving a matchmaking ban.
func (p *Player) Banned(now time.Time) bool {
	return !p.BannedUntil.IsZero() && now.Before(p.BannedUntil)
}

// PlayerStore keeps player records in memory and persists them as JSON.
//
// It is separate from the world file: the war state is gameplay content an
// operator may want to hand-edit or reset, while these records are the
// coordinator's own bookkeeping and should not be reset with it.
type PlayerStore struct {
	mu      sync.RWMutex
	path    string
	players map[uint64]*Player
	dirty   bool
}

type playerFile struct {
	Players []*Player `json:"players"`
}

// LoadPlayerStore reads the store, treating a missing file as an empty store —
// a first run is normal, unlike a missing world file.
func LoadPlayerStore(path string) (*PlayerStore, error) {
	s := &PlayerStore{path: path, players: make(map[uint64]*Player)}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read player store: %w", err)
	}
	var pf playerFile
	if err := json.Unmarshal(raw, &pf); err != nil {
		return nil, fmt.Errorf("parse player store: %w", err)
	}
	for _, p := range pf.Players {
		if p.SteamID != 0 {
			s.players[p.SteamID] = p
		}
	}
	return s, nil
}

// Get returns the record for a SteamID, creating it on first sight.
func (s *PlayerStore) Get(steamID uint64) *Player {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.players[steamID]
	if !ok {
		p = &Player{SteamID: steamID}
		s.players[steamID] = p
		s.dirty = true
	}
	return p
}

// Update mutates a record under the store lock and marks it for persistence.
func (s *PlayerStore) Update(steamID uint64, fn func(*Player)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.players[steamID]
	if !ok {
		p = &Player{SteamID: steamID}
		s.players[steamID] = p
	}
	fn(p)
	s.dirty = true
}

// Ban records a temporary matchmaking ban, extending any ban already running
// rather than replacing it — two offences should not be cheaper than one.
func (s *PlayerStore) Ban(steamID uint64, d time.Duration, reason string) time.Time {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.players[steamID]
	if !ok {
		p = &Player{SteamID: steamID}
		s.players[steamID] = p
	}
	base := now
	if p.BannedUntil.After(now) {
		base = p.BannedUntil
	}
	p.BannedUntil = base.Add(d)
	p.BanReason = reason
	s.dirty = true
	return p.BannedUntil
}

// Count reports how many records exist, for the admin endpoint.
func (s *PlayerStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.players)
}

// Save writes the store atomically if anything changed.
func (s *PlayerStore) Save() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	pf := playerFile{}
	for _, p := range s.players {
		pf.Players = append(pf.Players, p)
	}
	s.dirty = false
	s.mu.Unlock()

	raw, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
