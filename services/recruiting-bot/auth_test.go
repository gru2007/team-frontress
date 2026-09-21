package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func signInit(token string, values url.Values) string {
	lines := []string{}
	for k := range values {
		if k != "hash" {
			lines = append(lines, k+"="+values.Get(k))
		}
	}
	sort.Strings(lines)
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	values.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return values.Encode()
}

func initData(id int64, now time.Time) string {
	u, _ := json.Marshal(telegramUser{ID: id, FirstName: "Tester"})
	return signInit("test-token", url.Values{"auth_date": {strconv.FormatInt(now.Unix(), 10)}, "user": {string(u)}, "query_id": {"test"}})
}

func TestInitData(t *testing.T) {
	now := time.Now()
	good := initData(123, now)
	u, err := verifyInitData(good, "test-token", now)
	if err != nil || u.ID != 123 {
		t.Fatalf("valid authentication: %v", err)
	}
	guard := signInit("test-token", url.Values{
		"auth_date":                    {strconv.FormatInt(now.Unix(), 10)},
		"user":                         {`{"id":123,"first_name":"Tester"}`},
		"chat_join_request_query_id":   {"guard-query"},
	})
	if u, err := verifyInitData(guard, "test-token", now); err != nil || u.ID != 123 {
		t.Fatalf("valid guard Mini App authentication: %v", err)
	}
	for name, raw := range map[string]string{
		"tampered":     strings.Replace(good, "Tester", "Admin", 1),
		"stale":        initData(123, now.Add(-61*time.Minute)),
		"future":       initData(123, now.Add(time.Minute)),
		"duplicate":    good + "&auth_date=1",
		"bad encoding": "%zz",
		"invalid user": initData(0, now),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifyInitData(raw, "test-token", now); err == nil {
				t.Fatal("accepted invalid initData")
			}
		})
	}
	if _, err := verifyInitData(good, "wrong-token", now); err == nil {
		t.Fatal("accepted wrong bot token")
	}
}

func steamAssertion(a *app, state string, now time.Time) url.Values {
	identity := "https://steamcommunity.com/openid/id/76561198000000001"
	return url.Values{
		"state": {state}, "openid.ns": {openIDNamespace}, "openid.mode": {"id_res"},
		"openid.op_endpoint": {steamProvider}, "openid.identity": {identity}, "openid.claimed_id": {identity},
		"openid.return_to": {a.steamReturnTo(state)}, "openid.response_nonce": {now.UTC().Format("2006-01-02T15:04:05Z") + "unique"},
		"openid.assoc_handle": {"handle"}, "openid.sig": {"signature"},
		"openid.signed": {"op_endpoint,claimed_id,identity,return_to,response_nonce,assoc_handle"},
	}
}

func TestSteamVerification(t *testing.T) {
	a := testApp(t)
	state := strings.Repeat("a", 64)
	now := time.Now()
	requests := 0
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.String() != steamProvider || r.Method != "POST" {
			t.Fatal("verification was not sent to fixed provider")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("openid.mode") != "check_authentication" || r.Form.Get("state") != "" {
			t.Fatal("bad verification request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ns:" + openIDNamespace + "\nis_valid:true\n")), Header: make(http.Header)}, nil
	})
	if _, _, err := a.verifySteam(context.Background(), steamAssertion(a, state, now), state, now); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatal("provider verification missing")
	}
	for name, mutate := range map[string]func(url.Values){
		"provider": func(v url.Values) { v.Set("openid.op_endpoint", "https://evil.example/") },
		"identity": func(v url.Values) { v.Set("openid.identity", v.Get("openid.identity")+"/") },
		"claimed host": func(v url.Values) {
			v.Set("openid.claimed_id", "https://evil.example/openid/id/76561198000000001")
			v.Set("openid.identity", v.Get("openid.claimed_id"))
		},
		"invalid account": func(v url.Values) {
			v.Set("openid.claimed_id", "https://steamcommunity.com/openid/id/11111111111111111")
			v.Set("openid.identity", v.Get("openid.claimed_id"))
		},
		"return_to":      func(v url.Values) { v.Set("openid.return_to", a.cfg.PublicURL+"/steam/callback") },
		"unsigned nonce": func(v url.Values) { v.Set("openid.signed", "op_endpoint,claimed_id,identity,return_to,assoc_handle") },
		"old nonce": func(v url.Values) {
			v.Set("openid.response_nonce", now.Add(-11*time.Minute).UTC().Format("2006-01-02T15:04:05Z")+"x")
		},
		"future nonce": func(v url.Values) {
			v.Set("openid.response_nonce", now.Add(time.Minute).UTC().Format("2006-01-02T15:04:05Z")+"x")
		},
		"duplicate": func(v url.Values) { v.Add("openid.mode", "id_res") },
		"namespace": func(v url.Values) { v.Set("openid.ns", "wrong") },
	} {
		t.Run(name, func(t *testing.T) {
			v := steamAssertion(a, state, now)
			mutate(v)
			if _, _, err := a.verifySteam(context.Background(), v, state, now); err == nil {
				t.Fatal("accepted invalid assertion")
			}
		})
	}
	if requests != 1 {
		t.Fatal("invalid assertion reached provider")
	}
	for _, body := range []string{"ns:" + openIDNamespace + "\nis_valid:false\n", "is_valid:true\n", "ns:" + openIDNamespace + "\nis_valid:true\nis_valid:false\n"} {
		a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		if _, _, err := a.verifySteam(context.Background(), steamAssertion(a, state, now), state, now); err == nil {
			t.Fatal("accepted negative or malformed server verification")
		}
	}
}

func TestSteamBrowserHandoff(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if err := a.db.register(ctx, 123, "Tester", false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.importKeys(ctx, "TEST-KEY"); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"init_data": initData(123, time.Now())})
	auth := request(a, "POST", "/api/auth/telegram", string(body), a.cfg.PublicURL, nil)
	var login struct {
		User         *user  `json:"user"`
		SessionToken string `json:"session_token"`
		Admin        bool   `json:"admin"`
		GroupEnabled bool   `json:"group_enabled"`
	}
	if err := json.Unmarshal(auth.Body.Bytes(), &login); err != nil || auth.Code != 200 || login.User == nil || login.User.TelegramID != 123 || len(login.SessionToken) != 64 || login.Admin || !login.GroupEnabled {
		t.Fatalf("Telegram auth contract: %s", auth.Body.String())
	}
	var hash string
	var expires int64
	if err := a.db.QueryRow(`SELECT hash,expires_at FROM sessions WHERE telegram_id=123`).Scan(&hash, &expires); err != nil || hash != tokenHash(login.SessionToken) || expires < time.Now().Add(30*24*time.Hour-time.Minute).Unix() {
		t.Fatalf("session storage: %v", err)
	}
	if cookies := auth.Result().Cookies(); len(cookies) != 1 || cookies[0].Value != login.SessionToken || cookies[0].MaxAge != 30*24*60*60 {
		t.Fatal("bearer and cookie session lifetimes differ")
	}
	w := bearerRequest(a, "POST", "/api/steam/start", `{}`, "Bearer "+login.SessionToken, nil)
	if w.Code != 200 {
		t.Fatalf("cookie-free Steam start: %s", w.Body.String())
	}
	var start struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewRecorder()
	a.routes().ServeHTTP(h, httptest.NewRequest("GET", start.URL, nil))
	if h.Code != 200 || !strings.Contains(h.Body.String(), "123") {
		t.Fatalf("handoff: %d", h.Code)
	}
	if h.Header().Get("Referrer-Policy") != "strict-origin" || h.Header().Get("Content-Security-Policy") != "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://steamcommunity.com; frame-ancestors 'none'" {
		t.Fatal("handoff policy blocks native POST or Steam redirect")
	}
	cookies := h.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("insecure handoff cookie")
	}
	repeat := httptest.NewRecorder()
	a.routes().ServeHTTP(repeat, httptest.NewRequest("GET", start.URL, nil))
	if repeat.Code != 400 {
		t.Fatal("reused handoff")
	}
	state := cookies[0].Value
	confirm := httptest.NewRequest("POST", a.cfg.PublicURL+"/steam/continue", nil)
	confirm.AddCookie(cookies[0])
	confirm.Header.Set("Origin", a.cfg.PublicURL)
	confirmed := httptest.NewRecorder()
	a.routes().ServeHTTP(confirmed, confirm)
	if confirmed.Code != 303 || !strings.HasPrefix(confirmed.Header().Get("Location"), steamProvider+"?") {
		t.Fatal("browser confirmation did not redirect to Steam")
	}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ns:" + openIDNamespace + "\nis_valid:true\n")), Header: make(http.Header)}, nil
	})
	callback := a.cfg.PublicURL + "/steam/callback?" + steamAssertion(a, state, time.Now()).Encode()
	missing := httptest.NewRecorder()
	a.routes().ServeHTTP(missing, httptest.NewRequest("GET", callback, nil))
	if missing.Code != 400 {
		t.Fatal("accepted callback without browser cookie")
	}
	cb := httptest.NewRequest("GET", callback, nil)
	cb.AddCookie(cookies[0])
	out := httptest.NewRecorder()
	a.routes().ServeHTTP(out, cb)
	if out.Code != 303 || out.Header().Get("Location") != a.cfg.PublicURL+"/?linked=1" {
		t.Fatalf("callback failed: %d %s", out.Code, out.Body.String())
	}
	u, err := a.db.user(ctx, 123)
	if err != nil || u.SteamID == "" || u.Key != "TEST-KEY" {
		t.Fatalf("link allocation failed: %v", err)
	}
	var session *http.Cookie
	for _, c := range out.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("external browser did not get a session")
	}
	me := httptest.NewRequest("GET", a.cfg.PublicURL+"/api/me", nil)
	me.AddCookie(session)
	mw := httptest.NewRecorder()
	a.routes().ServeHTTP(mw, me)
	if mw.Code != 200 || !strings.Contains(mw.Body.String(), "TEST-KEY") {
		t.Fatal("external browser session unusable")
	}
	if strings.Contains(mw.Body.String(), "session_token") || strings.Contains(out.Body.String(), login.SessionToken) {
		t.Fatal("session token exposed outside Telegram auth")
	}
	a.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return telegramReply(200, `{"ok":true,"result":{"invite_link":"https://t.me/+invite"}}`), nil
	})
	for _, endpoint := range []struct{ method, path, want string }{
		{"GET", "/api/me", `"key":"TEST-KEY"`},
		{"POST", "/api/claim", `"key":"TEST-KEY"`},
		{"POST", "/api/group", `https://t.me/+invite`},
		{"GET", "/api/announcements", `"announcements":[]`},
	} {
		w := bearerRequest(a, endpoint.method, endpoint.path, `{}`, "Bearer "+login.SessionToken, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), endpoint.want) || strings.Contains(w.Body.String(), "session_token") {
			t.Fatalf("cookie-free %s: %d %s", endpoint.path, w.Code, w.Body.String())
		}
	}
	replay := httptest.NewRecorder()
	a.routes().ServeHTTP(replay, cb)
	if replay.Code != 400 {
		t.Fatal("callback replay accepted")
	}
}
