// Package config loads and validates the GC's settings.
//
// Everything the GC decides is driven from this one file: which match groups
// exist and how small a match may be, which dedicated servers may connect,
// where on-demand servers come from, and who else hears about results. There
// are no hardcoded maps, groups or campaigns anywhere else.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/maps"
)

// Config is the whole GC.
type Config struct {
	// Name is what players see as the coordinator's name.
	Name string `json:"name"`
	// Listen is the HTTP address, e.g. ":27100".
	Listen string `json:"listen"`
	// DataDir holds the durable state: running matches, player records and
	// the war's log. Empty keeps everything in memory, which a restart loses.
	DataDir  string `json:"data_dir"`
	AdminKey string `json:"admin_key,omitempty"`

	Auth        AuthConfig         `json:"auth"`
	ServerKeys  []ServerKey        `json:"server_keys"`
	MatchGroups []MatchGroupConfig `json:"match_groups"`
	// Providers was serveme, which is gone: the GC only uses the servers
	// that connect to it. A config that still lists one is refused.
	Providers []json.RawMessage `json:"providers,omitempty"`
	Timing    TimingConfig      `json:"timing"`
	War       WarConfig         `json:"war"`
	Webhooks  []WebhookConfig   `json:"webhooks"`
	// MapIndex maps a map name to its def index in items_game's
	// master_maps_list. The casual panel sends the maps a party picked as a
	// bitfield over those indexes; with the table the GC honours the vote,
	// without it the GC picks from the group's pool on its own.
	MapIndex map[string]uint32 `json:"map_index,omitempty"`
	Chat     ChatConfig        `json:"chat"`
	Support  SupportConfig     `json:"support"`
	// AntiCheat is how the GC acts on Easy Anti-Cheat.
	AntiCheat AntiCheatConfig `json:"anticheat"`
	// Regions split matchmaking by where players are: each groups Steam's
	// relay data centers ("fra", "iad"...), which the game pings, with the
	// servers of that region (server_keys[].region, or the region a server
	// reports). Empty: one pool, any server.
	Regions []RegionConfig `json:"regions,omitempty"`
	// RegionWaitSecs is how long a party waits for a match in its own region
	// before it may be matched with anyone, on any server. Default 120.
	RegionWaitSecs int `json:"region_wait_secs,omitempty"`
}

// RegionConfig is one matchmaking region.
type RegionConfig struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	// Pops are Steam relay data center codes whose players belong here.
	Pops []string `json:"pops"`
}

// RegionWait is RegionWaitSecs with its default.
func (c *Config) RegionWait() time.Duration { return secs(c.RegionWaitSecs, 120) }

// AntiCheatConfig is how the GC acts on what Easy Anti-Cheat reports.
type AntiCheatConfig struct {
	// RequireProtectedServers hands matches only to servers that run an
	// Easy Anti-Cheat protected session (they say so in every heartbeat).
	RequireProtectedServers bool `json:"require_protected_servers"`
	// TempBanHours is how long an EAC temporary ban keeps a player out of
	// matchmaking. EAC does not say when its own ends. Default 24.
	TempBanHours int `json:"temp_ban_hours"`
	// SteamGameBans puts bans on players' Steam profiles.
	SteamGameBans SteamGameBansConfig `json:"steam_game_bans"`
}

// SteamGameBansConfig is how the GC issues Steam game bans
// (ICheatReportingService) -- the "1 game ban on record" on a profile. They
// need the publisher key (support.publisher_key / $STEAM_PUBLISHER_KEY) and
// VAC support enabled for the app in Steamworks.
type SteamGameBansConfig struct {
	// Mode: "off" (default); "manual": an admin bans through the admin API;
	// "auto": also every permanent Easy Anti-Cheat ban a server with
	// anticheat_authority reports. EOS issues those only for sanctions your
	// team activated in the EOS Developer Portal.
	Mode string `json:"mode"`
	// AppIDs to ban in. Default: auth.app_id and auth.app_ids.
	AppIDs []uint32 `json:"app_ids,omitempty"`
	// Immediate skips Valve's default ban delay. Leave it off: the delay
	// keeps cheat sellers from learning which session got an account caught.
	Immediate bool `json:"immediate"`
	// ForwardPlayerReports files every in-game "cheating" report with Steam
	// too (ReportPlayerCheating), where Steamworks lists them.
	ForwardPlayerReports bool `json:"forward_player_reports"`
}

// Enabled reports whether any Steam ban may be issued.
func (s SteamGameBansConfig) Enabled() bool { return s.Mode == "manual" || s.Mode == "auto" }

// Auto reports whether EAC's permanent bans become Steam bans by themselves.
func (s SteamGameBansConfig) Auto() bool { return s.Mode == "auto" }

// TempBan is TempBanHours as a duration.
func (a AntiCheatConfig) TempBan() time.Duration {
	if a.TempBanHours <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(a.TempBanHours) * time.Hour
}

// ChatConfig is the global, clan and party chat in the main menu.
type ChatConfig struct {
	Disabled bool `json:"disabled"`
	// History is how many lines each channel keeps for players who arrive
	// later. Default 60.
	History int `json:"history"`
	// MutedSteamIDs cannot write in chat.
	MutedSteamIDs []string `json:"muted_steam_ids"`
	// BannedWords are starred out (case-insensitive, whole substrings).
	BannedWords []string `json:"banned_words"`
}

// SupportConfig is the in-game "support the developers" tab: donations and a
// monthly subscription through Steam microtransactions (ISteamMicroTxn).
type SupportConfig struct {
	Enabled bool `json:"enabled"`
	// PublisherKey is a Steam publisher web API key with the microtransaction
	// permission. $STEAM_PUBLISHER_KEY overrides it. It never leaves the GC.
	PublisherKey string `json:"publisher_key"`
	// AppID the purchases are for; defaults to auth.app_id.
	AppID uint32 `json:"app_id"`
	// Sandbox uses ISteamMicroTxnSandbox: no money moves. Use it until the
	// store is live.
	Sandbox bool `json:"sandbox"`
	// Fake approves every purchase without Steam, for working on the screens.
	// Only allowed with auth.mode "dev".
	Fake bool `json:"fake"`
	// Prices by ISO 4217 currency, in the currency's cents. Missing ones use
	// the built-in table; currencies in neither are charged in USD (Steam
	// converts).
	Prices map[string]Prices `json:"prices,omitempty"`
}

// Prices are a currency's tiers: one-off donations and monthly subscriptions.
type Prices struct {
	Once    []int64 `json:"once"`
	Monthly []int64 `json:"monthly"`
}

// AuthConfig decides how much the GC believes a client.
type AuthConfig struct {
	// Mode is "dev" (take the client's word for its SteamID) or "webapi"
	// (verify a Steam web API ticket). Only "webapi" belongs in production.
	Mode          string   `json:"mode"`
	SteamAPIKey   string   `json:"steam_api_key"`
	PublisherAuth bool     `json:"publisher_auth"`
	AppID         uint32   `json:"app_id"`
	AppIDs        []uint32 `json:"app_ids"`
	// Identity is the string the game passes to GetAuthTicketForWebApi.
	Identity string `json:"identity"`
}

// AppIDList is every accepted app, AppID first, without duplicates or zeroes.
func (a AuthConfig) AppIDList() []uint32 {
	out := make([]uint32, 0, len(a.AppIDs)+1)
	seen := map[uint32]bool{}
	for _, id := range append([]uint32{a.AppID}, a.AppIDs...) {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// Verified reports whether identities are proven.
func (a AuthConfig) Verified() bool { return a.Mode == "webapi" }

// ServerKey lets dedicated servers connect. A community that runs servers for
// this GC gets its own key, so its servers can be told apart and cut off on
// their own.
type ServerKey struct {
	Name      string `json:"name"`
	Key       string `json:"key"`
	Community string `json:"community,omitempty"`
	Region    string `json:"region,omitempty"`
	// AntiCheatAuthority lets this key's servers sanction players: an EAC
	// ban they report keeps the player out of matchmaking. Give it only to
	// servers you run -- a community server could report anything.
	AntiCheatAuthority bool `json:"anticheat_authority,omitempty"`
}

// Mode is what the GC may do with a match once it has formed.
type Mode string

const (
	// ModeFrontline keeps taking players while the match runs.
	ModeFrontline Mode = "frontline"
	// ModeRanked forms a roster once and leaves it alone.
	ModeRanked Mode = "ranked"
)

// MatchGroupConfig is one queue.
type MatchGroupConfig struct {
	// MatchGroup is TF2's ETFMatchGroup: 7 is Casual 12v12, 2 is Ladder 6v6.
	MatchGroup int32  `json:"match_group"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Mode       Mode   `json:"mode"`

	MinPlayers   int `json:"min_players"`
	IdealPlayers int `json:"ideal_players"`
	MaxPlayers   int `json:"max_players"`
	// PatientSecs is how long the GC holds out for IdealPlayers before it
	// settles for MinPlayers.
	PatientSecs int `json:"patient_secs"`
	// BackfillSecs: 0 fills a frontline match for as long as it runs, a
	// positive value stops after that long, -1 never backfills.
	BackfillSecs int `json:"backfill_secs"`

	// Modes pulls in every stock map of those modes; Maps adds more.
	Modes []string `json:"modes,omitempty"`
	Maps  []string `json:"maps"`
	// ServerConfig is retained for existing configs. Pooled game servers
	// choose their own cfg based on MatchGroup and map.
	ServerConfig string `json:"server_config"`
	// Region, if set, only uses servers of that region.
	Region string `json:"region,omitempty"`

	Restrictions Restrictions `json:"restrictions"`
}

// EffectiveMode is Mode with its default.
func (g MatchGroupConfig) EffectiveMode() Mode {
	if g.Mode == "" {
		return ModeFrontline
	}
	return g.Mode
}

// EffectiveMaps is the map pool: the modes' stock maps plus Maps.
func (g MatchGroupConfig) EffectiveMaps() []string {
	out := maps.InModes(g.Modes...)
	seen := make(map[string]bool, len(out))
	for _, m := range out {
		seen[m] = true
	}
	for _, m := range g.Maps {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// TeamCap is the most players one side may have.
func (g MatchGroupConfig) TeamCap() int { return g.MaxPlayers / 2 }

// PartyCap is the biggest party that may queue.
func (g MatchGroupConfig) PartyCap() int {
	c := g.TeamCap()
	if n := g.Restrictions.MaxPartySize; n > 0 && n < c {
		return n
	}
	return c
}

// Backfills reports whether running matches of this group take new players.
func (g MatchGroupConfig) Backfills() bool {
	return g.EffectiveMode() == ModeFrontline && g.BackfillSecs >= 0
}

// Restrictions are who may queue for a group.
type Restrictions struct {
	RequireVerifiedAuth bool     `json:"require_verified_auth"`
	MaxPartySize        int      `json:"max_party_size"`
	MinPartySize        int      `json:"min_party_size"`
	MinMatchesPlayed    int      `json:"min_matches_played"`
	AbandonCooldownMins int      `json:"abandon_cooldown_mins"`
	MaxAbandons         int      `json:"max_abandons"`
	AllowedSteamIDs     []string `json:"allowed_steam_ids"`
	BannedSteamIDs      []string `json:"banned_steam_ids"`
}

// TimingConfig is every deadline the GC enforces. Each one exists so that no
// state can be stuck forever: everything either progresses or times out.
type TimingConfig struct {
	TickMillis int `json:"tick_millis"`
	// SessionTTLSecs: a peer that has not polled for this long is gone.
	SessionTTLSecs int `json:"session_ttl_secs"`
	// PollSecs is how long a long-poll waits for messages.
	PollSecs int `json:"poll_secs"`
	// ServerBootSecs is how long a formed match may wait for a server to be
	// ready before it is abandoned and its parties requeued.
	ServerBootSecs int `json:"server_boot_secs"`
	// ServerLostSecs is how long a running match survives its server's
	// session being gone.
	ServerLostSecs int `json:"server_lost_secs"`
	// NoShowSecs ends a running match nobody connected to.
	NoShowSecs int `json:"no_show_secs"`
	// MaxMatchSecs is a hard cap on a match's life.
	MaxMatchSecs int `json:"max_match_secs"`
	// OfflineGraceSecs is how long a queued party keeps its place after a
	// member's game went quiet.
	OfflineGraceSecs int `json:"offline_grace_secs"`
}

func secs(n, def int) time.Duration {
	if n <= 0 {
		n = def
	}
	return time.Duration(n) * time.Second
}

func (t TimingConfig) Tick() time.Duration {
	if t.TickMillis <= 0 {
		return time.Second
	}
	return time.Duration(t.TickMillis) * time.Millisecond
}
func (t TimingConfig) SessionTTL() time.Duration   { return secs(t.SessionTTLSecs, 60) }
func (t TimingConfig) Poll() time.Duration         { return secs(t.PollSecs, 25) }
func (t TimingConfig) ServerBoot() time.Duration   { return secs(t.ServerBootSecs, 300) }
func (t TimingConfig) ServerLost() time.Duration   { return secs(t.ServerLostSecs, 120) }
func (t TimingConfig) NoShow() time.Duration       { return secs(t.NoShowSecs, 300) }
func (t TimingConfig) MaxMatch() time.Duration     { return secs(t.MaxMatchSecs, 3*60*60) }
func (t TimingConfig) OfflineGrace() time.Duration { return secs(t.OfflineGraceSecs, 30) }

// WarConfig turns on the strategic layer.
type WarConfig struct {
	Enabled bool   `json:"enabled"`
	Theater string `json:"theater"`
	// MatchGroups fight the war. Empty means every frontline group.
	MatchGroups []int32 `json:"match_groups"`
}

// WebhookConfig tells another project about finished matches: a pickup site,
// a stats page, a Discord bot. Delivery is retried; a hook that is down never
// holds anything up.
type WebhookConfig struct {
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Secret string   `json:"secret"` // signs the body: X-Frontress-Signature
	Events []string `json:"events"` // "match_started", "match_ended"; empty = all
}

// Load reads and validates a config file. Environment variables override the
// few values that are usually secrets.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	c.applyEnv()
	return c, c.Validate()
}

func (c *Config) applyEnv() {
	if v := os.Getenv("GC_ADMIN_KEY"); v != "" {
		c.AdminKey = v
	}
	if v := os.Getenv("STEAM_API_KEY"); v != "" {
		c.Auth.SteamAPIKey = v
	}
	if v := os.Getenv("STEAM_PUBLISHER_KEY"); v != "" {
		c.Support.PublisherKey = v
	}
	if v := os.Getenv("GC_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("GC_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	// GC_SERVER_KEY adds one key, for the container that is the whole fleet.
	if v := os.Getenv("GC_SERVER_KEY"); v != "" {
		// The operator's own fleet: its anti-cheat verdicts are trusted.
		c.ServerKeys = append(c.ServerKeys, ServerKey{Name: "env", Key: v, AntiCheatAuthority: true})
	}
}

// Validate refuses a config that cannot work, at startup, rather than at the
// first queue that trips over it.
func (c *Config) Validate() error {
	if c.AdminKey != "" && len(c.AdminKey) < 32 {
		return errors.New("admin_key must have at least 32 characters")
	}
	if c.Listen == "" {
		c.Listen = ":27100"
	}
	if c.Name == "" {
		c.Name = "Team Frontress"
	}
	if c.Auth.Identity == "" {
		c.Auth.Identity = "frontress-gc"
	}
	switch c.Auth.Mode {
	case "", "dev":
		c.Auth.Mode = "dev"
	case "webapi":
		if c.Auth.SteamAPIKey == "" {
			return errors.New("auth: webapi mode needs steam_api_key (or STEAM_API_KEY)")
		}
		if len(c.Auth.AppIDList()) == 0 {
			return errors.New("auth: webapi mode needs app_id")
		}
	default:
		return fmt.Errorf("auth: unknown mode %q", c.Auth.Mode)
	}

	if c.Chat.History <= 0 {
		c.Chat.History = 60
	}
	if c.Support.Enabled {
		if !c.Support.Fake && c.DataDir == "" {
			return errors.New("support: persistent data_dir is required for Steam orders")
		}
		for currency, prices := range c.Support.Prices {
			if len(currency) != 3 {
				return fmt.Errorf("support: invalid currency %q", currency)
			}
			for _, amount := range append(append([]int64{}, prices.Once...), prices.Monthly...) {
				if amount <= 0 {
					return fmt.Errorf("support: %s prices must be positive", currency)
				}
			}
		}
		if c.Support.AppID == 0 {
			c.Support.AppID = c.Auth.AppID
		}
		switch {
		case c.Support.Fake && c.Auth.Mode != "dev":
			return errors.New("support: fake purchases are only allowed with auth.mode dev")
		case !c.Support.Fake && c.Support.PublisherKey == "":
			return errors.New("support: needs publisher_key (or STEAM_PUBLISHER_KEY), or fake for development")
		case !c.Support.Fake && c.Support.AppID == 0:
			return errors.New("support: needs app_id")
		}
	}

	keys := map[string]bool{}
	for i, k := range c.ServerKeys {
		if len(k.Key) < 16 {
			return fmt.Errorf("server_keys[%d] %q: key must be at least 16 characters", i, k.Name)
		}
		if keys[k.Key] {
			return fmt.Errorf("server_keys[%d] %q: key is used twice", i, k.Name)
		}
		keys[k.Key] = true
	}

	groups := map[int32]bool{}
	enabled := 0
	for i := range c.MatchGroups {
		g := &c.MatchGroups[i]
		if groups[g.MatchGroup] {
			return fmt.Errorf("match group %d is configured twice", g.MatchGroup)
		}
		groups[g.MatchGroup] = true
		if !g.Enabled {
			continue
		}
		enabled++
		if g.MinPlayers < 2 || g.MinPlayers%2 != 0 || g.MaxPlayers%2 != 0 || g.MaxPlayers < g.MinPlayers {
			return fmt.Errorf("match group %d: min_players and max_players must be even, min >= 2, max >= min", g.MatchGroup)
		}
		if g.IdealPlayers == 0 {
			g.IdealPlayers = g.MaxPlayers
		}
		if g.IdealPlayers < g.MinPlayers || g.IdealPlayers > g.MaxPlayers {
			return fmt.Errorf("match group %d: ideal_players must be between min and max", g.MatchGroup)
		}
		if len(g.EffectiveMaps()) == 0 {
			return fmt.Errorf("match group %d: no maps (set modes or maps)", g.MatchGroup)
		}
		if g.Restrictions.RequireVerifiedAuth && !c.Auth.Verified() {
			return fmt.Errorf("match group %d requires verified auth, but auth.mode is %q", g.MatchGroup, c.Auth.Mode)
		}
		for _, id := range append(append([]string{}, g.Restrictions.AllowedSteamIDs...), g.Restrictions.BannedSteamIDs...) {
			if _, err := strconv.ParseUint(id, 10, 64); err != nil {
				return fmt.Errorf("match group %d: %q is not a SteamID64", g.MatchGroup, id)
			}
		}
	}
	if enabled == 0 {
		return errors.New("no match group is enabled")
	}

	if len(c.Providers) > 0 {
		return errors.New("providers: on-demand servers (serveme) are gone; run servers that connect with a server key (server_keys) instead")
	}
	for i, w := range c.Webhooks {
		if w.URL == "" {
			return fmt.Errorf("webhooks[%d]: url is required", i)
		}
	}
	sb := &c.AntiCheat.SteamGameBans
	switch sb.Mode {
	case "", "off", "manual", "auto":
	default:
		return fmt.Errorf("anticheat.steam_game_bans.mode must be off, manual or auto, not %q", sb.Mode)
	}
	if sb.Enabled() || sb.ForwardPlayerReports {
		if c.Support.PublisherKey == "" {
			return errors.New("anticheat.steam_game_bans needs the publisher key (support.publisher_key or $STEAM_PUBLISHER_KEY)")
		}
		if len(sb.AppIDs) == 0 {
			sb.AppIDs = c.Auth.AppIDList()
		}
		if len(sb.AppIDs) == 0 {
			return errors.New("anticheat.steam_game_bans needs app_ids (or auth.app_id)")
		}
	}
	return nil
}

// Group finds an enabled match group.
func (c *Config) Group(g int32) (MatchGroupConfig, bool) {
	for _, mg := range c.MatchGroups {
		if mg.MatchGroup == g && mg.Enabled {
			return mg, true
		}
	}
	return MatchGroupConfig{}, false
}

// FightsWar reports whether matches of a group are battles of the war.
func (c *Config) FightsWar(g MatchGroupConfig) bool {
	if !c.War.Enabled {
		return false
	}
	if len(c.War.MatchGroups) == 0 {
		return g.EffectiveMode() == ModeFrontline
	}
	for _, id := range c.War.MatchGroups {
		if id == g.MatchGroup {
			return true
		}
	}
	return false
}
