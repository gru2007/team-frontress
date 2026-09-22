package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRotateKeysReplacesInventoryAndPreservesAccess(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for _, id := range []int64{10, 20} {
		if err := a.db.register(ctx, id, "Tester", true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.importKeys(ctx, "OLD-A\nOLD-B\nOLD-WAITING"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE keys SET telegram_id=10,issued_at=1 WHERE value='OLD-A'; UPDATE keys SET telegram_id=20,issued_at=1 WHERE value='OLD-B'`); err != nil {
		t.Fatal(err)
	}
	if err := a.db.grantGroupAccess(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if revoked, err := a.db.revokeKey(ctx, 20); err != nil || !revoked {
		t.Fatalf("revoke: %v %v", revoked, err)
	}

	if _, required, err := a.db.rotateKeys(ctx, "ONLY-ONE"); err != errNotEnoughReplacementKeys || required != 2 {
		t.Fatalf("insufficient rotation: required=%d err=%v", required, err)
	}
	var oldCount int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM keys WHERE value LIKE 'OLD-%'`).Scan(&oldCount); err != nil || oldCount != 3 {
		t.Fatal("failed rotation changed inventory")
	}

	rotated, available, err := a.db.rotateKeys(ctx, "NEW-A\nNEW-B\nNEW-FREE\nNEW-FREE")
	if err != nil || len(rotated) != 2 || available != 1 {
		t.Fatalf("rotation: keys=%v available=%d err=%v", rotated, available, err)
	}
	var old, free, access int
	if err := a.db.QueryRow(`SELECT
 (SELECT COUNT(*) FROM keys WHERE value LIKE 'OLD-%'),
 (SELECT COUNT(*) FROM keys WHERE telegram_id IS NULL),
 (SELECT COUNT(*) FROM group_access WHERE telegram_id=10)`).Scan(&old, &free, &access); err != nil {
		t.Fatal(err)
	}
	if old != 0 || free != 1 || access != 1 {
		t.Fatalf("inventory/access changed incorrectly: old=%d free=%d access=%d", old, free, access)
	}
	active, revoked, err := a.db.keyAccessState(ctx, 20)
	if err != nil || active || !revoked {
		t.Fatalf("revocation was not retained: active=%v revoked=%v err=%v", active, revoked, err)
	}
}

func TestAdminRotateKeysNotifiesActiveOwners(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		if err := a.db.register(ctx, id, "Tester", true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.importKeys(ctx, "OLD"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE keys SET telegram_id=2,issued_at=1 WHERE value='OLD'`); err != nil {
		t.Fatal(err)
	}

	var sent struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatalf("unexpected Telegram method: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer telegram.Close()
	a.telegramBase = telegram.URL
	a.client = telegram.Client()

	admin := login(t, a, 1)
	w := request(a, "POST", "/api/admin/rotate-keys", `{"keys":"NEW-OWNER\nNEW-FREE"}`, a.cfg.PublicURL, admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reissued":1`) || !strings.Contains(w.Body.String(), `"notified":1`) {
		t.Fatalf("rotation response: %d %s", w.Code, w.Body.String())
	}
	if sent.ChatID != 2 || !strings.Contains(sent.Text, "NEW-OWNER") {
		t.Fatalf("notification: %+v", sent)
	}
}
