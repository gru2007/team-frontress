package gc

import (
	"time"

	"github.com/greyline-frontress/coordinator/internal/legacy/hostelect"
	"github.com/greyline-frontress/coordinator/internal/legacy/war"
	"github.com/greyline-frontress/coordinator/internal/security"
	"github.com/greyline-frontress/coordinator/internal/wire/pb"
)

// RosterSlot is one player's place in a match.
type RosterSlot struct {
	SteamID   uint64
	Side      war.Side
	Connected bool // reported connected to the game server
	Present   bool // coordinator link is alive
	IsHost    bool
	// LeftAt is when the player dropped out of a live match. While inside the
	// reconnect grace window their slot is held rather than freed.
	LeftAt time.Time
	RTTMs  uint32
}

// attestation is a non-host client's account of how the match ended.
type attestation struct {
	outcome war.Outcome
	red     uint32
	blu     uint32
	at      time.Time
}

// Match is one battle instance, from roster assembly to a recorded result.
type Match struct {
	ID      string
	FrontID string
	Map     string
	Mode    string
	MaxPlrs int
	// CanMigrate is false for modes whose state does not survive a change of
	// host. Such a battle is abandoned rather than resumed wrongly.
	CanMigrate bool

	State      pb.EMatchState
	StateSince time.Time
	CreatedAt  time.Time

	Roster map[uint64]*RosterSlot
	Order  []uint64 // stable iteration order, for reproducible logs

	HostID  uint64
	Secret  []byte
	Policy  *security.Policy
	Ranking []hostelect.Scored

	// Where the battle lives once the host has it up, exactly as the host
	// reported it. The coordinator forwards these two and interprets neither:
	// under Steam Networking the address is a FakeIP standing in for a P2P/SDR
	// route, so it means nothing outside the engine that produced it.
	ConnectAddress    string
	GameServerSteamID uint64

	Snapshot   *pb.CMsgMatchSnapshot
	Migrations int
	// FailedHosts are accounts that were assigned host and did not deliver.
	// They stay excluded for the rest of this match.
	FailedHosts map[uint64]bool

	// Security bookkeeping.
	Flags           int
	MissedIntegrity int
	LastIntegrity   time.Time

	HostResult *pb.CMsgMatchResult
	// ResultAt is when the host's result arrived. The corroboration window is
	// measured from here, not from StateSince, which still points at the start
	// of the match.
	ResultAt     time.Time
	Attestations map[uint64]attestation

	// Set once the match has been resolved, so a late duplicate result from a
	// retrying host cannot double-count towards the war.
	Resolved bool
}

func newMatch(id, frontID string, m war.MapEntry, maxPlayers int, policy *security.Policy, secret []byte) *Match {
	now := time.Now()
	return &Match{
		ID:           id,
		FrontID:      frontID,
		Map:          m.Map,
		Mode:         m.Mode,
		CanMigrate:   m.CanMigrate(),
		MaxPlrs:      maxPlayers,
		State:        pb.EMatchState_MATCH_FORMING,
		StateSince:   now,
		CreatedAt:    now,
		Roster:       make(map[uint64]*RosterSlot),
		FailedHosts:  make(map[uint64]bool),
		Attestations: make(map[uint64]attestation),
		Policy:       policy,
		Secret:       secret,
	}
}

func (m *Match) add(steamID uint64, side war.Side) {
	if _, ok := m.Roster[steamID]; ok {
		return
	}
	m.Roster[steamID] = &RosterSlot{SteamID: steamID, Side: side, Present: true}
	m.Order = append(m.Order, steamID)
}

func (m *Match) setState(s pb.EMatchState) {
	m.State = s
	m.StateSince = time.Now()
	// Start the integrity clock when play begins, so a host that never sends a
	// single report is caught by the same staleness check as one that stops.
	if s == pb.EMatchState_MATCH_LIVE && m.LastIntegrity.IsZero() {
		m.LastIntegrity = m.StateSince
	}
}

func (m *Match) in(s pb.EMatchState) bool { return m.State == s }

// hosted reports whether there is a live server to send anyone to. False
// between the host being elected and it reporting ready, and again for the
// length of a migration.
func (m *Match) hosted() bool {
	return m.ConnectAddress != "" || m.GameServerSteamID != 0
}

// live reports whether the match is in a state where players expect to be
// playing, which is what decides whether leaving counts as an abandon.
func (m *Match) live() bool {
	switch m.State {
	case pb.EMatchState_MATCH_LOADING, pb.EMatchState_MATCH_LIVE, pb.EMatchState_MATCH_MIGRATING:
		return true
	}
	return false
}

func (m *Match) over() bool {
	return m.State == pb.EMatchState_MATCH_FINISHED || m.State == pb.EMatchState_MATCH_ABORTED
}

// slots returns roster slots in stable order.
func (m *Match) slots() []*RosterSlot {
	out := make([]*RosterSlot, 0, len(m.Order))
	for _, id := range m.Order {
		if s, ok := m.Roster[id]; ok {
			out = append(out, s)
		}
	}
	return out
}

// nonHostPresent counts players other than the host whose coordinator link is
// alive. It is the denominator for result corroboration and for deciding that
// a host really is gone.
func (m *Match) nonHostPresent() int {
	n := 0
	for _, s := range m.slots() {
		if s.SteamID != m.HostID && s.Present {
			n++
		}
	}
	return n
}

func (m *Match) connectedCount() int {
	n := 0
	for _, s := range m.slots() {
		if s.Connected {
			n++
		}
	}
	return n
}

func (m *Match) sideCounts() (red, blu int) {
	for _, s := range m.slots() {
		switch s.Side {
		case war.SideRed:
			red++
		case war.SideBlu:
			blu++
		}
	}
	return
}

// candidates builds the host-election input for the current roster.
func (m *Match) candidates(sessions map[uint64]*Session, players *PlayerStore, excludeHost bool) []hostelect.Candidate {
	out := make([]hostelect.Candidate, 0, len(m.Order))
	for _, slot := range m.slots() {
		c := hostelect.Candidate{SteamID: slot.SteamID}
		if s, ok := sessions[slot.SteamID]; ok {
			c.Online = true
			c.Caps = s.caps
		}
		c.Hist = players.Get(slot.SteamID).History
		switch {
		case m.FailedHosts[slot.SteamID]:
			c.ExcludeReason = "already failed to host this match"
		case excludeHost && slot.SteamID == m.HostID:
			c.ExcludeReason = "is the host being replaced"
		case !slot.Present:
			c.ExcludeReason = "coordinator link is down"
		}
		out = append(out, c)
	}
	return out
}

// outcomeFromScores maps a reported scoreline onto a war outcome. Used to sanity
// check a host that reports an outcome inconsistent with its own scores.
func outcomeFromScores(red, blu uint32) war.Outcome {
	switch {
	case red > blu:
		return war.OutcomeRedWin
	case blu > red:
		return war.OutcomeBluWin
	default:
		return war.OutcomeStalemate
	}
}

func warOutcome(o pb.EMatchOutcome) war.Outcome {
	switch o {
	case pb.EMatchOutcome_OUTCOME_RED_WIN:
		return war.OutcomeRedWin
	case pb.EMatchOutcome_OUTCOME_BLU_WIN:
		return war.OutcomeBluWin
	case pb.EMatchOutcome_OUTCOME_STALEMATE:
		return war.OutcomeStalemate
	case pb.EMatchOutcome_OUTCOME_ABANDONED:
		return war.OutcomeAbandoned
	default:
		return war.OutcomeUnknown
	}
}

func pbOutcome(o war.Outcome) pb.EMatchOutcome {
	switch o {
	case war.OutcomeRedWin:
		return pb.EMatchOutcome_OUTCOME_RED_WIN
	case war.OutcomeBluWin:
		return pb.EMatchOutcome_OUTCOME_BLU_WIN
	case war.OutcomeStalemate:
		return pb.EMatchOutcome_OUTCOME_STALEMATE
	case war.OutcomeAbandoned:
		return pb.EMatchOutcome_OUTCOME_ABANDONED
	default:
		return pb.EMatchOutcome_OUTCOME_UNKNOWN
	}
}

func pbSide(s war.Side) pb.ESide {
	switch s {
	case war.SideRed:
		return pb.ESide_SIDE_RED
	case war.SideBlu:
		return pb.ESide_SIDE_BLU
	default:
		return pb.ESide_SIDE_UNASSIGNED
	}
}
