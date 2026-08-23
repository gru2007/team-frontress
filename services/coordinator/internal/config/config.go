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
