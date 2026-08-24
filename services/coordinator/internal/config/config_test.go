package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

func validConfig() Config {
	c := Defaults()
	c.MatchGroups[0].Maps = []string{"koth_product_final"}
	c.Pool.Providers = []ProviderConfig{{
		Kind:    "static",
		Servers: []StaticServer{{Name: "a", Connect: "10.0.0.1:27015", RCON: "r"}},
	}}
	return c
}

func TestDefaultsPlusMapsAndAServerAreValid(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidationCatchesTheMistakesThatBiteLater(t *testing.T) {
	cases := []struct {
		name  string
		mutef func(*Config)
	}{
		{"no providers", func(c *Config) { c.Pool.Providers = nil }},
		{"no enabled groups", func(c *Config) { c.MatchGroups[0].Enabled = false }},
		{"odd min players", func(c *Config) { c.MatchGroups[0].MinPlayers = 5 }},
		{"min above ideal", func(c *Config) { c.MatchGroups[0].MinPlayers = 20 }},
		{"ideal above max", func(c *Config) { c.MatchGroups[0].IdealPlayers = 99 }},
		{"no maps and no war", func(c *Config) { c.MatchGroups[0].Maps = nil }},
		{"webapi without a key", func(c *Config) { c.Auth.Mode = "webapi"; c.Auth.SteamAPIKey = "" }},
		{"unknown auth mode", func(c *Config) { c.Auth.Mode = "trustme" }},
		{"unknown provider", func(c *Config) { c.Pool.Providers[0].Kind = "magic" }},
		{"registered without a secret", func(c *Config) {
			c.Pool.Providers = []ProviderConfig{{Kind: "registered"}}
			c.Secret = ""
		}},
		{"war without a theater", func(c *Config) { c.War.Enabled = true }},
		{"duplicate match group", func(c *Config) {
			c.MatchGroups = append(c.MatchGroups, c.MatchGroups[0])
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutef(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("this config was accepted and should not have been")
			}
		})
	}
}

func TestAMatchGroupWithNoMapsIsFineWhenTheWarPicksThem(t *testing.T) {
	c := validConfig()
	c.MatchGroups[0].Maps = nil
	c.War.Enabled = true
	c.War.TheaterFile = "theater.json"
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestLoadReadsOverTheDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coordinator.json")
	raw, err := json.Marshal(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	g, ok := c.Group(wire.MatchGroupCasual12v12)
	if !ok || !g.Enabled {
		t.Fatal("the casual group did not survive the round trip")
	}
	if g.MinPlayers != 4 {
		t.Fatalf("min players = %d, want the default 4", g.MinPlayers)
	}
}

func TestDurationsFallBackWhenUnset(t *testing.T) {
	var timing TimingConfig
	if timing.Tick() <= 0 || timing.TicketTTL() <= 0 || timing.AssignmentTTL() <= 0 {
		t.Fatal("an unset timing block produced a zero duration, which would spin")
	}
	var p PoolConfig
	if p.BootDeadline() <= 0 || p.IdleEnd() <= 0 || p.MaxMatch() <= 0 {
		t.Fatal("an unset pool block produced a zero duration")
	}
}

func TestRestrictionsMustBeSatisfiable(t *testing.T) {
	base := func() Config {
		c := Defaults()
		c.Secret = "s"
		c.MatchGroups = []MatchGroupConfig{{
			MatchGroup: 2, Name: "Ranked", Enabled: true,
			MinPlayers: 12, IdealPlayers: 12, MaxPlayers: 12,
			Maps: []string{"cp_process_final"},
		}}
		c.Pool.Providers = []ProviderConfig{{Kind: "static", Servers: []StaticServer{{Connect: "1.2.3.4:27015"}}}}
		return c
	}

	// A party cap bigger than a team can never be met.
	c := base()
	c.MatchGroups[0].Restrictions = Restrictions{MaxPartySize: 9}
	if err := c.Validate(); err == nil {
		t.Error("a max_party_size larger than a team was accepted")
	}

	// Verified identities need a coordinator that can check them.
	c = base()
	c.Auth = AuthConfig{Mode: "dev"}
	c.MatchGroups[0].Restrictions = Restrictions{RequireVerifiedAuth: true}
	if err := c.Validate(); err == nil {
		t.Error("require_verified_auth was accepted with auth.mode dev, which cannot verify anything")
	}

	c = base()
	c.Auth = AuthConfig{Mode: "webapi", SteamAPIKey: "k", AppID: 5147520}
	c.MatchGroups[0].Restrictions = Restrictions{RequireVerifiedAuth: true, MaxPartySize: 3}
	if err := c.Validate(); err != nil {
		t.Errorf("a satisfiable ranked config was refused: %v", err)
	}

	// A typo in a SteamID silently bans nobody, so it is refused instead.
	c = base()
	c.MatchGroups[0].Restrictions = Restrictions{BannedSteamIDs: []wire.SteamID{"7656119800000000"}}
	if err := c.Validate(); err == nil {
		t.Error("a malformed SteamID in a ban list was accepted")
	}
}

func TestPartyCapIsTheStricterOfTheTwo(t *testing.T) {
	g := MatchGroupConfig{MaxPlayers: 12}
	if got := g.PartyCap(); got != 6 {
		t.Errorf("PartyCap = %d, want half a match when nothing restricts it", got)
	}
	g.Restrictions.MaxPartySize = 3
	if got := g.PartyCap(); got != 3 {
		t.Errorf("PartyCap = %d, want the restriction", got)
	}
	g.Restrictions.MaxPartySize = 6
	if got := g.PartyCap(); got != 6 {
		t.Errorf("PartyCap = %d", got)
	}
}

func TestSearchTTLIsShorterThanAnAssignmentsTTL(t *testing.T) {
	cfg := Defaults()

	search := cfg.Timing.SearchTTL()
	if search >= cfg.Timing.TicketTTL() {
		t.Fatalf("search ttl %s is not shorter than the ticket ttl %s: a client that quit "+
			"keeps its place in queue for as long as one that is loading a map",
			search, cfg.Timing.TicketTTL())
	}
	if search < 15*time.Second {
		t.Fatalf("search ttl = %s, want at least 15s so one slow round-trip cannot drop a player", search)
	}
}

func TestSearchTTLNeverOutlivesAConfiguredTicketTTL(t *testing.T) {
	cfg := Defaults()
	cfg.Timing.TicketTTLSecs = 5

	if got := cfg.Timing.SearchTTL(); got != 5*time.Second {
		t.Fatalf("search ttl = %s, want 5s: an operator who asked for a short ttl gets it", got)
	}
}
