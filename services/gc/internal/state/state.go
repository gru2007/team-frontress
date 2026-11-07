// Package state persists the matches the GC is running, so a restarted GC
// picks them up where it left them.
//
// Parties and queues are deliberately not here: they are rebuilt from the
// clients themselves, which reconnect within seconds. A running match is the
// one thing a client cannot rebuild -- it is twelve people and a server that
// all need to keep agreeing about who is playing.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Member is one seat.
type Member struct {
	SteamID      uint64    `json:"steam_id,string"`
	Name         string    `json:"name,omitempty"`
	Team         int32     `json:"team"`
	PartyID      uint64    `json:"party_id,string,omitempty"`
	ConnectState int32     `json:"connect_state"`
	Standby      bool      `json:"standby,omitempty"`
	JoinedAt     time.Time `json:"joined_at"`
}

// War is a match's part in the campaign.
type War struct {
	Campaign     string `json:"campaign,omitempty"`
	UniformSwap  bool   `json:"uniform_swap"`
	FrontID      string `json:"front_id"`
	NodeID       string `json:"node_id"`
	NodeName     string `json:"node_name"`
	StageIndex   int    `json:"stage_index"`
	StageCount   int    `json:"stage_count"`
	StageKind    string `json:"stage_kind"`
	Attacker     string `json:"attacker"`
	AttackerTeam int32  `json:"attacker_team"`
	// Conditions the battle is fought under (war.Conditions).
	Conditions []string `json:"conditions,omitempty"`
	// Order: the front was the war's order when the battle formed; everyone
	// in it earns the order's bonus.
	Order bool `json:"order,omitempty"`
}

// Lobby is a match.
type Lobby struct {
	ID              uint64    `json:"id,string"`
	MatchID         uint64    `json:"match_id,string"`
	Group           int32     `json:"group"`
	Map             string    `json:"map"`
	MaxPlayers      int       `json:"max_players"`
	State           string    `json:"state"`
	Members         []Member  `json:"members"`
	ServerID        uint64    `json:"server_id,string,omitempty"`
	Connect         string    `json:"connect,omitempty"`
	STV             string    `json:"stv,omitempty"`
	FormedAt        time.Time `json:"formed_at"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	MMVersion       uint32    `json:"mm_version"`
	PreviousMatchID uint64    `json:"previous_match_id,string,omitempty"`
	NextMaps        []uint32  `json:"next_maps,omitempty"`
	War             *War      `json:"war,omitempty"`
}

// Store reads and writes the snapshot.
type Store struct {
	mu   sync.Mutex
	path string
	mem  []Lobby
}

// Open uses a file; an empty path keeps the snapshot in memory.
func Open(path string) *Store { return &Store{path: path} }

// Memory is a store that forgets on restart.
func Memory() *Store { return &Store{} }

// Load reads the snapshot. A missing file is an empty one.
func (s *Store) Load() ([]Lobby, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return append([]Lobby(nil), s.mem...), nil
	}
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	var out []Lobby
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("state: %s: %w", s.path, err)
	}
	return out, nil
}

// Save replaces the snapshot. The file is written beside its final name and
// renamed over it, so a crash mid-write leaves the previous snapshot whole.
func (s *Store) Save(lobbies []Lobby) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		s.mem = append([]Lobby(nil), lobbies...)
		return nil
	}
	raw, err := json.MarshalIndent(lobbies, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".matches-*")
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("state: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("state: %w", err)
	}
	return nil
}
