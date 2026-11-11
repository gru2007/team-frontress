package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/httpapi"
)

func TestAdminPanelChangesSettingsAndServerKeys(t *testing.T) {
	cfg := testConfig()
	cfg.AdminKey = strings.Repeat("k", 32)
	cfg.DataDir = t.TempDir()
	_, base, stop := startGCWith(t, cfg, nil)
	defer stop()

	do := func(method, path, body string, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+cfg.AdminKey)
		req.Header.Set("X-Frontress-Actor", "76561197960265729")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	var got struct {
		Settings struct {
			Revision  int                        `json:"revision"`
			Persisted bool                       `json:"persisted"`
			FromPanel []string                   `json:"from_panel"`
			Sections  map[string]json.RawMessage `json:"sections"`
		} `json:"settings"`
		Catalog struct {
			Modes map[string][]string `json:"modes"`
		} `json:"catalog"`
	}
	if code := do("GET", "/admin/v1/settings", "", &got); code != 200 || got.Settings.Revision != 0 ||
		!got.Settings.Persisted || len(got.Catalog.Modes["koth"]) == 0 || got.Settings.Sections["match_groups"] == nil {
		t.Fatalf("settings: %d %+v", code, got)
	}

	if code := do("PUT", "/admin/v1/settings", `{"revision":1,"sections":{"name":"x"}}`, nil); code != 409 {
		t.Fatal("a stale revision was saved", code)
	}
	if code := do("PUT", "/admin/v1/settings", `{"revision":0,"sections":{"match_groups":[{"match_group":7,"enabled":true,"min_players":3,"max_players":4,"maps":["koth_viaduct"]}]}}`, nil); code != 400 {
		t.Fatal("an invalid match group was saved", code)
	}
	if code := do("PUT", "/admin/v1/settings", `{"revision":0,"sections":{"server_keys":[]}}`, nil); code != 400 {
		t.Fatal("server keys must go through their own endpoint", code)
	}
	if code := do("PUT", "/admin/v1/settings", `{"revision":0,"note":"rename","sections":{"name":"Frontress EU","timing":{"tick_millis":20,"poll_secs":5}}}`, &got); code != 200 ||
		got.Settings.Revision != 1 || len(got.Settings.FromPanel) != 2 {
		t.Fatalf("save: %d %+v", code, got)
	}
	var status struct {
		Name string `json:"name"`
	}
	resp, _ := http.Get(base + "/v1/status")
	_ = json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for status.Name != "Frontress EU" && time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
		resp, _ = http.Get(base + "/v1/status")
		_ = json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()
	}
	if status.Name != "Frontress EU" {
		t.Fatal("the new name was not applied:", status.Name)
	}
	var restart struct {
		Settings struct {
			RestartPending []string `json:"restart_pending"`
		} `json:"settings"`
	}
	do("GET", "/admin/v1/settings", "", &restart)
	if len(restart.Settings.RestartPending) != 1 || restart.Settings.RestartPending[0] != "timing.poll_secs" {
		t.Fatalf("poll_secs needs a restart: %+v", restart)
	}

	// A new server key: shown once, and a server can join with it at once.
	var created struct {
		Key string `json:"key"`
	}
	if code := do("POST", "/admin/v1/server-keys", `{"action":"create","name":"eu-02","region":"eu","anticheat_authority":true}`, &created); code != 200 || len(created.Key) < 32 {
		t.Fatalf("create key: %d %+v", code, created)
	}
	if code := do("POST", "/admin/v1/server-keys", `{"action":"create","name":"eu-02"}`, nil); code != 400 {
		t.Fatal("a duplicate key name", code)
	}
	gs := newPeer(t, base, gsID)
	gs.connect(url.Values{"kind": {"server"}, "key": {created.Key}, "connect": {"127.0.0.1:27016"}})
	gs.send(msg(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus), &gcpb.CMsgGameServerMatchmakingStatus{
		MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_EMPTY.Enum(),
	})
	var keys struct {
		Keys []struct {
			Name    string `json:"name"`
			KeyID   string `json:"key_id"`
			Source  string `json:"source"`
			Servers []struct {
				State string `json:"state"`
			} `json:"servers"`
		} `json:"keys"`
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		do("GET", "/admin/v1/server-keys", "", &keys)
		if len(keys.Keys) == 2 && len(keys.Keys[1].Servers) == 1 && keys.Keys[1].Servers[0].State == "free" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server keys: %+v", keys)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if keys.Keys[1].Name != "eu-02" || keys.Keys[1].Source != "panel" || strings.Contains(keys.Keys[1].KeyID, created.Key) {
		t.Fatalf("key view: %+v", keys.Keys[1])
	}

	// Revoking it disconnects the server and refuses it next time.
	if code := do("POST", "/admin/v1/server-keys", `{"action":"revoke","name":"eu-02"}`, nil); code != 200 {
		t.Fatal("revoke", code)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		var live struct {
			Servers []struct {
				State string `json:"state"`
			} `json:"servers"`
		}
		do("GET", "/admin/v1/live", "", &live)
		if len(live.Servers) == 0 || live.Servers[0].State == "offline" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the revoked server is still in the pool: %+v", live)
		}
		time.Sleep(30 * time.Millisecond)
	}
	form := url.Values{"kind": {"server"}, "key": {created.Key}, "protocol": {strconv.Itoa(httpapi.ProtocolVersion)}, "steam_id": {"90000000000000002"}}
	resp, err := http.PostForm(base+"/gc/v1/session", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("a revoked key was accepted", resp.StatusCode)
	}

	raw, err := os.ReadFile(filepath.Join(cfg.DataDir, "settings.json"))
	if err != nil || !strings.Contains(string(raw), `"revision": 3`) || strings.Contains(string(raw), created.Key) {
		t.Fatalf("settings.json: %s %v", raw, err)
	}
	var history struct {
		Revisions []struct {
			Revision  int    `json:"revision"`
			UpdatedBy string `json:"updated_by"`
		} `json:"revisions"`
	}
	if code := do("GET", "/admin/v1/settings/history", "", &history); code != 200 || len(history.Revisions) != 3 ||
		history.Revisions[0].Revision != 3 || history.Revisions[0].UpdatedBy != "76561197960265729" {
		t.Fatalf("history: %d %+v", code, history)
	}
}
