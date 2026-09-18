package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func request(a *app, method, path, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, a.cfg.PublicURL+path, strings.NewReader(body))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

func login(t *testing.T, a *app, id int64) *http.Cookie {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"init_data": initData(id, time.Now())})
	w := request(a, "POST", "/api/auth/telegram", string(b), a.cfg.PublicURL, nil)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("login did not set cookie")
	return nil
}

func bearerRequest(a *app, method, path, body, authorization string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, a.cfg.PublicURL+path, strings.NewReader(body))
	r.Header.Set("Origin", a.cfg.PublicURL)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", authorization)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

func TestBearerPrecedenceAndInvalidSessions(t *testing.T) {
	a := testApp(t)
	admin := login(t, a, 1)
	member := login(t, a, 2)
	if w := bearerRequest(a, "GET", "/api/admin", "", "bEaReR "+member.Value, admin); w.Code != 403 {
		t.Fatal("bearer did not override admin cookie")
	}
	if _, err := a.db.Exec(`UPDATE sessions SET expires_at=0 WHERE hash=?`, tokenHash(member.Value)); err != nil {
		t.Fatal(err)
	}
	for _, authorization := range []string{"", "Bearer", "Basic " + admin.Value, "Bearer short", "Bearer " + strings.Repeat("0", 64), "Bearer " + member.Value, "Bearer " + admin.Value + " extra"} {
		for _, path := range []string{"/api/me", "/api/announcements", "/api/admin"} {
			if w := bearerRequest(a, "GET", path, "", authorization, admin); w.Code != 401 {
				t.Fatalf("invalid bearer fell back to cookie: %s %q: %d", path, authorization, w.Code)
			}
		}
		if w := bearerRequest(a, "POST", "/api/steam/start", `{}`, authorization, admin); w.Code != 401 {
			t.Fatal("invalid bearer mutation accepted")
		}
	}
}

func TestAPIAuthorizationAndCSRF(t *testing.T) {
	a := testApp(t)
	w := request(a, "GET", "/api/me", "", "", nil)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"user":null}` {
		t.Fatal("unauthenticated me shape")
	}
	admin := login(t, a, 1)
	member := login(t, a, 2)
	if !admin.HttpOnly || !admin.Secure || admin.SameSite != http.SameSiteLaxMode || admin.Path != "/" || admin.Domain != "" {
		t.Fatal("insecure session cookie")
	}
	for _, path := range []string{"/api/admin", "/api/announcements"} {
		if w := request(a, "GET", path, "", "", nil); w.Code != 401 {
			t.Fatalf("anonymous %s: %d", path, w.Code)
		}
	}
	if w := request(a, "GET", "/api/admin", "", "", member); w.Code != 403 {
		t.Fatal("non-admin read allowed")
	}
	for _, path := range []string{"/api/admin/keys", "/api/admin/announcements"} {
		if w := request(a, "POST", path, `{}`, a.cfg.PublicURL, member); w.Code != 403 {
			t.Fatal("non-admin mutation allowed")
		}
	}
	for _, origin := range []string{"", "null", "https://evil.test", a.cfg.PublicURL + ".evil.test"} {
		if w := request(a, "POST", "/api/admin/keys", `{"keys":"SECRET"}`, origin, admin); w.Code != 403 {
			t.Fatalf("CSRF allowed: %q", origin)
		}
	}
	w = request(a, "POST", "/api/admin/keys", `{"keys":"SECRET\nSECRET\nSECOND"}`, a.cfg.PublicURL, admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"imported":2`) {
		t.Fatal("import contract failed")
	}
	w = request(a, "GET", "/api/admin", "", "", admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available_keys":2`) || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal("admin list contract or secret minimization failed")
	}
	w = request(a, "POST", "/api/claim", `{}`, a.cfg.PublicURL, member)
	if w.Code != 409 {
		t.Fatal("unlinked claim allowed")
	}
	w = request(a, "POST", "/api/group", `{}`, a.cfg.PublicURL, member)
	if w.Code != 403 {
		t.Fatal("unlinked group allowed")
	}
	w = request(a, "POST", "/api/join-request/approve", `{"grant_group_access":true}`, a.cfg.PublicURL, member)
	if w.Code != 409 {
		t.Fatal("join approval without pending Telegram query allowed")
	}
	if _, err := a.db.Exec(`UPDATE sessions SET expires_at=0 WHERE hash=?`, tokenHash(member.Value)); err != nil {
		t.Fatal(err)
	}
	w = request(a, "POST", "/api/steam/start", `{}`, a.cfg.PublicURL, member)
	if w.Code != 401 {
		t.Fatal("expired session allowed")
	}
	for _, body := range []string{`{"keys":"A","unknown":true}`, `{"keys":"A"}{}`, `{"keys":`, `{"keys":"` + strings.Repeat("X", (1<<20)+1) + `"}`} {
		w = request(a, "POST", "/api/admin/keys", body, a.cfg.PublicURL, admin)
		if w.Code != 400 && w.Code != 413 {
			t.Fatalf("invalid or oversized JSON allowed: %d", w.Code)
		}
		var e map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e["error"] == "" {
			t.Fatal("error not JSON")
		}
	}
	if w := request(a, "GET", "/healthz", "", "", nil); w.Code != 200 {
		t.Fatal("health check failed")
	}
}

func TestAuthenticationAllocatesAvailableKeys(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	login(t, a, 2)
	if _, err := a.db.Exec(`UPDATE users SET steam_id='76561198000000001' WHERE telegram_id=2`); err != nil {
		t.Fatal(err)
	}
	c := login(t, a, 2)
	w := request(a, "POST", "/api/claim", `{}`, a.cfg.PublicURL, c)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"waiting":true`) {
		t.Fatal("waitlist contract failed")
	}
	if _, err := a.db.importKeys(ctx, "AUTH-KEY"); err != nil {
		t.Fatal(err)
	}
	c = login(t, a, 2)
	w = request(a, "GET", "/api/me", "", "", c)
	if !strings.Contains(w.Body.String(), `"key":"AUTH-KEY"`) || !strings.Contains(w.Body.String(), `"admin":false`) || !strings.Contains(w.Body.String(), `"group_enabled":true`) || !strings.Contains(w.Body.String(), `"join_request_pending":false`) {
		t.Fatal("linked authentication allocation or me contract failed")
	}
}

func TestConfig(t *testing.T) {
	t.Setenv("BOT_TOKEN", "test-token")
	t.Setenv("PUBLIC_URL", "https://frontress.test/")
	t.Setenv("ADMIN_TELEGRAM_IDS", "1, 2\n3")
	t.Setenv("TESTERS_CHAT_ID", "-100123")
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("LISTEN_ADDR", "")
	c, err := loadConfig()
	if err != nil || !c.Admins[3] || c.PublicURL != "https://frontress.test" || c.ListenAddr != ":8080" {
		t.Fatalf("config: %v", err)
	}
	for _, value := range []string{"http://frontress.test", "https://frontress.test/path", "https://user:pass@frontress.test", "https://frontress.test?q=a", "https://frontress.test/#a", ""} {
		t.Setenv("PUBLIC_URL", value)
		if _, err := loadConfig(); err == nil {
			t.Fatalf("invalid PUBLIC_URL allowed: %s", value)
		}
	}
	t.Setenv("PUBLIC_URL", "https://frontress.test")
	t.Setenv("ADMIN_TELEGRAM_IDS", "@admin")
	if _, err := loadConfig(); err == nil {
		t.Fatal("invalid admin whitelist allowed")
	}
	t.Setenv("ADMIN_TELEGRAM_IDS", "")
	t.Setenv("TESTERS_CHAT_ID", "123")
	if _, err := loadConfig(); err == nil {
		t.Fatal("non-group chat allowed")
	}
}
