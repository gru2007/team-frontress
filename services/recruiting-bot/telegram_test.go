package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func telegramReply(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestAnnouncementsRetryAndBlocked(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		if err := a.db.register(ctx, id, "Tester", id != 3); err != nil {
			t.Fatal(err)
		}
	}
	id, err := a.db.announce(ctx, "News")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatal("announced to users who never contacted bot")
	}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return telegramReply(429, `{"ok":false,"error_code":429,"parameters":{"retry_after":30}}`), nil
	})
	if err := a.deliverOne(ctx); err != nil {
		t.Fatal(err)
	}
	var done, attempts int
	var next int64
	if err := a.db.QueryRow(`SELECT done,attempts,next_at FROM deliveries WHERE announcement_id=? AND telegram_id=1`, id).Scan(&done, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	if done != 0 || attempts != 1 || next < time.Now().Add(29*time.Second).Unix() {
		t.Fatal("429 not durably backed off")
	}
	if err := a.deliverOne(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("global flood pause ignored")
	}
	if _, err := a.db.Exec(`UPDATE deliveries SET next_at=0; UPDATE settings SET value=0 WHERE name='delivery_cooldown'`); err != nil {
		t.Fatal(err)
	}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return telegramReply(403, `{"ok":false,"error_code":403}`), nil
	})
	if err := a.deliverOne(ctx); err != nil {
		t.Fatal(err)
	}
	var blocked int
	if err := a.db.QueryRow(`SELECT blocked FROM users WHERE telegram_id=1`).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if blocked != 1 {
		t.Fatal("blocked user not recorded")
	}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		var b struct {
			Text   string `json:"text"`
			ChatID int64  `json:"chat_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Fatal(err)
		}
		if b.Text != "News" || b.ChatID != 2 {
			t.Fatal("wrong announcement recipient")
		}
		return telegramReply(200, `{"ok":true,"result":{}}`), nil
	})
	if err := a.deliverOne(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.deliverOne(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("successful delivery repeated")
	}
	if _, err := a.db.announce(ctx, "Next"); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE done=0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("blocked recipient requeued")
	}
	if err := a.db.register(ctx, 1, "Tester", true); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT blocked FROM users WHERE telegram_id=1`).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if blocked != 0 {
		t.Fatal("returning user remained blocked")
	}
}

func TestDeliveryLeaseAndTransientFailure(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if err := a.db.register(ctx, 1, "Tester", true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.announce(ctx, "News"); err != nil {
		t.Fatal(err)
	}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if err := a.deliverOne(ctx); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("delivery was not leased")
		}
		return nil, errors.New("network failed")
	})
	if err := a.deliverOne(ctx); err != nil {
		t.Fatal(err)
	}
	var done int
	var next int64
	if err := a.db.QueryRow(`SELECT done,next_at FROM deliveries`).Scan(&done, &next); err != nil {
		t.Fatal(err)
	}
	if done != 0 || next <= time.Now().Unix() {
		t.Fatal("transient failure not retried")
	}
	if _, err := a.db.Exec(`UPDATE deliveries SET next_at=0`); err != nil {
		t.Fatal(err)
	}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return telegramReply(200, `{"ok":true,"result":{}}`), nil
	})
	if err := a.deliverOne(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestGroupEligibilityAndInvite(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		if err := a.db.register(ctx, id, "Tester", true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.Exec(`UPDATE users SET steam_id=CAST(76561198000000000+telegram_id AS TEXT) WHERE telegram_id IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.importKeys(ctx, "KEY"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.claim(ctx, 1); err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(r.URL.Path, "createChatInviteLink") {
			if body["creates_join_request"] != true || body["chat_id"] != float64(a.cfg.TestersChatID) || body["member_limit"] != nil {
				t.Fatal("invite is not join-request only")
			}
			return telegramReply(200, `{"ok":true,"result":{"invite_link":"https://t.me/+invite"}}`), nil
		}
		id := int64(body["user_id"].(float64))
		want := "declineChatJoinRequest"
		if id == 1 {
			want = "approveChatJoinRequest"
		}
		if !strings.HasSuffix(r.URL.Path, want) {
			t.Fatalf("user %d unexpected method %s", id, r.URL.Path)
		}
		return telegramReply(200, `{"ok":true,"result":true}`), nil
	})
	if _, err := a.groupInvite(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2, 3, 4} {
		var u update
		b, _ := json.Marshal(map[string]any{"chat_join_request": map[string]any{"from": map[string]int64{"id": id}, "chat": map[string]int64{"id": a.cfg.TestersChatID}}})
		if err := json.Unmarshal(b, &u); err != nil {
			t.Fatal(err)
		}
		if err := a.handleUpdate(ctx, u); err != nil {
			t.Fatal(err)
		}
		u.Join.Chat.ID = -999
		if err := a.handleUpdate(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 5 {
		t.Fatal("processed join requests for another group")
	}
}

func TestPollingPersistsProcessedOffset(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			return telegramReply(200, `{"ok":true,"result":{}}`), nil
		}
		requests++
		var body struct {
			Offset int64 `json:"offset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if body.Offset != 0 {
				t.Fatal("wrong initial offset")
			}
			return telegramReply(200, `{"ok":true,"result":[{"update_id":42,"message":{"from":{"id":123,"first_name":"Tester"},"chat":{"id":123,"type":"private"},"text":"/start"}}]}`), nil
		}
		if body.Offset != 43 {
			t.Fatal("processed offset not used")
		}
		cancel()
		return nil, ctx.Err()
	})
	a.poll(ctx)
	var offset int64
	if err := a.db.QueryRow(`SELECT value FROM settings WHERE name='offset'`).Scan(&offset); err != nil {
		t.Fatal(err)
	}
	if offset != 43 {
		t.Fatal("offset not durable")
	}
	u, err := a.db.user(context.Background(), 123)
	if err != nil || u.FirstName != "Tester" {
		t.Fatal("bot start did not register user")
	}
}

func TestPollingDoesNotAcknowledgeFailedUpdate(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			cancel()
			return nil, errors.New("failed")
		}
		return telegramReply(200, `{"ok":true,"result":[{"update_id":42,"message":{"from":{"id":123,"first_name":"Tester"},"chat":{"id":123,"type":"private"},"text":"/start"}}]}`), nil
	})
	a.poll(ctx)
	var offset int64
	if err := a.db.QueryRow(`SELECT value FROM settings WHERE name='offset'`).Scan(&offset); err != nil {
		t.Fatal(err)
	}
	if offset != 0 {
		t.Fatal("failed update was acknowledged")
	}
}

func TestDeliveryCooldownSurvivesNewAnnouncementsAndRestart(t *testing.T) {
	a := testApp(t)
	path := filepath.Join(t.TempDir(), "cooldown.db")
	db, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	a.db = db
	ctx := context.Background()
	if err := db.register(ctx, 1, "Tester", true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.announce(ctx, "Before"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return telegramReply(429, `{"ok":false,"error_code":429,"parameters":{"retry_after":30}}`), nil
		}
		return telegramReply(200, `{"ok":true,"result":{}}`), nil
	})
	if err := a.deliverOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.announce(ctx, "During"); err != nil {
		t.Fatal(err)
	}
	if err := a.deliverOne(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("new announcement bypassed cooldown: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a.db = db
	if err := a.deliverOne(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("restart lost cooldown: %v", err)
	}
	if calls != 1 {
		t.Fatal("network called during cooldown")
	}
	if _, err := db.Exec(`UPDATE settings SET value=0 WHERE name='delivery_cooldown'`); err != nil {
		t.Fatal(err)
	}
	if err := a.deliverOne(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("new announcement did not resume")
	}
}

func TestPollingPermanentErrorsDoNotPoisonUpdates(t *testing.T) {
	for _, method := range []string{"createChatInviteLink", "sendMessage", "approveChatJoinRequest"} {
		for _, code := range []int{400, 401, 403, 404} {
			t.Run(fmt.Sprintf("%s/%d", method, code), func(t *testing.T) {
				a := testApp(t)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := a.db.register(ctx, 123, "Tester", true); err != nil {
					t.Fatal(err)
				}
				if _, err := a.db.Exec(`UPDATE users SET steam_id='76561198000000001'; INSERT INTO keys(value,telegram_id) VALUES('KEY',123)`); err != nil {
					t.Fatal(err)
				}
				first := `{"update_id":42,"message":{"from":{"id":123},"chat":{"id":123,"type":"private"},"text":"/group"}}`
				if method == "approveChatJoinRequest" {
					first = fmt.Sprintf(`{"update_id":42,"chat_join_request":{"from":{"id":123},"chat":{"id":%d}}}`, a.cfg.TestersChatID)
				}
				polls, failures, replies := 0, 0, 0
				a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Path, "getUpdates") {
						polls++
						if polls == 1 {
							return telegramReply(200, `{"ok":true,"result":[`+first+`,{"update_id":43,"message":{"from":{"id":124},"chat":{"id":124,"type":"private"},"text":"/help"}}]}`), nil
						}
						cancel()
						return nil, ctx.Err()
					}
					if strings.HasSuffix(r.URL.Path, method) && failures == 0 {
						failures++
						return telegramReply(code, fmt.Sprintf(`{"ok":false,"error_code":%d}`, code)), nil
					}
					if strings.HasSuffix(r.URL.Path, "createChatInviteLink") {
						return telegramReply(200, `{"ok":true,"result":{"invite_link":"https://t.me/+invite"}}`), nil
					}
					if strings.HasSuffix(r.URL.Path, "sendMessage") {
						var body struct {
							Text string `json:"text"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Fatal(err)
						}
						if method == "createChatInviteLink" && replies == 0 && !strings.Contains(body.Text, "Не удалось создать приглашение") {
							t.Fatal("missing Russian failure message")
						}
						replies++
					}
					return telegramReply(200, `{"ok":true,"result":true}`), nil
				})
				a.poll(ctx)
				var offset int
				if err := a.db.QueryRow(`SELECT value FROM settings WHERE name='offset'`).Scan(&offset); err != nil {
					t.Fatal(err)
				}
				if offset != 44 || failures != 1 || replies == 0 {
					t.Fatalf("poll stalled: offset=%d failures=%d replies=%d", offset, failures, replies)
				}
			})
		}
	}
}

func TestPermanentTelegramErrorClassification(t *testing.T) {
	for _, err := range []error{errors.New("database failed"), sql.ErrConnDone, &telegramError{Code: 429}, &telegramError{Code: 500}} {
		if permanentTelegramError(err) {
			t.Fatalf("retryable failure acknowledged: %v", err)
		}
	}
	a := testApp(t)
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	var u update
	if err := json.Unmarshal([]byte(`{"message":{"from":{"id":123},"chat":{"id":123,"type":"private"},"text":"/group"}}`), &u); err != nil {
		t.Fatal(err)
	}
	if err := a.handleUpdate(context.Background(), u); err == nil || permanentTelegramError(err) {
		t.Fatal("database failure swallowed")
	}
}

func TestBlockedUserDatabaseFailureRemainsRetryable(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.db.Exec(`CREATE TRIGGER reject_blocked BEFORE UPDATE OF blocked ON users WHEN NEW.blocked=1 BEGIN SELECT RAISE(FAIL, 'blocked write failed'); END`); err != nil {
		t.Fatal(err)
	}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return telegramReply(403, `{"ok":false,"error_code":403}`), nil
	})
	var u update
	if err := json.Unmarshal([]byte(`{"message":{"from":{"id":123},"chat":{"id":123,"type":"private"},"text":"/help"}}`), &u); err != nil {
		t.Fatal(err)
	}
	if err := a.handleUpdate(ctx, u); err == nil || permanentTelegramError(err) {
		t.Fatalf("blocked-state database failure must be retried: %v", err)
	}
}
