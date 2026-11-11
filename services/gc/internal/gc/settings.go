package gc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/steamban"
)

// The admin panel changes the configuration while the GC runs. A save is
// validated as a whole, written to <data_dir>/settings.json and applied on
// the loop; matches already running keep the settings they formed with.

// SettingsView is what the admin panel edits.
type SettingsView struct {
	Revision  int                        `json:"revision"`
	UpdatedAt time.Time                  `json:"updated_at,omitzero"`
	UpdatedBy string                     `json:"updated_by,omitempty"`
	Persisted bool                       `json:"persisted"`  // false: no data_dir, a restart forgets
	FromPanel []string                   `json:"from_panel"` // sections settings.json overrides
	Sections  map[string]json.RawMessage `json:"sections"`
	// RestartPending are saved settings the running GC cannot apply.
	RestartPending []string  `json:"restart_pending"`
	Bootstrap      Bootstrap `json:"bootstrap"`
}

// Bootstrap is the part of gc.json the panel shows but does not change.
type Bootstrap struct {
	Listen          string   `json:"listen"`
	DataDir         string   `json:"data_dir"`
	AuthMode        string   `json:"auth_mode"`
	AppIDs          []uint32 `json:"app_ids"`
	SupportEnabled  bool     `json:"support_enabled"`
	SupportSandbox  bool     `json:"support_sandbox"`
	WarEnabled      bool     `json:"war_enabled"`
	WarTheater      string   `json:"war_theater,omitempty"`
	PublisherKeySet bool     `json:"publisher_key_set"`
}

// ErrStaleSettings: somebody saved since the admin loaded the page.
var ErrStaleSettings = errors.New("the settings changed since you opened them; reload and try again")

// Settings is the current configuration as the panel sees it. Run on the loop.
func (g *GC) Settings() SettingsView {
	v := SettingsView{
		Revision:       g.settings.Revision,
		UpdatedAt:      g.settings.UpdatedAt,
		UpdatedBy:      g.settings.UpdatedBy,
		Persisted:      g.cfg.DataDir != "",
		FromPanel:      []string{},
		Sections:       g.cfg.Sections(),
		RestartPending: config.RestartPending(g.started, g.cfg),
		Bootstrap: Bootstrap{
			Listen: g.cfg.Listen, DataDir: g.cfg.DataDir, AuthMode: g.cfg.Auth.Mode, AppIDs: g.cfg.Auth.AppIDList(),
			SupportEnabled: g.cfg.Support.Enabled, SupportSandbox: g.cfg.Support.Sandbox,
			WarEnabled: g.cfg.War.Enabled, WarTheater: g.cfg.War.Theater,
			PublisherKeySet: g.cfg.Support.PublisherKey != "",
		},
	}
	for _, key := range config.ManagedSections {
		if _, ok := g.settings.Sections[key]; ok {
			v.FromPanel = append(v.FromPanel, key)
		}
	}
	if v.RestartPending == nil {
		v.RestartPending = []string{}
	}
	return v
}

// SaveSettings replaces the given sections. revision must be the one the
// admin edited. Run on the loop.
func (g *GC) SaveSettings(revision int, sections map[string]json.RawMessage, by, note string) (SettingsView, error) {
	if revision != g.settings.Revision {
		return SettingsView{}, ErrStaleSettings
	}
	if len(sections) == 0 {
		return SettingsView{}, errors.New("nothing to save")
	}
	if _, ok := sections["server_keys"]; ok {
		return SettingsView{}, errors.New("server keys are changed through /admin/v1/server-keys")
	}
	return g.commitSettings(sections, by, note)
}

func (g *GC) commitSettings(sections map[string]json.RawMessage, by, note string) (SettingsView, error) {
	next := g.cfg
	if err := next.Overlay(sections); err != nil {
		return SettingsView{}, err
	}
	// Keys from the environment are kept whatever the panel saves.
	if _, ok := sections["server_keys"]; ok {
		for _, k := range g.cfg.ServerKeys {
			if k.Source == config.SourceEnv {
				next.ServerKeys = append(next.ServerKeys, k)
			}
		}
	}
	if err := next.Validate(); err != nil {
		return SettingsView{}, err
	}

	saved := config.SettingsFile{
		Revision: g.settings.Revision + 1, UpdatedAt: g.now(), UpdatedBy: by, Note: note,
		Sections: map[string]json.RawMessage{},
	}
	for k, v := range g.settings.Sections {
		saved.Sections[k] = v
	}
	own := next.Sections()
	for key := range sections {
		saved.Sections[key] = own[key]
	}
	if err := config.WriteSettings(config.SettingsPath(g.cfg.DataDir), saved); err != nil {
		return SettingsView{}, fmt.Errorf("could not save settings: %w", err)
	}
	g.settings = saved
	g.applyConfig(next)
	keys := make([]string, 0, len(sections))
	for k := range sections {
		keys = append(keys, k)
	}
	g.log.Info("settings changed", "revision", saved.Revision, "by", by, "sections", strings.Join(keys, ","), "note", note)
	return g.Settings(), nil
}

// applyConfig makes next the running configuration. Run on the loop.
func (g *GC) applyConfig(next config.Config) {
	g.cfg = next
	live := next
	g.live.Store(&live)
	g.hooks.SetHooks(next.Webhooks)

	if sb := next.AntiCheat.SteamGameBans; g.steamBans == nil && (sb.Enabled() || sb.ForwardPlayerReports) &&
		next.Support.PublisherKey != "" {
		g.steamBans = &steamban.Client{Key: next.Support.PublisherKey}
	}

	// A queue that is gone or switched off lets its parties go.
	for _, p := range g.parties {
		changed := false
		for group := range p.Queues {
			if _, ok := next.Group(group); !ok {
				delete(p.Queues, group)
				changed = true
			}
		}
		if changed {
			for _, member := range p.Members {
				g.notice(member, "This queue was closed; the search stopped.")
			}
			g.publishParty(p)
		}
	}

	// A server whose key was revoked or rotated is let go. Its match, if it
	// has one, ends on the usual server-lost deadline.
	valid := map[string]bool{}
	for _, k := range next.ServerKeys {
		valid[config.KeyID(k.Key)] = true
	}
	for _, srv := range g.servers {
		if s := srv.Session; s != nil && !s.Closed() && s.KeyID != "" && !valid[s.KeyID] {
			g.log.Info("server key revoked: server disconnected", "server", srv.SteamID, "name", srv.Name)
			g.sessions.End(s)
		}
	}
	g.publishStatus()
}

// ServerKeyView is a server key as the panel lists it: never the key itself.
type ServerKeyView struct {
	Name               string       `json:"name"`
	KeyID              string       `json:"key_id"`
	Community          string       `json:"community,omitempty"`
	Region             string       `json:"region,omitempty"`
	AntiCheatAuthority bool         `json:"anticheat_authority"`
	Source             string       `json:"source"` // panel, gc.json or env
	Servers            []LiveServer `json:"servers"`
}

// ServerKeys lists the keys with the servers connected with each. Run on the loop.
func (g *GC) ServerKeys() []ServerKeyView {
	live := g.Live()
	_, fromPanel := g.settings.Sections["server_keys"]
	out := []ServerKeyView{}
	for _, k := range g.cfg.ServerKeys {
		v := ServerKeyView{Name: k.Name, KeyID: config.KeyID(k.Key), Community: k.Community, Region: k.Region,
			AntiCheatAuthority: k.AntiCheatAuthority, Source: "gc.json", Servers: []LiveServer{}}
		switch {
		case k.Source == config.SourceEnv:
			v.Source = "env"
		case fromPanel:
			v.Source = "panel"
		}
		for _, s := range live.Servers {
			if s.KeyID == v.KeyID {
				v.Servers = append(v.Servers, s)
			}
		}
		out = append(out, v)
	}
	return out
}

// ServerKeyChange is what the panel asks of a key.
type ServerKeyChange struct {
	Action             string `json:"action"` // create, update, rotate, revoke
	Name               string `json:"name"`
	Community          string `json:"community"`
	Region             string `json:"region"`
	AntiCheatAuthority bool   `json:"anticheat_authority"`
}

// ChangeServerKey creates, edits, rotates or revokes a key. A new key (create,
// rotate) is returned once and never shown again. Run on the loop.
func (g *GC) ChangeServerKey(change ServerKeyChange, by string) (string, error) {
	change.Name = strings.TrimSpace(change.Name)
	if change.Name == "" || len(change.Name) > 64 {
		return "", errors.New("a key needs a name (at most 64 characters)")
	}
	keys := []config.ServerKey{}
	idx := -1
	for _, k := range g.cfg.ServerKeys {
		if k.Source == config.SourceEnv {
			if k.Name == change.Name {
				return "", errors.New("this key comes from $GC_SERVER_KEY; change it where the GC is deployed")
			}
			continue
		}
		if k.Name == change.Name {
			idx = len(keys)
		}
		keys = append(keys, k)
	}
	secret := ""
	switch change.Action {
	case "create":
		if idx >= 0 {
			return "", errors.New("a key with this name exists")
		}
		secret = newServerKey()
		keys = append(keys, config.ServerKey{Name: change.Name, Key: secret, Community: change.Community,
			Region: change.Region, AntiCheatAuthority: change.AntiCheatAuthority})
	case "update", "rotate", "revoke":
		if idx < 0 {
			return "", errors.New("no such key")
		}
		switch change.Action {
		case "update":
			keys[idx].Community, keys[idx].Region, keys[idx].AntiCheatAuthority =
				change.Community, change.Region, change.AntiCheatAuthority
		case "rotate":
			secret = newServerKey()
			keys[idx].Key = secret
		case "revoke":
			keys = append(keys[:idx], keys[idx+1:]...)
		}
	default:
		return "", errors.New("action must be create, update, rotate or revoke")
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		return "", err
	}
	if _, err := g.commitSettings(map[string]json.RawMessage{"server_keys": raw}, by, "server key "+change.Name+": "+change.Action); err != nil {
		return "", err
	}
	return secret, nil
}

func newServerKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
