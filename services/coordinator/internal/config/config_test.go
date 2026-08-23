package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
