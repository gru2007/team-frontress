package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func giveKey(t *testing.T, a *app, id int64, value string) {
	t.Helper()
	ctx := context.Background()
	if err := a.db.register(ctx, id, "Tester", true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE users SET steam_id=? WHERE telegram_id=?`, "7656119800000000"+string(rune('0'+id%10)), id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.importKeys(ctx, value); err != nil {
		t.Fatal(err)
	}
	key, err := a.db.claim(ctx, id)
	if err != nil || key != value {
		t.Fatalf("claim: %q %v", key, err)
	}
}

func TestGroupAccessRequiresActiveKey(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		if err := a.db.register(ctx, id, "Tester", true); err != nil {
			t.Fatal(err)
		}
	}
	// A legacy /group grant must no longer be an authorization source.
	if err := a.db.grantGroupAccess(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if eligible, err := a.db.hasActiveKey(ctx, 1); err != nil || eligible {
		t.Fatalf("legacy group access granted membership: eligible=%v err=%v", eligible, err)
	}
	giveKey(t, a, 2, "KEY-A")
	if eligible, err := a.db.hasActiveKey(ctx, 2); err != nil || !eligible {
		t.Fatalf("issued key did not grant access: eligible=%v err=%v", eligible, err)
	}
	revoked, err := a.db.revokeKey(ctx, 2)
	if err != nil || !revoked {
		t.Fatalf("revoke: %v %v", revoked, err)
	}
	if eligible, err := a.db.hasActiveKey(ctx, 2); err != nil || eligible {
		t.Fatalf("revoked key still granted access: eligible=%v err=%v", eligible, err)
	}
	restored, err := a.db.restoreKey(ctx, 2)
	if err != nil || !restored {
		t.Fatalf("restore: %v %v", restored, err)
	}
	if eligible, err := a.db.hasActiveKey(ctx, 2); err != nil || !eligible {
		t.Fatalf("restored key did not restore access: eligible=%v err=%v", eligible, err)
	}
}

func TestGroupCommandRequiresKey(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	invites := 0
	messages := []string{}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/createChatInviteLink"):
			invites++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["creates_join_request"] != true {
				t.Fatal("/group did not create a join-request link")
			}
			return telegramReply(200, `{"ok":true,"result":{"invite_link":"https://t.me/+group-request"}}`), nil
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var body struct { Text string `json:"text"` }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			messages = append(messages, body.Text)
			return telegramReply(200, `{"ok":true,"result":{}}`), nil
		default:
			t.Fatalf("unexpected Telegram method: %s", r.URL.Path)
			return nil, nil
		}
	})
	command := func(id int64) update {
		var u update
		b, _ := json.Marshal(map[string]any{"message": map[string]any{"from": map[string]any{"id": id, "first_name": "Tester"}, "chat": map[string]any{"id": id, "type": "private"}, "text": "/group"}})
		if err := json.Unmarshal(b, &u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	if err := a.handleUpdate(ctx, command(10)); err != nil {
		t.Fatal(err)
	}
	if invites != 0 || len(messages) != 1 || !strings.Contains(messages[0], "только после получения активного ключа") {
		t.Fatalf("keyless /group granted access: invites=%d messages=%q", invites, messages)
	}
	giveKey(t, a, 10, "KEY-B")
	if err := a.handleUpdate(ctx, command(10)); err != nil {
		t.Fatal(err)
	}
	if invites != 1 || len(messages) != 2 || !strings.Contains(messages[1], "https://t.me/+group-request") {
		t.Fatalf("key holder did not receive group link: invites=%d messages=%q", invites, messages)
	}
}

func TestGuardQueryCannotApproveWithoutKey(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	webApps := 0
	answers := 0
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendChatJoinRequestWebApp"):
			webApps++
			if body["chat_join_request_query_id"] != "guard-query" || body["web_app_url"] != a.cfg.PublicURL+"/?join_request=1" {
				t.Fatalf("bad guard Mini App request: %#v", body)
			}
			return telegramReply(200, `{"ok":true,"result":true}`), nil
		case strings.HasSuffix(r.URL.Path, "/answerChatJoinRequestQuery"):
			answers++
			if body["chat_join_request_query_id"] != "guard-query" || body["result"] != "approve" {
				t.Fatalf("bad guard answer: %#v", body)
			}
			return telegramReply(200, `{"ok":true,"result":true}`), nil
		default:
			t.Fatalf("unexpected Telegram method: %s", r.URL.Path)
			return nil, nil
		}
	})
	var join update
	if err := json.Unmarshal([]byte(`{"chat_join_request":{"from":{"id":10,"first_name":"Tester"},"chat":{"id":-100123},"query_id":"guard-query"}}`), &join); err != nil {
		t.Fatal(err)
	}
	if err := a.handleUpdate(ctx, join); err != nil {
		t.Fatal(err)
	}
	if webApps != 1 || answers != 0 {
		t.Fatalf("unexpected initial guard calls: webapps=%d answers=%d", webApps, answers)
	}
	cookie := login(t, a, 10)
	// Compatibility field from the previous Mini App must not be able to grant access anymore.
	w := request(a, "POST", "/api/join-request/approve", `{"grant_group_access":true}`, a.cfg.PublicURL, cookie)
	if w.Code != http.StatusForbidden || answers != 0 {
		t.Fatalf("keyless Mini App approval bypassed key check: %d %s answers=%d", w.Code, w.Body.String(), answers)
	}
	pending, err := a.db.joinRequestPending(ctx, 10)
	if err != nil || !pending {
		t.Fatalf("rejected guard query was lost: %v", err)
	}
	giveKey(t, a, 10, "KEY-C")
	w = request(a, "POST", "/api/join-request/approve", `{}`, a.cfg.PublicURL, cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"approved":true`) || answers != 1 {
		t.Fatalf("key holder could not approve pending query: %d %s answers=%d", w.Code, w.Body.String(), answers)
	}
	pending, err = a.db.joinRequestPending(ctx, 10)
	if err != nil || pending {
		t.Fatalf("resolved guard query remained pending: %v", err)
	}
}

func TestGuardQueryApprovesExistingKeyWithoutMiniApp(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	giveKey(t, a, 10, "KEY-D")
	answers := 0
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/answerChatJoinRequestQuery") {
			t.Fatalf("key holder unexpectedly opened Mini App: %s", r.URL.Path)
		}
		answers++
		return telegramReply(200, `{"ok":true,"result":true}`), nil
	})
	var join update
	if err := json.Unmarshal([]byte(`{"chat_join_request":{"from":{"id":10,"first_name":"Tester"},"chat":{"id":-100123},"query_id":"eligible-query"}}`), &join); err != nil {
		t.Fatal(err)
	}
	if err := a.handleUpdate(ctx, join); err != nil {
		t.Fatal(err)
	}
	if answers != 1 {
		t.Fatal("key holder guard query was not approved")
	}
}

func TestRevokeRemovesFromGroupAndDoesNotRecycleKey(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	giveKey(t, a, 2, "KEY-E")
	admin := login(t, a, 1)
	target := login(t, a, 2)
	removals := 0
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/unbanChatMember") {
			t.Fatalf("unexpected Telegram method: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["chat_id"] != float64(a.cfg.TestersChatID) || body["user_id"] != float64(2) || body["only_if_banned"] != false {
			t.Fatalf("bad removal request: %#v", body)
		}
		removals++
		return telegramReply(200, `{"ok":true,"result":true}`), nil
	})
	w := request(a, "POST", "/api/admin/revoke-key", `{"telegram_id":2}`, a.cfg.PublicURL, admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"revoked":true`) || !strings.Contains(w.Body.String(), `"removed_from_group":true`) || removals != 1 {
		t.Fatalf("revoke contract failed: %d %s removals=%d", w.Code, w.Body.String(), removals)
	}
	active, revoked, err := a.db.keyAccessState(ctx, 2)
	if err != nil || active || !revoked {
		t.Fatalf("revocation state: active=%v revoked=%v err=%v", active, revoked, err)
	}
	var available, assigned int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM keys WHERE telegram_id IS NULL`).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM keys WHERE telegram_id=2 AND value='KEY-E'`).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if available != 0 || assigned != 1 {
		t.Fatalf("revoked Steam key was recycled: available=%d assigned=%d", available, assigned)
	}
	w = request(a, "GET", "/api/me", "", "", target)
	if w.Code != 200 || strings.Contains(w.Body.String(), "KEY-E") || !strings.Contains(w.Body.String(), `"key_revoked":true`) {
		t.Fatalf("revoked key leaked to user: %d %s", w.Code, w.Body.String())
	}
	w = request(a, "POST", "/api/group", `{}`, a.cfg.PublicURL, target)
	if w.Code != http.StatusForbidden {
		t.Fatalf("revoked user retained group access: %d %s", w.Code, w.Body.String())
	}
	w = request(a, "POST", "/api/admin/restore-key", `{"telegram_id":2}`, a.cfg.PublicURL, admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"restored":true`) {
		t.Fatalf("restore failed: %d %s", w.Code, w.Body.String())
	}
	active, revoked, err = a.db.keyAccessState(ctx, 2)
	if err != nil || !active || revoked {
		t.Fatalf("restore state: active=%v revoked=%v err=%v", active, revoked, err)
	}
}

func TestLegacyGroupAccessIsReconciled(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for _, id := range []int64{2, 3} {
		if err := a.db.register(ctx, id, "Tester", true); err != nil {
			t.Fatal(err)
		}
		if err := a.db.grantGroupAccess(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	giveKey(t, a, 3, "KEY-F")
	removed := []int64{}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/unbanChatMember") {
			t.Fatalf("unexpected Telegram method: %s", r.URL.Path)
		}
		var body struct { UserID int64 `json:"user_id"` }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		removed = append(removed, body.UserID)
		return telegramReply(200, `{"ok":true,"result":true}`), nil
	})
	if err := a.reconcileGroupAccessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != 2 {
		t.Fatalf("legacy unauthorized users not reconciled: %v", removed)
	}
	var legacy int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM group_access`).Scan(&legacy); err != nil || legacy != 0 {
		t.Fatalf("legacy access table not cleared: %d %v", legacy, err)
	}
	if active, err := a.db.hasActiveKey(ctx, 3); err != nil || !active {
		t.Fatalf("key holder lost access during reconciliation: %v", err)
	}
}

func TestPendingGuardQuerySurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.db")
	db, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.register(ctx, 10, "Tester", false); err != nil {
		t.Fatal(err)
	}
	if err := db.rememberJoinQuery(ctx, 10, "persisted-query"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	query, err := db.pendingJoinQuery(ctx, 10)
	if err != nil || query != "persisted-query" {
		t.Fatalf("restart lost pending guard query: %q %v", query, err)
	}
}

func TestPublicGroupMustRequireJoinRequests(t *testing.T) {
	a := testApp(t)
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return telegramReply(200, `{"ok":true,"result":{"id":-100123,"type":"supergroup","username":"team_frontress_test"}}`), nil
	})
	if err := a.validateGroup(context.Background()); !errors.Is(err, errPublicGroupJoinRequestsDisabled) {
		t.Fatalf("public group without join requests accepted: %v", err)
	}
}

func TestGuardBotMustBeAssigned(t *testing.T) {
	a := testApp(t)
	calls := 0
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		switch {
		case strings.HasSuffix(r.URL.Path, "/getChat"):
			return telegramReply(200, `{"ok":true,"result":{"id":-100123,"type":"supergroup","username":"team_frontress_test","join_by_request":true}}`), nil
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			return telegramReply(200, `{"ok":true,"result":{"id":777,"is_bot":true,"first_name":"Bot","supports_join_request_queries":true}}`), nil
		default:
			t.Fatalf("unexpected method: %s", r.URL.Path)
			return nil, nil
		}
	})
	if err := a.validateGroup(context.Background()); !errors.Is(err, errGuardBotNotConfigured) {
		t.Fatalf("missing guard bot accepted: %v", err)
	}
	if calls != 2 {
		t.Fatalf("unexpected validation calls: %d", calls)
	}

	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getChat"):
			return telegramReply(200, `{"ok":true,"result":{"id":-100123,"type":"supergroup","username":"team_frontress_test","join_by_request":true,"guard_bot":{"id":777,"is_bot":true,"first_name":"Bot"}}}`), nil
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			return telegramReply(200, `{"ok":true,"result":{"id":777,"is_bot":true,"first_name":"Bot","supports_join_request_queries":true}}`), nil
		default:
			t.Fatalf("unexpected method: %s", r.URL.Path)
			return nil, nil
		}
	})
	if err := a.validateGroup(context.Background()); err != nil {
		t.Fatalf("configured guard bot rejected: %v", err)
	}
}

func TestRemovalPermissionMustBeAssigned(t *testing.T) {
	a := testApp(t)
	allow := false
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			return telegramReply(200, `{"ok":true,"result":{"id":777,"is_bot":true,"first_name":"Bot"}}`), nil
		case strings.HasSuffix(r.URL.Path, "/getChatMember"):
			if allow {
				return telegramReply(200, `{"ok":true,"result":{"status":"administrator","can_restrict_members":true,"user":{"id":777,"is_bot":true,"first_name":"Bot"}}}`), nil
			}
			return telegramReply(200, `{"ok":true,"result":{"status":"administrator","can_restrict_members":false,"user":{"id":777,"is_bot":true,"first_name":"Bot"}}}`), nil
		default:
			t.Fatalf("unexpected method: %s", r.URL.Path)
			return nil, nil
		}
	})
	if err := a.validateRemovalRights(context.Background()); !errors.Is(err, errCannotRemoveMembers) {
		t.Fatalf("missing removal permission accepted: %v", err)
	}
	allow = true
	if err := a.validateRemovalRights(context.Background()); err != nil {
		t.Fatalf("valid removal permission rejected: %v", err)
	}
}
