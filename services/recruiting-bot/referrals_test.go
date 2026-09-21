package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func referralUpdate(t *testing.T, id int64, text string) update {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"message": map[string]any{
			"from": map[string]any{"id": id, "first_name": "Tester"},
			"chat": map[string]any{"id": id, "type": "private"},
			"text": text,
		},
	})
	var u update
	if err := json.Unmarshal(b, &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestReferralIsFirstStartOnly(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		if err := a.db.register(ctx, id, "Referrer", true); err != nil {
			t.Fatal(err)
		}
	}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatalf("unexpected Telegram method: %s", r.URL.Path)
		}
		return telegramReply(200, `{"ok":true,"result":{}}`), nil
	})

	if err := a.handleUpdate(ctx, referralUpdate(t, 10, "/start ref_1")); err != nil {
		t.Fatal(err)
	}
	if err := a.handleUpdate(ctx, referralUpdate(t, 10, "/start ref_2")); err != nil {
		t.Fatal(err)
	}
	if err := a.handleUpdate(ctx, referralUpdate(t, 20, "/start ref_20")); err != nil {
		t.Fatal(err)
	}
	if err := a.handleUpdate(ctx, referralUpdate(t, 30, "/start ref_999999")); err != nil {
		t.Fatal(err)
	}

	var referrer int64
	if err := a.db.QueryRow(`SELECT referrer_id FROM referrals WHERE referee_id=10`).Scan(&referrer); err != nil || referrer != 1 {
		t.Fatalf("referral attribution changed: referrer=%d err=%v", referrer, err)
	}
	for _, id := range []int64{20, 30} {
		var n int
		if err := a.db.QueryRow(`SELECT COUNT(*) FROM referrals WHERE referee_id=?`, id).Scan(&n); err != nil || n != 0 {
			t.Fatalf("invalid referral accepted for %d: n=%d err=%v", id, n, err)
		}
	}
	count, err := a.db.referralCount(ctx, 1)
	if err != nil || count != 1 {
		t.Fatalf("referral count=%d err=%v", count, err)
	}
}

func TestReferralCommandReturnsDeepLinkAndCount(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if err := a.db.register(ctx, 1, "Referrer", true); err != nil {
		t.Fatal(err)
	}
	if err := a.db.register(ctx, 10, "Referral", true); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.db.recordReferral(ctx, 10, 1); err != nil || !ok {
		t.Fatalf("record referral: ok=%v err=%v", ok, err)
	}
	var sent string
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			return telegramReply(200, `{"ok":true,"result":{"id":777,"is_bot":true,"first_name":"Bot","username":"frontress_test_bot"}}`), nil
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var body struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			sent = body.Text
			return telegramReply(200, `{"ok":true,"result":{}}`), nil
		default:
			t.Fatalf("unexpected Telegram method: %s", r.URL.Path)
			return nil, nil
		}
	})
	if err := a.handleUpdate(ctx, referralUpdate(t, 1, "/ref")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent, "https://t.me/frontress_test_bot?start=ref_1") || !strings.Contains(sent, "Приглашено: 1") {
		t.Fatalf("unexpected referral message: %q", sent)
	}
}

func TestReferralPayloadParser(t *testing.T) {
	if id, ok := parseReferralPayload("ref_123"); !ok || id != 123 {
		t.Fatal("valid referral payload rejected")
	}
	for _, raw := range []string{"", "ref_", "ref_0", "ref_-1", "ref_nope", "other_123"} {
		if _, ok := parseReferralPayload(raw); ok {
			t.Fatalf("invalid referral payload accepted: %q", raw)
		}
	}
}
