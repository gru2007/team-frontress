// Package config loads and validates the coordinator's settings.
//
// Everything the coordinator does is driven from this file: which match groups
// exist, how small a match may be, where servers come from, whether identity is
// verified. There are no hardcoded maps, modes or campaigns anywhere else.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// Config is the whole coordinator.
type Config struct {
	Name string `json:"name"`
	// Listen is the public HTTP address, e.g. ":27100".
	Listen string `json:"listen"`
	// Secret authenticates dedicated servers and result reports. Required
	// unless the pool has no self-registering servers.
	Secret string `json:"secret"`

	Auth        AuthConfig         `json:"auth"`
	Players     PlayersConfig      `json:"players"`
	MatchGroups []MatchGroupConfig `json:"match_groups"`
	Pool        PoolConfig         `json:"pool"`
	Timing      TimingConfig       `json:"timing"`
	War         WarConfig          `json:"war"`
}

// AuthConfig decides how much the coordinator believes a client.
type AuthConfig struct {
	// Mode is "dev" (take the client's word for its SteamID) or "webapi"
	// (verify the Steam auth session ticket). Only "webapi" produces
	// verified identities; the game will not enforce a roster without one.
	Mode string `json:"mode"`
	// SteamAPIKey is required by mode "webapi".
	SteamAPIKey string `json:"steam_api_key"`
	// AppID the tickets are issued for. Team Frontress playtest is 5147520.
	AppID uint32 `json:"app_id"`
}

// Verified reports whether this mode produces identities the game may trust.
func (a AuthConfig) Verified() bool { return a.Mode == "webapi" }

// PlayersConfig is the record the coordinator keeps of the people playing.
//
// It exists for one reason: a restriction like "five matches before ranked" or
// "no queue for half an hour after you walk out" needs to remember something
// across a restart. Matchmaking itself keeps working with this off; only the
// restrictions that depend on history stop being enforceable.
type PlayersConfig struct {
	// File is an append-only JSONL log, folded on startup. Empty keeps the
	// records in memory, which a restart forgets.
	File string `json:"file"`
	// AbandonGraceSecs is how long after a match starts a player may still
	// show up before never connecting counts as abandoning it.
	AbandonGraceSecs int `json:"abandon_grace_secs"`
}

// AbandonGrace is AbandonGraceSecs with a default.
func (p PlayersConfig) AbandonGrace() time.Duration {
	return dur(p.AbandonGraceSecs, time.Second, 5*time.Minute)
}

// Mode is how a match group behaves. It is not a game mode; it is what the
// coordinator is allowed to do with the match.
type Mode string

const (
	// ModeFrontline is the open queue: matches keep filling while they run, so
	// a server that started as a 4v4 becomes a 12v12 as people arrive.
	ModeFrontline Mode = "frontline"
	// ModeRanked forms a fixed roster and leaves it alone. Nobody is added to
	// a ranked match after it starts.
	ModeRanked Mode = "ranked"
)

// MatchGroupConfig is one playable queue.
//
// MinPlayers is the floor that makes small populations work: a group whose
// MinPlayers is 4 will form a 2v2 rather than leave four people waiting for a
// twelfth. IdealPlayers is what the coordinator forms when it can.
type MatchGroupConfig struct {
	MatchGroup wire.MatchGroup `json:"match_group"`
	Name       string          `json:"name"`
	Enabled    bool            `json:"enabled"`
	// Mode defaults to frontline. See Mode.
	Mode         Mode `json:"mode"`
	MinPlayers   int  `json:"min_players"`
	IdealPlayers int  `json:"ideal_players"`
	MaxPlayers   int  `json:"max_players"`
	// Maps this group may pick from when the parties express no preference
	// and the war layer is off.
	Maps []string `json:"maps"`
	// ServerConfig is exec'd on the game server before the map change, e.g.
	// "server_casual". Empty means no config.
	ServerConfig string `json:"server_config"`
	// PatientSecs is how long the coordinator holds out for IdealPlayers
	// before it will settle for MinPlayers. Zero means never hold out.
	PatientSecs int `json:"patient_secs"`
	// BackfillSecs is how long after a match starts it still accepts new
	// players. Zero means for as long as it runs; negative disables backfill
	// even in frontline mode.
	//
	// Only frontline matches backfill at all.
	BackfillSecs int `json:"backfill_secs"`
	// Restrictions are who may queue for this group at all. An absent block
	// is the open queue, which is what casual wants and what ranked does not.
	Restrictions Restrictions `json:"restrictions"`
}

// Restrictions gate a match group's queue.
//
// Everything here is off when unset, so a group without a restrictions block
// behaves exactly as it did before this existed. Ranked is the reason it
// exists: a queue whose result is recorded against a player is a queue that
// has to know who the player is, that nobody is stacking a six-stack against
// solo queuers, and that the person who walked out of the last match is not
// starting another one this minute.
type Restrictions struct {
	// RequireVerifiedAuth refuses the group unless auth.mode is "webapi".
	// Validation rejects the combination outright rather than letting a
	// deployment discover it at the first queue.
	RequireVerifiedAuth bool `json:"require_verified_auth"`
	// MaxPartySize is the biggest party that may queue together. Zero means
	// the group's own limit (half a match). One is solo queue.
	MaxPartySize int `json:"max_party_size"`
	// MinPartySize refuses parties smaller than this, for a group that is
	// meant to be queued as a full team.
	MinPartySize int `json:"min_party_size"`
	// MinMatchesPlayed is how many finished matches -- in any group -- a
	// player needs before this one opens up. Needs players.file to survive a
	// restart; without it, it counts matches since the coordinator started.
	MinMatchesPlayed int `json:"min_matches_played"`
	// AbandonCooldownMins keeps a player who left a match early out of this
	// group for a while afterwards.
	AbandonCooldownMins int `json:"abandon_cooldown_mins"`
	// MaxAbandons removes a player from this group entirely once they have
	// abandoned this many matches. Zero means never.
	MaxAbandons int `json:"max_abandons"`
	// AllowedSteamIDs, when set, is the only list of people who may queue:
	// a closed test, a league, an invite-only ladder.
	AllowedSteamIDs []wire.SteamID `json:"allowed_steam_ids"`
	// BannedSteamIDs may not queue for this group. It is deliberately per
	// group: a casual ban and a ranked ban are different punishments.
	BannedSteamIDs []wire.SteamID `json:"banned_steam_ids"`
}

// Any reports whether this block restricts anything at all.
func (r Restrictions) Any() bool {
	return r.RequireVerifiedAuth || r.MaxPartySize > 0 || r.MinPartySize > 0 ||
		r.MinMatchesPlayed > 0 || r.AbandonCooldownMins > 0 || r.MaxAbandons > 0 ||
		len(r.AllowedSteamIDs) > 0 || len(r.BannedSteamIDs) > 0
}

// PartyCap is the largest party this group accepts, restrictions included.
func (g MatchGroupConfig) PartyCap() int {
	cap := g.MaxPlayers / 2
	if n := g.Restrictions.MaxPartySize; n > 0 && n < cap {
		return n
	}
	return cap
}

// BBackfills reports whether this group tops matches up while they run.
func (g MatchGroupConfig) BBackfills() bool {
	return g.EffectiveMode() == ModeFrontline && g.BackfillSecs >= 0
}

// EffectiveMode is Mode with the default applied.
func (g MatchGroupConfig) EffectiveMode() Mode {
	if g.Mode == "" {
		return ModeFrontline
	}
	return g.Mode
}

// TeamCap is the most players one team may hold. Keeping every team at or
// below it is what keeps a backfilled match balanced without any further
// arithmetic: nobody can be added to a side that is already half the match.
func (g MatchGroupConfig) TeamCap() int { return g.MaxPlayers / 2 }

// PoolConfig says where game servers come from.
type PoolConfig struct {
	// Providers is evaluated in order; the first that yields a server wins.
	// Known kinds: "static", "registered", "serveme".
	Providers []ProviderConfig `json:"providers"`
	// BootDeadlineSecs is how long a reserved server has to answer RCON
	// before the assignment is abandoned and the parties re-queued.
	BootDeadlineSecs int `json:"boot_deadline_secs"`
	// IdleEndSecs ends a match whose server has had no players for this long.
	IdleEndSecs int `json:"idle_end_secs"`
	// MaxMatchSecs ends a match this long after it started, no matter what.
	MaxMatchSecs int `json:"max_match_secs"`
}

// ProviderConfig configures one source of servers.
type ProviderConfig struct {
	Kind   string `json:"kind"`
	Region string `json:"region,omitempty"`

	// kind "static": servers the coordinator drives over RCON directly.
	Servers []StaticServer `json:"servers,omitempty"`

	// kind "serveme": a serveme.tf (or fork) reservation API.
	BaseURL        string `json:"base_url,omitempty"`
	APIKey         string `json:"api_key,omitempty"`
	ReserveMins    int    `json:"reserve_mins,omitempty"`
	ServerConfigID int    `json:"server_config_id,omitempty"`
	// PreferDocker asks for a container host when serveme offers both those
	// and bare-metal servers. It is the default deployment: a match starts a
	// container, plays, and the container is destroyed with the reservation.
	PreferDocker bool `json:"prefer_docker,omitempty"`
	// ReadyTimeoutSecs bounds the wait for a reservation's server to boot.
	// Zero falls back to the caller's deadline (pool.boot_deadline_secs),
	// which is usually what you want.
	ReadyTimeoutSecs int `json:"ready_timeout_secs,omitempty"`
}

// StaticServer is a server the operator runs and the coordinator owns.
type StaticServer struct {
	Name    string `json:"name"`
	Connect string `json:"connect"` // ip:port
	RCON    string `json:"rcon"`
	Region  string `json:"region,omitempty"`
	// STV is the SourceTV address players use to spectate, "ip:port". It is
	// only reported, never configured: SourceTV has to be enabled when the
	// server starts, not over RCON.
	STV string `json:"stv,omitempty"`
}

// TimingConfig holds the clocks.
type TimingConfig struct {
	// TickMS is how often the matchmaker tries to form matches.
	TickMS int `json:"tick_ms"`
	// TicketTTLSecs expires a queue ticket the client stopped polling.
	TicketTTLSecs int `json:"ticket_ttl_secs"`
	// PollAfterMS is what the client is told to wait between polls.
	PollAfterMS int `json:"poll_after_ms"`
	// AssignmentTTLSecs is how long an assignment stays fetchable after the
	// match forms, so a client that reconnects can still find its way in.
	AssignmentTTLSecs int `json:"assignment_ttl_secs"`
}

// WarConfig is the strategic layer. Off by default: matchmaking works without
// it, and the war is stage three.
type WarConfig struct {
	Enabled bool `json:"enabled"`
	// TheaterFile is the campaign map: nodes, adjacency, HQs, stage plans.
	TheaterFile string `json:"theater_file"`
	// EventLog is the append-only record of what the war did. It is the
	// source of truth for war state; delete it to start a new campaign.
	EventLog string `json:"event_log"`
}

// Defaults returns a config that runs a small pickup server out of the box.
func Defaults() Config {
	return Config{
		Name:   "Team Frontress",
		Listen: ":27100",
		Auth:   AuthConfig{Mode: "dev", AppID: 5147520},
		MatchGroups: []MatchGroupConfig{
			{
				MatchGroup:   wire.MatchGroupCasual12v12,
				Name:         "Casual Frontline",
				Mode:         ModeFrontline,
				Enabled:      true,
				MinPlayers:   4,
				IdealPlayers: 12,
				MaxPlayers:   24,
				PatientSecs:  60,
				ServerConfig: "server_casual",
			},
			{
				MatchGroup:   wire.MatchGroupLadder6v6,
				Name:         "Ranked 6v6",
				Mode:         ModeRanked,
				Enabled:      false,
				MinPlayers:   12,
				IdealPlayers: 12,
				MaxPlayers:   12,
				ServerConfig: "server_competitive",
			},
		},
		Pool: PoolConfig{
			BootDeadlineSecs: 90,
			IdleEndSecs:      300,
			MaxMatchSecs:     3 * 60 * 60,
		},
		Timing: TimingConfig{
			TickMS:            1000,
			TicketTTLSecs:     45,
			PollAfterMS:       2000,
			AssignmentTTLSecs: 600,
		},
	}
}

// Load reads a config file over the defaults and validates the result.
func Load(path string) (Config, error) {
	cfg := Defaults()
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	// Unmarshalling over the defaults keeps unspecified scalars, but a
	// specified match_groups or providers array replaces the default wholesale
	// — which is what an operator listing their own groups means.
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Validate rejects a config that would fail later, at a worse moment.
func (c Config) Validate() error {
	if c.Listen == "" {
		return errors.New("listen must be set")
	}
	switch c.Auth.Mode {
	case "dev":
	case "webapi":
		if c.Auth.SteamAPIKey == "" {
			return errors.New("auth.steam_api_key is required for auth.mode webapi")
		}
		if c.Auth.AppID == 0 {
			return errors.New("auth.app_id is required for auth.mode webapi")
		}
	default:
		return fmt.Errorf("auth.mode %q: want \"dev\" or \"webapi\"", c.Auth.Mode)
	}

	enabled := 0
	seen := map[wire.MatchGroup]bool{}
	for i, g := range c.MatchGroups {
		if seen[g.MatchGroup] {
			return fmt.Errorf("match_groups[%d]: duplicate match_group %d", i, g.MatchGroup)
		}
		seen[g.MatchGroup] = true
		if !g.Enabled {
			continue
		}
		enabled++
		switch {
		case g.MinPlayers < 2:
			return fmt.Errorf("match_groups[%d] (%s): min_players must be at least 2", i, g.Name)
		case g.MinPlayers%2 != 0:
			return fmt.Errorf("match_groups[%d] (%s): min_players must be even, teams are equal", i, g.Name)
		case g.IdealPlayers < g.MinPlayers:
			return fmt.Errorf("match_groups[%d] (%s): ideal_players %d is below min_players %d", i, g.Name, g.IdealPlayers, g.MinPlayers)
		case g.MaxPlayers < g.IdealPlayers:
			return fmt.Errorf("match_groups[%d] (%s): max_players %d is below ideal_players %d", i, g.Name, g.MaxPlayers, g.IdealPlayers)
		}
		if len(g.Maps) == 0 && !c.War.Enabled {
			return fmt.Errorf("match_groups[%d] (%s): needs maps, or war.enabled to choose them", i, g.Name)
		}
		switch g.EffectiveMode() {
		case ModeFrontline, ModeRanked:
		default:
			return fmt.Errorf("match_groups[%d] (%s): mode %q: want %q or %q",
				i, g.Name, g.Mode, ModeFrontline, ModeRanked)
		}
		if g.MaxPlayers%2 != 0 {
			return fmt.Errorf("match_groups[%d] (%s): max_players must be even, teams are equal", i, g.Name)
		}
		if err := g.Restrictions.validate(g, c.Auth); err != nil {
			return fmt.Errorf("match_groups[%d] (%s): %w", i, g.Name, err)
		}
	}
	if enabled == 0 {
		return errors.New("no enabled match groups")
	}

	if len(c.Pool.Providers) == 0 {
		return errors.New("pool.providers is empty: nothing can host a match")
	}
	needSecret := false
	for i, p := range c.Pool.Providers {
		switch p.Kind {
		case "static":
			if len(p.Servers) == 0 {
				return fmt.Errorf("pool.providers[%d]: static provider has no servers", i)
			}
			for j, s := range p.Servers {
				if s.Connect == "" {
					return fmt.Errorf("pool.providers[%d].servers[%d]: connect must be set", i, j)
				}
			}
		case "registered":
			needSecret = true
		case "serveme":
			if p.BaseURL == "" || p.APIKey == "" {
				return fmt.Errorf("pool.providers[%d]: serveme provider needs base_url and api_key", i)
			}
		default:
			return fmt.Errorf("pool.providers[%d]: unknown kind %q", i, p.Kind)
		}
	}
	if needSecret && c.Secret == "" {
		return errors.New("secret must be set when servers register themselves")
	}

	if c.War.Enabled && c.War.TheaterFile == "" {
		return errors.New("war.theater_file is required when war.enabled")
	}
	return nil
}

// validate rejects a restrictions block that cannot be satisfied, at boot
// rather than at the first queue request.
func (r Restrictions) validate(g MatchGroupConfig, auth AuthConfig) error {
	if r.RequireVerifiedAuth && !auth.Verified() {
		return fmt.Errorf("restrictions.require_verified_auth needs auth.mode %q, not %q", "webapi", auth.Mode)
	}
	teamCap := g.MaxPlayers / 2
	if r.MaxPartySize < 0 {
		return errors.New("restrictions.max_party_size cannot be negative")
	}
	if r.MaxPartySize > teamCap {
		return fmt.Errorf("restrictions.max_party_size %d is bigger than a team (%d)", r.MaxPartySize, teamCap)
	}
	if r.MinPartySize < 0 {
		return errors.New("restrictions.min_party_size cannot be negative")
	}
	if r.MinPartySize > teamCap {
		return fmt.Errorf("restrictions.min_party_size %d cannot fit one team (%d)", r.MinPartySize, teamCap)
	}
	if max := r.MaxPartySize; max > 0 && r.MinPartySize > max {
		return fmt.Errorf("restrictions.min_party_size %d is above max_party_size %d", r.MinPartySize, max)
	}
	for _, id := range append(append([]wire.SteamID(nil), r.AllowedSteamIDs...), r.BannedSteamIDs...) {
		if !validSteamID(id) {
			return fmt.Errorf("restrictions: %q is not a SteamID64", id)
		}
	}
	return nil
}

// validSteamID is the config-time copy of the check steamauth does at runtime.
// Keeping it here avoids an import cycle for one loop over digits.
func validSteamID(id wire.SteamID) bool {
	if len(id) < 17 || len(id) > 20 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return id[0] == '7'
}

// Group returns the config for a match group, or false.
func (c Config) Group(g wire.MatchGroup) (MatchGroupConfig, bool) {
	for _, mg := range c.MatchGroups {
		if mg.MatchGroup == g {
			return mg, true
		}
	}
	return MatchGroupConfig{}, false
}

// Durations, so callers do not sprinkle time.Duration arithmetic around.

func (t TimingConfig) Tick() time.Duration { return dur(t.TickMS, time.Millisecond, time.Second) }
func (t TimingConfig) TicketTTL() time.Duration {
	return dur(t.TicketTTLSecs, time.Second, 45*time.Second)
}
func (t TimingConfig) AssignmentTTL() time.Duration {
	return dur(t.AssignmentTTLSecs, time.Second, 10*time.Minute)
}
func (p PoolConfig) BootDeadline() time.Duration {
	return dur(p.BootDeadlineSecs, time.Second, 90*time.Second)
}
func (p PoolConfig) IdleEnd() time.Duration { return dur(p.IdleEndSecs, time.Second, 5*time.Minute) }
func (p PoolConfig) MaxMatch() time.Duration {
	return dur(p.MaxMatchSecs, time.Second, 3*time.Hour)
}

func dur(n int, unit, fallback time.Duration) time.Duration {
	if n <= 0 {
		return fallback
	}
	return time.Duration(n) * unit
}
