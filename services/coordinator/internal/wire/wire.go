// Package wire holds the coordinator's transport-neutral matchmaking domain
// types. Game clients and dedicated servers use Valve protobuf messages at the
// GC boundary; these types are used between the GC and durable backend.
package wire

// SteamID is a SteamID64 carried as a decimal string.
//
// It is never a JSON number: SteamID64 does not survive a float64, and the
// game's own menu learned that the hard way. Keep it a string end to end.
type SteamID string

// MatchGroup mirrors ETFMatchGroup in tf_gcmessages.proto. Only the values the
// coordinator serves are named here.
type MatchGroup int32

const (
	MatchGroupInvalid     MatchGroup = -1
	MatchGroupMvMPractice MatchGroup = 0
	MatchGroupMvMMannUp   MatchGroup = 1
	MatchGroupLadder6v6   MatchGroup = 2
	MatchGroupLadder9v9   MatchGroup = 3
	MatchGroupLadder12v12 MatchGroup = 4
	MatchGroupCasual6v6   MatchGroup = 5
	MatchGroupCasual9v9   MatchGroup = 6
	MatchGroupCasual12v12 MatchGroup = 7
)

// Team mirrors the game's team indices.
type Team int32

const (
	TeamUnassigned Team = 0
	TeamSpectator  Team = 1
	TeamRed        Team = 2
	TeamBlu        Team = 3
)

func (t Team) String() string {
	switch t {
	case TeamRed:
		return "RED"
	case TeamBlu:
		return "BLU"
	case TeamSpectator:
		return "SPEC"
	default:
		return "UNASSIGNED"
	}
}

// QueueState is the lifecycle of a queue ticket.
type QueueState string

const (
	QueueStateSearching QueueState = "searching"
	QueueStateAssigned  QueueState = "assigned"
	QueueStateCancelled QueueState = "cancelled"
	QueueStateExpired   QueueState = "expired"
	QueueStateFailed    QueueState = "failed"
)

// Assignment is a formed match handed to a client.
//
// Connect and Password are everything the game needs; the rest is what the
// briefing and the roster gate are built on.
type Assignment struct {
	MatchID    string     `json:"match_id"`
	MatchGroup MatchGroup `json:"match_group"`
	Map        string     `json:"map"`
	Connect    string     `json:"connect"`
	Password   string     `json:"password,omitempty"`
	// STV is the SourceTV address, if the server running this match has one.
	// Spectating never needs the match password.
	STV string `json:"stv,omitempty"`
	// LateJoin is true when this assignment topped up a match that was already
	// running. The client uses it to skip the "match found" ceremony and just
	// connect.
	LateJoin bool `json:"late_join,omitempty"`
	// Team the player was assigned to, or TeamUnassigned when the coordinator
	// leaves teams to the server.
	Team Team `json:"team"`
	// Roster is every player in the match, in both teams, so the client can
	// show who it is playing with before it connects.
	Roster []AssignedPlayer `json:"roster,omitempty"`
	// War is present when the war layer chose this battle. Empty otherwise.
	War *WarBriefing `json:"war,omitempty"`
}

// AssignedPlayer is one seat in a formed match.
type AssignedPlayer struct {
	SteamID SteamID `json:"steam_id"`
	Name    string  `json:"name,omitempty"`
	Team    Team    `json:"team"`
	// Rating is gateway policy input fetched from tf2pickup. It is never sent
	// to clients or game servers as part of the roster protocol.
	Rating int `json:"-"`
}

// WarBriefing states a battle's place in the campaign. It is the seam the
// strategic layer plugs into; nothing in matchmaking depends on it being set.
type WarBriefing struct {
	FrontID    string `json:"front_id"`
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name"`
	StageIndex int    `json:"stage_index"`
	StageCount int    `json:"stage_count"`
	StageKind  string `json:"stage_kind"`
	// BattleMode is what the chosen battlefield plays as ("koth", "payload",
	// "attack_defend"). The briefing carries it so the client can say what
	// kind of fight this is without knowing the map table.
	BattleMode  string `json:"battle_mode,omitempty"`
	AttackerWar string `json:"attacker_war"` // "RED" or "BLU" in war terms
	// AttackerTeam is the game team the attacker wears this battle. The war
	// side and the game team are not the same thing.
	AttackerTeam Team `json:"attacker_team"`
}

// QueueStatus is the reply to polling a queue ticket.
type QueueStatus struct {
	TicketID    string      `json:"ticket_id"`
	State       QueueState  `json:"state"`
	QueuedSecs  int         `json:"queued_secs"`
	InQueue     int         `json:"in_queue"`     // players queued for this group
	NeedPlayers int         `json:"need_players"` // how many more to form now
	PollAfterMS int         `json:"poll_after_ms"`
	Assignment  *Assignment `json:"assignment,omitempty"`
	Error       string      `json:"error,omitempty"`
	// Detail is a short line for the player about why the queue is not
	// moving: most often a match that has already formed and is waiting for
	// a free server. "searching" with nothing else on screen is the same
	// picture whether the queue is empty or the pool is, and those are very
	// different things to be waiting for.
	Detail string `json:"detail,omitempty"`
}

// PlayerProgress is what the coordinator remembers about one player, in the
// shape the menu wants to draw: the record, and the XP and level derived from
// it. Nothing here is a skill rating -- see internal/players/progress.go.
type PlayerProgress struct {
	SteamID  SteamID `json:"steam_id"`
	Name     string  `json:"name,omitempty"`
	Matches  int     `json:"matches"`
	Wins     int     `json:"wins"`
	Losses   int     `json:"losses"`
	Abandons int     `json:"abandons"`
	XP       int     `json:"xp"`
	Level    int     `json:"level"`
	// LevelXP / LevelXPTotal are the progress bar: how far into this level,
	// and how much the level takes.
	LevelXP      int `json:"level_xp"`
	LevelXPTotal int `json:"level_xp_total"`
}

// Status is the public health/population view. The main menu can render this
// without authenticating.
type Status struct {
	Name          string         `json:"name"`
	OnlinePlayers int            `json:"online_players"`
	QueuedPlayers map[string]int `json:"queued_players"` // match group id -> players
	LiveMatches   int            `json:"live_matches"`
	FreeServers   int            `json:"free_servers"`
	// 1 when FreeServers is an exact count. A remote/on-demand provider
	// such as serveme cannot answer that without making an allocation
	// request, so reporting zero as though it were exact is misleading.
	ServerCapacityKnown int              `json:"server_capacity_known"`
	MatchGroups         []MatchGroupInfo `json:"match_groups"`
	War                 *WarStatus       `json:"war,omitempty"`
}

// MatchGroupInfo tells the client which groups are worth showing.
type MatchGroupInfo struct {
	MatchGroup MatchGroup `json:"match_group"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	// Mode is "frontline" or "ranked". See config.Mode.
	Mode       string `json:"mode"`
	MinPlayers int    `json:"min_players"`
	MaxPlayers int    `json:"max_players"`
	// Backfill is true when a match in this group keeps filling while it runs.
	Backfill bool `json:"backfill"`
	// Maps is what this group actually plays. The menu shows a map list the
	// player picks from, and picking a map nobody here runs is worse than not
	// offering it: the queue silently plays something else.
	Maps []string `json:"maps,omitempty"`
	// OpenMatches is how many live matches are currently accepting players.
	OpenMatches int `json:"open_matches"`
	// Restrictions is present when the group has entry rules, so the menu can
	// say why a queue is closed before the player presses play. It is what the
	// rules are, never who they were applied to.
	Restrictions *GroupRestrictions `json:"restrictions,omitempty"`
}

// GroupRestrictions is the public description of a match group's entry rules.
type GroupRestrictions struct {
	// MaxPartySize is the biggest party the group accepts, 1 for solo queue.
	MaxPartySize int `json:"max_party_size,omitempty"`
	// MinPartySize is the smallest, for a group queued as a full team.
	MinPartySize int `json:"min_party_size,omitempty"`
	// MinMatchesPlayed gates the group behind experience elsewhere.
	MinMatchesPlayed int `json:"min_matches_played,omitempty"`
	// RequiresVerifiedAuth means a Steam ticket, not a claimed SteamID.
	RequiresVerifiedAuth bool `json:"requires_verified_auth,omitempty"`
	// InviteOnly means the group has an allow list.
	InviteOnly bool `json:"invite_only,omitempty"`
	// AbandonCooldownMins is the penalty for walking out of a match.
	AbandonCooldownMins int `json:"abandon_cooldown_mins,omitempty"`
}

// WarStatus is the strategic summary. Stage-3 groundwork.
type WarStatus struct {
	CampaignID   string      `json:"campaign_id"`
	ActiveFronts []FrontInfo `json:"active_fronts"`
}

// FrontInfo is one open front.
type FrontInfo struct {
	FrontID    string `json:"front_id"`
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name"`
	Attacker   string `json:"attacker"`
	StageIndex int    `json:"stage_index"`
	StageCount int    `json:"stage_count"`
	StageKind  string `json:"stage_kind"`
	Map        string `json:"map"`
}

// MatchResult is a finished match reported by the server that ran it.
type MatchResult struct {
	MatchID string `json:"match_id"`
	// Winner is the game team that won, or TeamUnassigned for a draw or an
	// aborted match.
	Winner   Team             `json:"winner"`
	RedScore int              `json:"red_score"`
	BluScore int              `json:"blu_score"`
	Aborted  bool             `json:"aborted"`
	Players  []AssignedPlayer `json:"players,omitempty"`
}

// Error is the body of every non-2xx reply.
type Error struct {
	Error string `json:"error"`
}
