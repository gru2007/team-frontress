package config

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"
)

// The admin panel (mm.team-frontress.org) edits part of the configuration
// while the GC runs. Those sections are kept in <data_dir>/settings.json and
// override the same sections of gc.json, which stays the place for what the
// GC needs before it can serve anyone: the listen address, the data
// directory, the admin key, Steam authentication, payments and the war.

// ManagedSections are the top-level keys the admin panel may change.
var ManagedSections = []string{
	"name", "match_groups", "server_keys", "regions", "region_wait_secs",
	"server_ping", "anticheat", "chat", "timing", "map_index", "webhooks",
}

// RestartOnly are settings a running GC cannot change: they size the loop,
// the sessions and the long-poll. A change is saved and takes effect on the
// next start.
var RestartOnly = []string{"timing.tick_millis", "timing.session_ttl_secs", "timing.poll_secs"}

// SettingsFile is what the admin panel saved.
type SettingsFile struct {
	Revision  int                        `json:"revision"`
	UpdatedAt time.Time                  `json:"updated_at"`
	UpdatedBy string                     `json:"updated_by,omitempty"`
	Note      string                     `json:"note,omitempty"`
	Sections  map[string]json.RawMessage `json:"sections"`
}

// SettingsPath is where the settings live for a data directory, or "".
func SettingsPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "settings.json")
}

// ReadSettings reads a settings file. A missing file is revision 0.
func ReadSettings(path string) (SettingsFile, error) {
	out := SettingsFile{Sections: map[string]json.RawMessage{}}
	if path == "" {
		return out, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("%s: %w", path, err)
	}
	if out.Sections == nil {
		out.Sections = map[string]json.RawMessage{}
	}
	return out, nil
}

// WriteSettings replaces the settings file atomically and appends the same
// record to settings-history.jsonl beside it.
func WriteSettings(path string, f SettingsFile) error {
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	line, err := json.Marshal(f)
	if err != nil {
		return err
	}
	h, err := os.OpenFile(historyPath(path), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer h.Close()
	_, err = h.Write(append(line, '\n'))
	return err
}

func historyPath(settingsPath string) string {
	return filepath.Join(filepath.Dir(settingsPath), "settings-history.jsonl")
}

// SettingsHistory is every saved revision, newest first.
func SettingsHistory(path string, limit int) ([]SettingsFile, error) {
	if path == "" {
		return []SettingsFile{}, nil
	}
	f, err := os.Open(historyPath(path))
	if errors.Is(err, os.ErrNotExist) {
		return []SettingsFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var all []SettingsFile
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var s SettingsFile
		if json.Unmarshal(sc.Bytes(), &s) == nil {
			all = append(all, s)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Revision > all[j].Revision })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (c *Config) section(key string) any {
	switch key {
	case "name":
		return &c.Name
	case "match_groups":
		return &c.MatchGroups
	case "server_keys":
		return &c.ServerKeys
	case "regions":
		return &c.Regions
	case "region_wait_secs":
		return &c.RegionWaitSecs
	case "server_ping":
		return &c.ServerPing
	case "anticheat":
		return &c.AntiCheat
	case "chat":
		return &c.Chat
	case "timing":
		return &c.Timing
	case "map_index":
		return &c.MapIndex
	case "webhooks":
		return &c.Webhooks
	}
	return nil
}

// Overlay replaces whole sections of c with the given JSON. An unknown
// section, or an unknown key inside one, is an error: a misspelt setting
// would otherwise do nothing.
func (c *Config) Overlay(sections map[string]json.RawMessage) error {
	for key, raw := range sections {
		p := c.section(key)
		if p == nil {
			return fmt.Errorf("settings: %q is not a setting the admin panel manages", key)
		}
		v := reflect.ValueOf(p).Elem()
		v.SetZero()
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(p); err != nil {
			return fmt.Errorf("settings: %s: %w", key, err)
		}
	}
	return nil
}

// Sections is the managed part of c as JSON. Server keys from the
// environment ($GC_SERVER_KEY) are left out: they are not the panel's.
func (c Config) Sections() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, key := range ManagedSections {
		var v any = c.section(key)
		if key == "server_keys" {
			own := []ServerKey{}
			for _, k := range c.ServerKeys {
				if k.Source != SourceEnv {
					own = append(own, k)
				}
			}
			v = own
		}
		raw, err := json.Marshal(v)
		if err == nil {
			out[key] = raw
		}
	}
	return out
}

// RestartPending lists the RestartOnly settings in which next differs from
// the running configuration.
func RestartPending(running, next Config) []string {
	var out []string
	if running.Timing.TickMillis != next.Timing.TickMillis {
		out = append(out, "timing.tick_millis")
	}
	if running.Timing.SessionTTLSecs != next.Timing.SessionTTLSecs {
		out = append(out, "timing.session_ttl_secs")
	}
	if running.Timing.PollSecs != next.Timing.PollSecs {
		out = append(out, "timing.poll_secs")
	}
	return out
}

// KeyID identifies a server key without revealing it.
func KeyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

// SourceEnv marks a server key that came from $GC_SERVER_KEY.
const SourceEnv = "env"

// LoadWithSettings is Load with the admin panel's settings applied over
// gc.json. It also returns what was read from settings.json.
func LoadWithSettings(path string) (Config, SettingsFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, SettingsFile{}, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, SettingsFile{}, fmt.Errorf("%s: %w", path, err)
	}
	dataDir := c.DataDir
	if v := os.Getenv("GC_DATA_DIR"); v != "" {
		dataDir = v
	}
	sf, err := ReadSettings(SettingsPath(dataDir))
	if err != nil {
		return Config{}, SettingsFile{}, err
	}
	if err := c.Overlay(sf.Sections); err != nil {
		return Config{}, SettingsFile{}, err
	}
	c.applyEnv()
	return c, sf, c.Validate()
}
