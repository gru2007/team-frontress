// Package config holds the coordinator's runtime configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// AuthMode selects how a client's claimed SteamID is verified.
type AuthMode string

const (
	// AuthDev trusts the SteamID asserted in the hello. LAN and local testing
	// only — anyone can claim to be anyone.
	AuthDev AuthMode = "dev"
	// AuthWebAPI validates the Steam auth session ticket through
	// ISteamUserAuth/AuthenticateUserTicket. Requires a publisher Web API key
	// for the AppID the game ships under.
	AuthWebAPI AuthMode = "webapi"
)

// Config is the whole coordinator configuration. Loaded from JSON, with
// environment overrides for the secrets.
type Config struct {
	// APIListen is the HTTP address clients and server agents talk to. This is
	// the coordinator's live protocol.
	APIListen string `json:"api_listen"`
	// AdminListen serves /healthz and the read-only state endpoints.
	AdminListen string `json:"admin_listen"`

	// TheaterPath is the war's static definition: the node graph and its
	// battlefields.
	TheaterPath string `json:"theater_path"`
	// WarLogPath is the append-only war event log. It is the world: deleting it
	// starts a new war, and keeping it lets one be replayed.
	WarLogPath string `json:"war_log_path"`

	Pool PoolConfig `json:"pool"`
	War  WarConfig  `json:"war"`

	// Listen is the old framed-protobuf listener used by the retired P2P
	// coordinator. It is kept so an existing config file still loads; nothing
	// reads it any more.
	Listen string `json:"listen,omitempty"`

	Auth AuthConfig `json:"auth"`

	// ProtocolVersion clients must present in their hello.
	ProtocolVersion uint32 `json:"protocol_version"`
	// AcceptedClientVersions, when non-empty, restricts which game builds may
	// connect. Keeps a stale build from corrupting war state.
	AcceptedClientVersions []string `json:"accepted_client_versions"`

	Timing   TimingConfig   `json:"timing"`
	Match    MatchConfig    `json:"match"`
	Election ElectionConfig `json:"election"`
	Security SecurityConfig `json:"security"`

	// There is no separate state file. The war event log at WarLogPath is the
	// world: a restart replays it and gets the identical campaign back. An
	// earlier state_path setting suggested otherwise, wrote nothing, and only
	// gave an operator a file to look for when a war seemed lost.
}

type AuthConfig struct {
	Mode AuthMode `json:"mode"`
	// AppID the auth tickets are issued for.
	AppID uint32 `json:"app_id"`
	// WebAPIKey is a publisher key. Prefer the GREYLINE_STEAM_WEBAPI_KEY env
	// var over putting it in the config file.
	WebAPIKey string `json:"web_api_key"`
	// RejectVACBanned drops players with a VAC or game ban on the AppID.
	RejectVACBanned bool `json:"reject_vac_banned"`
	// RequireOwnership rejects players whose ticket resolves to a different
	// AppID than the configured one.
	RequireOwnership bool `json:"require_ownership"`
	// TicketCacheTTL avoids re-validating the same ticket on reconnect storms.
	TicketCacheTTL Duration `json:"ticket_cache_ttl"`
}

// PoolConfig governs the dedicated server pool.
type PoolConfig struct {
	// Key is the shared secret an agent presents to join the pool. Every server
	// holding it can report results that move the war, so treat it as a
	// production credential: prefer the GREYLINE_POOL_KEY env var.
	Key string `json:"key"`
	// AdminKey guards the admin endpoint. Empty is only safe when AdminListen
	// is bound to loopback.
	AdminKey string `json:"admin_key"`
	// OfflineAfter is how long a silent dedicated server stays in the pool.
	OfflineAfter Duration `json:"offline_after"`
	// OfflineAfterP2P is the same, for a player-hosted server. Shorter by
	// default: a home connection dropping is far more likely, and far less
	// recoverable, than a dedicated machine's.
	OfflineAfterP2P Duration `json:"offline_after_p2p"`
	// PollWait caps a long-poll, for both agents and clients.
	PollWait Duration `json:"poll_wait"`
}

// WarConfig is the strategic layer's tuning.
type WarConfig struct {
	// PlayersPerFront is how much population each additional active front
	// needs. This is the rule that keeps a small community in one place.
	PlayersPerFront int `json:"players_per_front"`
	// MaxFronts caps how wide the war can get.
	MaxFronts int `json:"max_fronts"`
	// MobilizationDeficit is the territory gap at which the losing side is put
	// under defensive mobilization.
	MobilizationDeficit int `json:"mobilization_deficit"`
	// Intermission is the armistice between campaigns.
	Intermission Duration `json:"intermission"`
	// CampaignName is what campaigns are numbered under.
	CampaignName string `json:"campaign_name"`
	// AttackerTeam is the in-game team the attacking side plays as in every
	// battle. "blu" matches every attack/defend and payload map ever built, so
	// a player's uniform always tells them whether they are attacking. Set it to
	// "" to let each battlefield decide instead, which means a player's colours
	// change with the map rather than with their role.
	AttackerTeam string `json:"attacker_team"`
	// FullStrengthPlayers is the headcount at which one battle is worth a whole
	// stage of an offensive. A thinner battle moves the front in proportion,
	// down to MinBattleWeight, so a 1v1 between whoever happened to be online
	// does not carry the war the way a full one does.
	FullStrengthPlayers int `json:"full_strength_players"`
	// MinBattleWeight floors that proportion. It is what keeps a four-person
	// evening able to move the war at all.
	MinBattleWeight float64 `json:"min_battle_weight"`
}

type TimingConfig struct {
	// HeartbeatInterval the client is told to use.
	HeartbeatInterval Duration `json:"heartbeat_interval"`
	// SessionTimeout after which a silent link is considered dead.
	SessionTimeout Duration `json:"session_timeout"`
	// HostBootDeadline is how long an assigned host has to report ready. It has
	// to cover a map load, the game server's Steam logon and its FakeIP
	// allocation, none of which the host controls — so it is deliberately
	// generous. The client runs the same number, taken from the assignment.
	HostBootDeadline Duration `json:"host_boot_deadline"`
	// ClientConnectDeadline is how long a client has to reach the host.
	ClientConnectDeadline Duration `json:"client_connect_deadline"`
	// MigrationHold is how long clients hold while a new host boots. It must be
	// at least HostBootDeadline: the replacement host is given that long to come
	// up, and giving up on it sooner sends everyone away from a battle that is
	// still on its way back.
	MigrationHold Duration `json:"migration_hold"`
	// ReconnectGrace is how long a slot is kept for a player who dropped out
	// of a live match.
	ReconnectGrace Duration `json:"reconnect_grace"`
	// TickInterval is the coordinator's own scheduling tick.
	TickInterval Duration `json:"tick_interval"`
	// HostAcceptDeadline is how long an elected P2P host has to call
	// client/host-register before the coordinator gives up on the offer and
	// puts the roster back in the queue.
	HostAcceptDeadline Duration `json:"host_accept_deadline"`
	// HostFailureCooldown is how long a player whose hosting attempt failed is
	// kept out of elections. Without it, one client that cannot host — a game
	// still loading, a machine that went away — wins the election again on
	// every tick, and its front does nothing but form and abort the same
	// battle while everyone else on it waits.
	HostFailureCooldown Duration `json:"host_failure_cooldown"`
}

type MatchConfig struct {
	// Sizes the coordinator is willing to form, smallest first. Each entry is
	// players per team.
	TeamSizes []int `json:"team_sizes"`
	// MinTeamSize below which no match is formed at all.
	MinTeamSize int `json:"min_team_size"`
	// FormWait is how long the queue waits for a bigger match before settling
	// for the smallest viable size.
	FormWait Duration `json:"form_wait"`
	// MaxConcurrentPerFront caps parallel battles on one front.
	MaxConcurrentPerFront int `json:"max_concurrent_per_front"`
	// AbandonBanDuration applied to a player who leaves a live match.
	AbandonBanDuration Duration `json:"abandon_ban_duration"`
	// MaxMigrations before the match is abandoned instead of migrated again.
	MaxMigrations int `json:"max_migrations"`
	// ResultQuorum is the fraction of non-host players that must corroborate
	// the host's reported result for it to count towards the war.
	ResultQuorum float64 `json:"result_quorum"`
	// WidenAfter is how long a queued player waits before the coordinator
	// starts favouring a front that already has people queued over one that
	// merely matches their preference. Zero disables widening.
	WidenAfter Duration `json:"widen_after"`
	// WidenStepBonus is how much score one WidenAfter-sized step of waiting
	// adds to a front that already has somebody queued on it.
	WidenStepBonus float64 `json:"widen_step_bonus"`
	// WidenMaxSteps caps how far widening can push the score.
	WidenMaxSteps int `json:"widen_max_steps"`
}

type ElectionConfig struct {
	WeightUpload      float64 `json:"weight_upload"`
	WeightLatency     float64 `json:"weight_latency"`
	WeightCPU         float64 `json:"weight_cpu"`
	WeightStability   float64 `json:"weight_stability"`
	DedicatedBonus    float64 `json:"dedicated_bonus"`
	PublicIPBonus     float64 `json:"public_ip_bonus"`
	AbandonPenalty    float64 `json:"abandon_penalty"`
	MinUploadKbps     uint32  `json:"min_upload_kbps"`
	MinCPUScore       uint32  `json:"min_cpu_score"`
	MinMemoryMB       uint32  `json:"min_memory_mb"`
	MaxAcceptableRTT  int     `json:"max_acceptable_rtt_ms"`
	RequireCanHostBit bool    `json:"require_can_host_bit"`
}

type SecurityConfig struct {
	// PolicyPath optionally overrides the built-in convar policy.
	PolicyPath string `json:"policy_path"`
	// IntegrityReportInterval expected from hosts. Missing reports are treated
	// as suspicious after IntegrityGrace consecutive misses.
	IntegrityReportInterval Duration `json:"integrity_report_interval"`
	IntegrityGrace          int      `json:"integrity_grace"`
	// FlagsBeforeAbort is how many non-fatal violations a match tolerates.
	FlagsBeforeAbort int `json:"flags_before_abort"`
	// KickOnClientViolation kicks the offending player rather than flagging.
	KickOnClientViolation bool `json:"kick_on_client_violation"`
	// ViolationBan applied to a player kicked for a policy violation.
	ViolationBan Duration `json:"violation_ban"`
	// RequireSignedResults rejects match results and integrity reports that are
	// not signed with the match secret. The signing half lives in the deferred
	// security layer, so this is off until that ships; with it off a host can
	// lie about a scoreline and only the clients' corroboration catches it.
	RequireSignedResults bool `json:"require_signed_results"`
	// Secret is the coordinator's own HMAC key, used to derive per-match
	// secrets. Prefer the GREYLINE_GC_SECRET env var.
	Secret string `json:"secret"`
}

// Duration is a time.Duration that (de)serializes as a Go duration string.
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n int64
		if err2 := json.Unmarshal(b, &n); err2 == nil {
			*d = Duration(time.Duration(n) * time.Second)
			return nil
		}
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("bad duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Default returns a configuration tuned for the MVP: one front, small
// population, listen-server hosts. The secret is deliberately left empty so
// Validate forces the operator to supply one.
func Default() *Config {
	return &Config{
		APIListen:       "0.0.0.0:27100",
		AdminListen:     "127.0.0.1:27101",
		TheaterPath:     "theater.industrial.json",
		WarLogPath:      "war-events.jsonl",
		ProtocolVersion: 2,
		Pool: PoolConfig{
			OfflineAfter:    Duration(45 * time.Second),
			OfflineAfterP2P: Duration(20 * time.Second),
			PollWait:        Duration(25 * time.Second),
		},
		War: WarConfig{
			PlayersPerFront:     16,
			MaxFronts:           4,
			MobilizationDeficit: 2,
			Intermission:        Duration(15 * time.Minute),
			CampaignName:        "THE SECOND GRAVEL WAR",
			AttackerTeam:        "blu",
			FullStrengthPlayers: 12,
			MinBattleWeight:     0.25,
		},
		Auth: AuthConfig{
			Mode:             AuthDev,
			AppID:            0,
			RejectVACBanned:  true,
			RequireOwnership: true,
			TicketCacheTTL:   Duration(10 * time.Minute),
		},
		Timing: TimingConfig{
			HeartbeatInterval:     Duration(10 * time.Second),
			SessionTimeout:        Duration(45 * time.Second),
			HostBootDeadline:      Duration(90 * time.Second),
			ClientConnectDeadline: Duration(90 * time.Second),
			MigrationHold:         Duration(105 * time.Second),
			ReconnectGrace:        Duration(120 * time.Second),
			TickInterval:          Duration(time.Second),
			HostAcceptDeadline:    Duration(20 * time.Second),
			HostFailureCooldown:   Duration(2 * time.Minute),
		},
		Match: MatchConfig{
			TeamSizes:             []int{2, 3, 4, 6},
			MinTeamSize:           2,
			FormWait:              Duration(30 * time.Second),
			MaxConcurrentPerFront: 2,
			AbandonBanDuration:    Duration(10 * time.Minute),
			MaxMigrations:         3,
			ResultQuorum:          0.5,
			WidenAfter:            Duration(30 * time.Second),
			WidenStepBonus:        0.4,
			WidenMaxSteps:         6,
		},
		Election: ElectionConfig{
			WeightUpload:      0.30,
			WeightLatency:     0.35,
			WeightCPU:         0.15,
			WeightStability:   0.20,
			DedicatedBonus:    0.50,
			PublicIPBonus:     0.10,
			AbandonPenalty:    0.35,
			MinUploadKbps:     1500,
			MinCPUScore:       40,
			MinMemoryMB:       2048,
			MaxAcceptableRTT:  180,
			RequireCanHostBit: true,
		},
		Security: SecurityConfig{
			IntegrityReportInterval: Duration(30 * time.Second),
			IntegrityGrace:          3,
			FlagsBeforeAbort:        3,
			KickOnClientViolation:   true,
			ViolationBan:            Duration(30 * time.Minute),
		},
	}
}

// Load reads a JSON config from path (empty path = built-in defaults only),
// fills unset fields from the defaults, then applies environment overrides.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}
	if v := os.Getenv("GREYLINE_LISTEN"); v != "" {
		cfg.APIListen = v
	}
	if v := os.Getenv("GREYLINE_POOL_KEY"); v != "" {
		cfg.Pool.Key = v
	}
	if v := os.Getenv("GREYLINE_ADMIN_KEY"); v != "" {
		cfg.Pool.AdminKey = v
	}
	if v := os.Getenv("GREYLINE_THEATER"); v != "" {
		cfg.TheaterPath = v
	}
	if v := os.Getenv("GREYLINE_WAR_LOG"); v != "" {
		cfg.WarLogPath = v
	}
	if v := os.Getenv("GREYLINE_ADMIN_LISTEN"); v != "" {
		cfg.AdminListen = v
	}
	if v := os.Getenv("GREYLINE_STEAM_WEBAPI_KEY"); v != "" {
		cfg.Auth.WebAPIKey = v
	}
	if v := os.Getenv("GREYLINE_GC_SECRET"); v != "" {
		cfg.Security.Secret = v
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

var (
	ErrNoSecret  = errors.New("security.secret is empty: set GREYLINE_GC_SECRET or security.secret in the config")
	ErrNoPoolKey = errors.New("pool.key is empty: set GREYLINE_POOL_KEY or pool.key in the config")
)

// Validate rejects configurations that would silently produce an insecure or
// non-functional coordinator.
func (c *Config) Validate() error {
	if c.APIListen == "" {
		return errors.New("api_listen address is empty")
	}
	if c.TheaterPath == "" {
		return errors.New("theater_path is empty: the war has no map to be fought on")
	}
	if c.WarLogPath == "" {
		return errors.New("war_log_path is empty: the war would not survive a restart")
	}
	if c.Pool.Key == "" {
		// Every machine holding this key can report results that move the war,
		// so an empty one would let anybody who can reach the port rewrite
		// history.
		return ErrNoPoolKey
	}
	if len(c.Match.TeamSizes) == 0 {
		return errors.New("match.team_sizes is empty")
	}
	for _, s := range c.Match.TeamSizes {
		if s <= 0 {
			return fmt.Errorf("match.team_sizes contains non-positive size %d", s)
		}
	}
	if c.Match.MinTeamSize <= 0 {
		return errors.New("match.min_team_size must be positive")
	}
	if c.Match.ResultQuorum < 0 || c.Match.ResultQuorum > 1 {
		return errors.New("match.result_quorum must be within [0,1]")
	}
	if c.Match.MaxMigrations < 0 {
		return errors.New("match.max_migrations must not be negative")
	}
	switch c.Auth.Mode {
	case AuthDev:
	case AuthWebAPI:
		if c.Auth.WebAPIKey == "" {
			return errors.New("auth.mode=webapi requires a publisher Web API key")
		}
		if c.Auth.AppID == 0 {
			return errors.New("auth.mode=webapi requires auth.app_id")
		}
	default:
		return fmt.Errorf("unknown auth.mode %q", c.Auth.Mode)
	}
	if c.Security.Secret == "" {
		return ErrNoSecret
	}
	if c.Timing.TickInterval <= 0 {
		return errors.New("timing.tick_interval must be positive")
	}
	if c.Timing.MigrationHold < c.Timing.HostBootDeadline {
		return fmt.Errorf("timing.migration_hold (%s) is shorter than timing.host_boot_deadline (%s): "+
			"players would be sent away from a battle whose new host is still booting",
			c.Timing.MigrationHold.D(), c.Timing.HostBootDeadline.D())
	}
	if c.Timing.HeartbeatInterval*2 > c.Timing.SessionTimeout {
		return fmt.Errorf("timing.session_timeout (%s) leaves no room for a missed heartbeat at %s: "+
			"links would be dropped and rebuilt in a loop",
			c.Timing.SessionTimeout.D(), c.Timing.HeartbeatInterval.D())
	}
	return nil
}

// SortedTeamSizes returns the configured team sizes in ascending order without
// mutating the config.
func (c *Config) SortedTeamSizes() []int {
	out := make([]int, len(c.Match.TeamSizes))
	copy(out, c.Match.TeamSizes)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
