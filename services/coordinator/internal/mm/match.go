package mm

import (
	"time"

	"github.com/greyline-frontress/coordinator/internal/war"
)

// MatchState is a battle's lifecycle. It exists on the coordinator only; the
// game server has its own idea of what it is doing and reports it separately.
type MatchState string

const (
	// MatchForming has a roster but no server yet.
	MatchForming MatchState = "forming"
	// MatchAwaitingHost has a roster and an elected player-host, but that
	// host has not yet registered its machine into the pool. Nobody has been
	// sent anywhere; the offer can still time out and fall back to the queue.
	MatchAwaitingHost MatchState = "awaiting_host"
	// MatchBooting has been handed to a server that is loading the map.
	MatchBooting MatchState = "booting"
	// MatchReady has a server up; players have been sent to it.
	MatchReady MatchState = "ready"
	// MatchLive is being played.
	MatchLive MatchState = "live"
	// MatchFinished produced a result.
	MatchFinished MatchState = "finished"
	// MatchAborted ended without one and never touches the war.
	MatchAborted MatchState = "aborted"
)

// Slot is one player's place in a battle.
type Slot struct {
	SteamID uint64   `json:"steam_id"`
	Name    string   `json:"name,omitempty"`
	Side    war.Side `json:"side"`
	Team    war.Side `json:"team"`
	// Contract marks a player fighting for the side that is not their
	// allegiance, for this battle only.
	Contract  bool `json:"contract,omitempty"`
	Connected bool `json:"connected"`
}

// Match is one battle instance, from a formed roster to a recorded result.
type Match struct {
	ID       string         `json:"id"`
	FrontID  string         `json:"front_id"`
	Plan     war.BattlePlan `json:"plan"`
	Slots    []*Slot        `json:"slots"`
	ServerID string         `json:"server_id,omitempty"`
	Connect  string         `json:"connect,omitempty"`
	Password string         `json:"-"`
	State    MatchState     `json:"state"`

	// HostSteamID is set when this battle's server is a player's own machine,
	// elected rather than reserved from the dedicated pool. Zero means an
	// ordinary dedicated server is hosting it.
	HostSteamID uint64 `json:"host_steam_id,omitempty"`

	CreatedAt  time.Time `json:"created_at"`
	StateSince time.Time `json:"state_since"`
	// Result is set once, when the result is actually recorded — for a
	// dedicated server that is the moment it is reported; for a P2P host it is
	// the moment the roster has corroborated it. PendingResult holds a P2P
	// host's report in the meantime, and confirms tracks who on the roster has
	// agreed with it.
	Result        *war.BattleResult `json:"result,omitempty"`
	PendingResult *war.BattleResult `json:"-"`
	confirms      map[uint64]bool
	RedScore      uint32 `json:"red_score"`
	BluScore      uint32 `json:"blu_score"`
}

func (m *Match) setState(s MatchState, now time.Time) {
	m.State = s
	m.StateSince = now
}

func (m *Match) over() bool {
	return m.State == MatchFinished || m.State == MatchAborted
}

// live reports whether players are expected to be on the server.
func (m *Match) live() bool {
	return m.State == MatchReady || m.State == MatchLive
}

func (m *Match) slot(steamID uint64) *Slot {
	for _, s := range m.Slots {
		if s.SteamID == steamID {
			return s
		}
	}
	return nil
}

func (m *Match) sideCounts() (red, blu int) {
	for _, s := range m.Slots {
		switch s.Side {
		case war.SideRed:
			red++
		case war.SideBlu:
			blu++
		}
	}
	return red, blu
}

// LiveBattle is one running battle as the war map shows it: the thing that
// turns a strategy screen into a place where something is happening right now.
type LiveBattle struct {
	MatchID   string        `json:"match_id"`
	FrontID   string        `json:"front_id"`
	FrontName string        `json:"front_name"`
	NodeID    string        `json:"node_id"`
	Map       string        `json:"map"`
	Mode      string        `json:"mode"`
	StageKind war.StageKind `json:"stage_kind"`
	Stage     int           `json:"stage"`
	State     MatchState    `json:"state"`
	Players   int           `json:"players"`
	RedScore  uint32        `json:"red_score"`
	BluScore  uint32        `json:"blu_score"`
	StartedAt time.Time     `json:"started_at"`
}
