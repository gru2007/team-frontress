package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testApp(t *testing.T) *app {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &app{cfg: config{BotToken: "test-token", PublicURL: "https://frontress.test", Admins: map[int64]bool{1: true}, TestersChatID: -100123}, db: s, client: &http.Client{Timeout: time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, telegramBase: "https://api.telegram.org"}
}

func addFlow(t *testing.T, s *store, id int64, state string, expiry time.Time) {
	t.Helper()
	_, err := s.Exec(`INSERT INTO steam_flows(state,ticket_hash,telegram_id,expires_at,handed_off) VALUES(?,?,?,?,1)`, state, tokenHash(state), id, expiry.Unix())
	if err != nil {
		t.Fatal(err)
	}
}

func TestAtomicClaimsAndWaitlist(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var keys []string
	for i := 0; i < 10; i++ {
		keys = append(keys, fmt.Sprintf("KEY-%02d", i))
	}
	n, err := a.db.importKeys(ctx, strings.Join(keys, "\n")+"\nKEY-00\n\n")
	if err != nil || n != 10 {
		t.Fatalf("import: %d %v", n, err)
	}
	for id := int64(1); id <= 30; id++ {
		if err := a.db.register(ctx, id, "Tester", true); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Exec(`UPDATE users SET steam_id=? WHERE telegram_id=?`, fmt.Sprint(76561198000000000+id), id); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for id := int64(1); id <= 30; id++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				if _, err := a.db.claim(ctx, id); err != nil {
					t.Errorf("claim: %v", err)
				}
			}
		}(id)
	}
	wg.Wait()
	var issued, unique int
	if err := a.db.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT telegram_id) FROM keys WHERE telegram_id IS NOT NULL`).Scan(&issued, &unique); err != nil {
		t.Fatal(err)
	}
	if issued != 10 || unique != 10 {
		t.Fatalf("allocation: issued=%d unique=%d", issued, unique)
	}
	var waiting int64
	if err := a.db.QueryRow(`SELECT telegram_id FROM users WHERE telegram_id NOT IN (SELECT telegram_id FROM keys WHERE telegram_id IS NOT NULL) LIMIT 1`).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if key, err := a.db.claim(ctx, waiting); err != nil || key != "" {
		t.Fatal("exhausted claim did not wait")
	}
	if _, err := a.db.importKeys(ctx, "NEW-KEY"); err != nil {
		t.Fatal(err)
	}
	key, err := a.db.claim(ctx, waiting)
	if err != nil || key != "NEW-KEY" {
		t.Fatal("waitlisted user cannot claim new stock")
	}
	if again, err := a.db.claim(ctx, waiting); err != nil || again != key {
		t.Fatal("claim not idempotent")
	}
	if err := a.db.register(ctx, 100, "Unlinked", false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.claim(ctx, 100); !errors.Is(err, errNotLinked) {
		t.Fatal("unlinked user claimed")
	}
}

func TestImmutableSteamAndSingleUse(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	expiry := time.Now().Add(time.Minute)
	for _, id := range []int64{1, 2} {
		if err := a.db.register(ctx, id, "Tester", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.importKeys(ctx, "ONE\nTWO"); err != nil {
		t.Fatal(err)
	}
	addFlow(t, a.db, 1, "first", expiry)
	if id, err := a.db.link(ctx, "first", "nonce1", "76561198000000001"); err != nil || id != 1 {
		t.Fatalf("link: %v", err)
	}
	if _, err := a.db.link(ctx, "first", "nonce2", "76561198000000001"); err == nil {
		t.Fatal("state reused")
	}
	addFlow(t, a.db, 2, "second", expiry)
	if _, err := a.db.link(ctx, "second", "nonce2", "76561198000000001"); !errors.Is(err, errConflict) {
		t.Fatal("steam bound to two Telegram accounts")
	}
	if _, err := a.db.link(ctx, "second", "nonce1", "76561198000000002"); err == nil {
		t.Fatal("nonce reused")
	}
	addFlow(t, a.db, 1, "third", expiry)
	if _, err := a.db.link(ctx, "third", "nonce3", "76561198000000002"); !errors.Is(err, errConflict) {
		t.Fatal("changed immutable identity")
	}
	u, err := a.db.user(ctx, 2)
	if err != nil || u.SteamID != "" || u.Key != "" {
		t.Fatal("failed transaction partially committed")
	}
	if _, err := a.db.link(ctx, "second", "nonce4", "76561198000000002"); err != nil {
		t.Fatal(err)
	}
	addFlow(t, a.db, 1, "expired", time.Now().Add(-time.Second))
	if _, err := a.db.link(ctx, "expired", "nonce5", "76561198000000001"); err == nil {
		t.Fatal("expired state accepted")
	}
}

func TestPersistentState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("database permissions expose private data")
	}
	ctx := context.Background()
	if err := s.register(ctx, 1, "Tester", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.announce(ctx, "Announcement"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(`UPDATE settings SET value=42 WHERE name='offset'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var offset, pending int
	if err := s.QueryRow(`SELECT value FROM settings WHERE name='offset'`).Scan(&offset); err != nil {
		t.Fatal(err)
	}
	if err := s.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE done=0`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if offset != 42 || pending != 1 {
		t.Fatalf("restart lost state: %d %d", offset, pending)
	}
	var mode string
	var timeout int
	if err := s.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := s.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || timeout != 5000 {
		t.Fatalf("SQLite configuration: %s %d", mode, timeout)
	}
}

func TestConcurrentSteamBinding(t *testing.T) {
	for _, sameTelegram := range []bool{false, true} {
		t.Run(fmt.Sprint("sameTelegram=", sameTelegram), func(t *testing.T) {
			a := testApp(t)
			ctx := context.Background()
			for _, id := range []int64{1, 2} {
				if err := a.db.register(ctx, id, "Tester", false); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.db.importKeys(ctx, "ONE\nTWO"); err != nil {
				t.Fatal(err)
			}
			secondID := int64(2)
			secondSteam := "76561198000000001"
			if sameTelegram {
				secondID = 1
				secondSteam = "76561198000000002"
			}
			addFlow(t, a.db, 1, "first", time.Now().Add(time.Minute))
			addFlow(t, a.db, secondID, "second", time.Now().Add(time.Minute))
			results := make(chan error, 2)
			go func() { _, err := a.db.link(ctx, "first", "nonce1", "76561198000000001"); results <- err }()
			go func() { _, err := a.db.link(ctx, "second", "nonce2", secondSteam); results <- err }()
			successes := 0
			for i := 0; i < 2; i++ {
				err := <-results
				if err == nil {
					successes++
				} else if !errors.Is(err, errConflict) {
					t.Fatal(err)
				}
			}
			if successes != 1 {
				t.Fatal("conflicting bindings both committed")
			}
			var issued, consumed, nonces int
			if err := a.db.QueryRow(`SELECT (SELECT COUNT(*) FROM keys WHERE telegram_id IS NOT NULL),(SELECT COUNT(*) FROM steam_flows WHERE consumed=1),(SELECT COUNT(*) FROM steam_nonces)`).Scan(&issued, &consumed, &nonces); err != nil {
				t.Fatal(err)
			}
			if issued != 1 || consumed != 1 || nonces != 1 {
				t.Fatalf("binding was not atomic: %d %d %d", issued, consumed, nonces)
			}
		})
	}
}
