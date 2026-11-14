package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestClanModerationHTTPAndGameEnforcement(t *testing.T) {
	cfg := testConfig()
	cfg.AdminKey = strings.Repeat("k", 32)
	_, base, stop := startGCWith(t, cfg, nil)
	defer stop()
	do := func(method, path, body, actor string, auth bool, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer "+cfg.AdminKey)
		}
		req.Header.Set("X-Frontress-Actor", actor)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil {
			if err := json.NewDecoder(res.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
		return res.StatusCode
	}
	actor := strconv.FormatUint(carol, 10)
	playerPath := "/admin/v1/players/" + strconv.FormatUint(alice, 10) + "/moderation"
	if got := do("GET", "/admin/v1/clans", "", actor, false, nil); got != 401 {
		t.Fatal(got)
	}
	if got := do("POST", playerPath, `{"action":"restrict_creation","reason":"x"}`, actor, false, nil); got != 401 {
		t.Fatal(got)
	}
	for _, body := range []string{`{"action":"nuke","reason":"x"}`, `{"action":"restrict_creation","reason":" "}`, `{"action":"restrict_creation","reason":"x","actor":"spoof"}`, `{"action":"mute","reason":"x"} {}`} {
		if got := do("POST", playerPath, body, actor, true, nil); got != 400 {
			t.Fatalf("accepted %s: %d", body, got)
		}
	}
	if got := do("POST", playerPath, `{"action":"restrict_creation","reason":"x"}`, "", true, nil); got != 400 {
		t.Fatal("actor not required", got)
	}
	a, b := newPeer(t, base, alice), newPeer(t, base, bob)
	a.client()
	b.client()
	a.op(map[string]any{"op": "clan_create", "name": "Remove this", "tag": "BAD", "open": true})
	a.until("clan created", func() bool { return a.last("clan") != nil && a.last("clan")["clan"] != nil })
	id := a.last("clan")["clan"].(map[string]any)["id"].(string)
	b.op(map[string]any{"op": "clan_join", "id": id})
	b.until("clan joined", func() bool { return b.last("clan") != nil && b.last("clan")["clan"] != nil })
	a.op(map[string]any{"op": "say", "ch": "global", "text": "retain the message without the tag"})
	b.until("tagged message", func() bool { return has(b.chatText("global"), "retain the message without the tag") })
	clanPath := "/admin/v1/clans/" + id
	if got := do("POST", clanPath, `{"action":"ban","reason":"prohibited symbols","restrict_members":true}`, actor, true, nil); got != 200 {
		t.Fatal("ban", got)
	}
	b.until("banned identity removed from chat", func() bool {
		ev := b.last("chat")
		if ev == nil || ev["reset"] != true {
			return false
		}
		retained := false
		for _, item := range ev["lines"].([]any) {
			line := item.(map[string]any)
			if line["tag"] == "BAD" || line["key"] == "fui_feed_clan_new" {
				return false
			}
			if line["text"] == "retain the message without the tag" {
				retained = true
			}
		}
		return retained
	})
	for _, p := range []*peer{a, b} {
		p.until("clan removed", func() bool { return p.last("clan")["clan"] == nil })
		p.op(map[string]any{"op": "clan_create", "name": "Replacement", "tag": "NEW"})
		p.until("creation rejected", func() bool { return p.last("error") != nil && p.last("error")["key"] == "fui_err_clan_creation_banned" })
	}
	var public struct {
		Clans []any `json:"clans"`
	}
	if got := do("GET", "/v1/clans", "", "", false, &public); got != 200 || len(public.Clans) != 0 {
		t.Fatal("deleted clan still public", got, public)
	}
	var admin struct {
		Moderation struct {
			CreationBans []any `json:"creation_bans"`
			History      []any `json:"history"`
		} `json:"moderation"`
	}
	if got := do("GET", "/admin/v1/clans", "", actor, true, &admin); got != 200 || len(admin.Moderation.CreationBans) != 2 || len(admin.Moderation.History) != 1 {
		t.Fatal("audit missing", got, admin)
	}
	if got := do("POST", playerPath, `{"action":"allow_creation","reason":"appeal"}`, actor, true, nil); got != 200 {
		t.Fatal(got)
	}
	a.op(map[string]any{"op": "clan_create", "name": "Remove this", "tag": "OK"})
	a.until("identity rejected", func() bool { return a.last("error")["key"] == "fui_err_clan_identity_banned" })
	if got := do("POST", clanPath, `{"action":"unban","reason":"appeal"}`, actor, true, nil); got != 200 {
		t.Fatal(got)
	}
	a.op(map[string]any{"op": "clan_create", "name": "Remove this", "tag": "BAD"})
	a.until("creation allowed again", func() bool { return a.last("clan")["clan"] != nil })
	if got := do("POST", playerPath, `{"action":"mute","reason":"abuse"}`, actor, true, nil); got != 200 {
		t.Fatal(got)
	}
	a.op(map[string]any{"op": "say", "ch": "global", "text": "blocked"})
	a.until("muted", func() bool { return a.last("error")["key"] == "fui_err_muted" })
	if got := do("POST", playerPath, `{"action":"unmute","reason":"appeal"}`, actor, true, nil); got != 200 {
		t.Fatal(got)
	}
	a.op(map[string]any{"op": "say", "ch": "global", "text": "allowed"})
	b.until("chat allowed again", func() bool { return has(b.chatText("global"), "allowed") })
	if got := do("POST", playerPath, `{"action":"clear_chat","reason":"remove abuse"}`, actor, true, nil); got != 200 {
		t.Fatal(got)
	}
	b.until("chat reset after deletion", func() bool {
		ev := b.last("chat")
		if ev == nil || ev["reset"] != true {
			return false
		}
		for _, item := range ev["lines"].([]any) {
			if item.(map[string]any)["from"] == strconv.FormatUint(alice, 10) {
				return false
			}
		}
		return true
	})
}
