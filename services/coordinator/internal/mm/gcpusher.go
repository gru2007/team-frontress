package mm

import (
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// GCServerPusher hands a dedicated server its match roster over the native
// GC transport: the same CSOTFGameServerLobby shared-object push a client's
// party already receives, addressed to the server's own GC session instead
// of a player's. gcparty.Manager implements this; mm only sees the
// interface, both because gcparty already imports mm (a direct import back
// would cycle) and because RCON-driven ServerSetup implementations and
// tests should not need a gcparty dependency to build.
//
// This is deliberately not a bespoke RCON command and not a SourceMod
// plugin surface: it is CTFGCServerSystem's own CSOTFGameServerLobby, the
// exact shared object real Valve matchmaking servers build CMatchInfo from,
// which is what makes tf_mm_strict's roster gate -- not the password -- the
// door a matched player walks through.
type GCServerPusher interface {
	// PushMatchRoster gives serverSteamID's GC session the current full
	// roster for the match it was just assigned or had a seat added to.
	// Callers pass the match's complete current player list, not a delta:
	// CSOTFGameServerLobby is a snapshot object, so an update replaces
	// Members wholesale.
	PushMatchRoster(serverSteamID uint64, spec ServerRosterSpec)
	// ClearMatchRoster tears down the lobby object a returned server no
	// longer holds a match for, mirroring RCONSetup.Teardown clearing the
	// stock convars it still owns (sv_password, tf_match_emulation, ...).
	ClearMatchRoster(serverSteamID uint64)
}

// ServerRosterSpec is what a game server's GC session needs to build its own
// CSOTFGameServerLobby -- the same fields the client-side copy already
// carries (see gcparty.Manager.buildLobbySOLocked), addressed to the server
// instead of a party.
type ServerRosterSpec struct {
	MatchID    string
	MatchGroup wire.MatchGroup
	Map        string
	Connect    string
	Roster     []wire.AssignedPlayer
}

// SetGCPusher wires the native GC roster-push path in. Called once from
// main after both the matchmaker and the GC party manager exist; nil (the
// zero value) is a valid, intentional configuration for a coordinator
// running with GC disabled, in which case pushServerRoster is a no-op and
// matches fall back to the password gate only.
func (m *Matchmaker) SetGCPusher(p GCServerPusher) {
	m.gcPusher = p
}

// pushServerRoster delivers mt's current roster to its assigned server over
// GC, if the coordinator has both a pusher configured and a known GC
// identity for that server's Connect address. Missing either is logged and
// otherwise ignored: a server matchmaking cannot reach over GC still runs
// the match on the password alone, exactly like an unmodified dedicated
// server would.
func (m *Matchmaker) pushServerRoster(connect string, mt *Match) {
	if m.gcPusher == nil || connect == "" {
		return
	}
	m.mu.Lock()
	spec := ServerRosterSpec{
		MatchID:    mt.ID,
		MatchGroup: mt.MatchGroup,
		Map:        mt.Map,
		Connect:    connect,
		Roster:     append([]wire.AssignedPlayer(nil), mt.Players...),
	}
	m.mu.Unlock()

	steamID, ok := m.cfg.GC.ServerSteamID(connect)
	if !ok {
		m.log.Debug("no GC identity configured for server, skipping native roster push",
			"match", spec.MatchID, "server", connect)
		return
	}
	m.gcPusher.PushMatchRoster(steamID, spec)
}

// clearServerRoster tears down the GC lobby object for a server being
// returned to the pool, mirroring pushServerRoster's identity resolution.
func (m *Matchmaker) clearServerRoster(connect string) {
	if m.gcPusher == nil || connect == "" {
		return
	}
	steamID, ok := m.cfg.GC.ServerSteamID(connect)
	if !ok {
		return
	}
	m.gcPusher.ClearMatchRoster(steamID)
}
