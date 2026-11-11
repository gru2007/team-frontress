package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsOverlayGCJSON(t *testing.T) {
	dir := t.TempDir()
	gcjson := `{"name":"from file","data_dir":` + strconvQuote(dir) + `,
		"server_keys":[{"name":"a","key":"aaaaaaaaaaaaaaaaaaaa"}],
		"match_groups":[{"match_group":7,"name":"Casual","enabled":true,"min_players":2,"max_players":4,"maps":["koth_viaduct"]}],
		"chat":{"history":10}}`
	path := filepath.Join(dir, "gc.json")
	if err := os.WriteFile(path, []byte(gcjson), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_SERVER_KEY", "envenvenvenvenvenvenv")

	c, sf, err := LoadWithSettings(path)
	if err != nil || sf.Revision != 0 || c.Name != "from file" || len(c.ServerKeys) != 2 {
		t.Fatalf("without settings: %+v %+v %v", c, sf, err)
	}
	own := c.Sections()
	var keys []ServerKey
	_ = json.Unmarshal(own["server_keys"], &keys)
	if len(keys) != 1 || keys[0].Name != "a" {
		t.Fatalf("the environment's key must not be the panel's: %s", own["server_keys"])
	}

	err = WriteSettings(SettingsPath(dir), SettingsFile{Revision: 3, Sections: map[string]json.RawMessage{
		"name": json.RawMessage(`"from panel"`),
		"chat": json.RawMessage(`{"banned_words":["bad"]}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	c, sf, err = LoadWithSettings(path)
	if err != nil || sf.Revision != 3 || c.Name != "from panel" || c.Chat.History != 60 || len(c.Chat.BannedWords) != 1 {
		t.Fatalf("with settings (a section is replaced whole): %+v %v", c.Chat, err)
	}
	if history, err := SettingsHistory(SettingsPath(dir), 0); err != nil || len(history) != 1 || history[0].Revision != 3 {
		t.Fatalf("history: %+v %v", history, err)
	}

	if err := c.Overlay(map[string]json.RawMessage{"listen": json.RawMessage(`":1"`)}); err == nil {
		t.Fatal("a bootstrap setting was accepted")
	}
	if err := c.Overlay(map[string]json.RawMessage{"chat": json.RawMessage(`{"histroy":5}`)}); err == nil || !strings.Contains(err.Error(), "histroy") {
		t.Fatal("a misspelt key was accepted:", err)
	}
}

func strconvQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
