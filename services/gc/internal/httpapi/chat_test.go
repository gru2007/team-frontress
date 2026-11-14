package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
)

func TestChatModerationAPIAndTagRefresh(t *testing.T) {
	cfg := testConfig()
	cfg.AdminKey = strings.Repeat("k", 32)
	_, base, stop := startGCWith(t, cfg, nil)
	defer stop()
	do := func(method, path, body string, auth bool, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer "+cfg.AdminKey)
		}
		req.Header.Set("X-Frontress-Actor", strconv.FormatUint(carol, 10))
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
	a := newPeer(t, base, alice)
	a.client()
	a.op(map[string]any{"op": "say", "ch": "global", "text": "keep menu message"})
	a.until("menu line", func() bool { return has(a.chatText("global"), "keep menu message") })
	server := newPeer(t, base, gsID)
	server.server()
	server.send(51030, &gcpb.CMsgPartyChatMsg{ActorId: proto.Uint64(alice), Msg: proto.String("team\nAlice\nprivate match message")})
	if code := do("GET", "/admin/v1/chat", "", false, nil); code != 401 {
		t.Fatal("chat exposed without key", code)
	}
	for _, query := range []string{"source=unknown", "steam_id=1", "before=wrong", "limit=999"} {
		if code := do("GET", "/admin/v1/chat?"+query, "", true, nil); code != 400 {
			t.Fatal("bad filter", query, code)
		}
	}
	var list struct {
		Epoch string `json:"epoch"`
		Lines []struct {
			ID     uint64 `json:"id"`
			Text   string `json:"text"`
			Source string `json:"source"`
		} `json:"lines"`
	}
	if code := do("GET", "/admin/v1/chat?source=match", "", true, &list); code != 200 || len(list.Lines) != 1 || list.Lines[0].Text != "private match message" {
		t.Fatal("match chat missing", code, list)
	}
	path := fmt.Sprintf("/admin/v1/chat/%d/delete", list.Lines[0].ID)
	if code := do("POST", path, `{"reason":"abuse","epoch":"old"}`, true, nil); code != 409 {
		t.Fatal("stale deletion accepted", code)
	}
	body := fmt.Sprintf(`{"reason":"abuse","epoch":%q}`, list.Epoch)
	if code := do("POST", path, body, false, nil); code != 401 {
		t.Fatal("unauthorized deletion", code)
	}
	if code := do("POST", path, body, true, nil); code != 200 {
		t.Fatal("delete failed", code)
	}
	if code := do("POST", path, body, true, nil); code != 404 {
		t.Fatal("deleted message still present", code)
	}
	a.op(map[string]any{"op": "clan_create", "name": "Preserve clan", "tag": "OLD", "open": true})
	a.until("clan", func() bool { return a.last("clan") != nil && a.last("clan")["clan"] != nil })
	id := a.last("clan")["clan"].(map[string]any)["id"].(string)
	if code := do("POST", "/admin/v1/clans/"+id, `{"action":"set_tag","tag":"new","reason":"inappropriate tag"}`, true, nil); code != 200 {
		t.Fatal("tag change failed", code)
	}
	a.until("new tag", func() bool { return a.last("clan")["clan"].(map[string]any)["tag"] == "NEW" })
	if got := a.last("clan")["clan"].(map[string]any)["id"]; got != id {
		t.Fatal("tag change recreated clan")
	}
	if code := do("POST", "/admin/v1/clans/"+id, `{"action":"set_tag","tag":"!","reason":"invalid"}`, true, nil); code != 400 {
		t.Fatal("invalid tag accepted", code)
	}
	for _, event := range a.events("chat") {
		for _, item := range event["lines"].([]any) {
			if item.(map[string]any)["text"] == "private match message" {
				t.Fatal("match/team chat leaked to menu")
			}
		}
	}
}
