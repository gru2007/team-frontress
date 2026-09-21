package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const steamProvider = "https://steamcommunity.com/openid/login"
const openIDNamespace = "http://specs.openid.net/auth/2.0"
const sessionCookie = "__Host-frontress"
const flowCookie = "__Host-frontress-steam"

var handoffPage = template.Must(template.New("handoff").Parse(`<!doctype html>
<html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Team Frontress — вход через Steam</title>
<style>body{font:18px system-ui;max-width:38rem;margin:10vh auto;padding:24px;background:#10151b;color:#eef2f5}button{font:inherit;padding:14px 20px;cursor:pointer}p{line-height:1.6}</style>
<h1>Привязка Steam</h1>
<p>Вы привязываете Steam к Telegram: <strong>{{.FirstName}}</strong>, ID <strong>{{.TelegramID}}</strong>.</p>
<p>Продолжайте только если это ваш аккаунт. Привязка постоянная. Если ссылку прислал другой человек, закройте страницу и откройте приложение из своего Telegram.</p>
<form method="post" action="/steam/continue"><button type="submit">Это мой аккаунт — перейти в Steam</button></form>
</html>`))

type telegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	IsBot     bool   `json:"is_bot"`
}

func verifyInitData(raw, token string, now time.Time) (telegramUser, error) {
	var u telegramUser
	v, err := url.ParseQuery(raw)
	if err != nil {
		return u, err
	}
	lines := make([]string, 0, len(v))
	for k, values := range v {
		if len(values) != 1 {
			return u, errors.New("duplicate field")
		}
		if k != "hash" {
			lines = append(lines, k+"="+values[0])
		}
	}
	sort.Strings(lines)
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	signature, err := hex.DecodeString(v.Get("hash"))
	if err != nil || !hmac.Equal(mac.Sum(nil), signature) {
		return u, errors.New("invalid signature")
	}
	date, err := strconv.ParseInt(v.Get("auth_date"), 10, 64)
	if err != nil || date > now.Add(30*time.Second).Unix() || date < now.Add(-time.Hour).Unix() {
		return u, errors.New("expired authentication")
	}
	if err := json.Unmarshal([]byte(v.Get("user")), &u); err != nil {
		return u, err
	}
	if u.ID <= 0 || u.IsBot || len(u.FirstName) > 1024 {
		return u, errors.New("invalid user")
	}
	return u, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func secureCookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

func (a *app) session(w http.ResponseWriter, r *http.Request, id int64) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO sessions(hash,telegram_id,expires_at) VALUES(?,?,?)`, tokenHash(token), id, time.Now().Add(30*24*time.Hour).Unix())
	if err != nil {
		return "", err
	}
	secureCookie(w, sessionCookie, token, 30*24*60*60)
	return token, nil
}

func (a *app) authenticated(r *http.Request) (int64, error) {
	var token string
	if values, provided := r.Header["Authorization"]; provided {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(values) != 1 || len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			return 0, errors.New("invalid authorization")
		}
		token = fields[1]
	} else {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			return 0, err
		}
		token = c.Value
	}
	if len(token) != 64 {
		return 0, errors.New("missing session")
	}
	var id int64
	err := a.db.QueryRowContext(r.Context(), `SELECT telegram_id FROM sessions WHERE hash=? AND expires_at>?`, tokenHash(token), time.Now().Unix()).Scan(&id)
	return id, err
}

func (a *app) telegramAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		InitData string `json:"init_data"`
	}
	if !decode(w, r, &body) {
		return
	}
	u, err := verifyInitData(body.InitData, a.cfg.BotToken, time.Now())
	if err != nil {
		fail(w, http.StatusUnauthorized, "Данные Telegram недействительны или устарели. Откройте приложение заново.")
		return
	}
	if err := a.db.register(r.Context(), u.ID, u.FirstName, false); err != nil {
		internal(w)
		return
	}
	// Re-authentication also lets an already linked tester claim newly imported stock.
	_, err = a.db.claim(r.Context(), u.ID)
	if err != nil && !errors.Is(err, errNotLinked) {
		internal(w)
		return
	}
	token, err := a.session(w, r, u.ID)
	if err != nil {
		internal(w)
		return
	}
	a.me(w, r, u.ID, token)
}

func (a *app) steamStart(w http.ResponseWriter, r *http.Request, id int64) {
	state, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	ticket, err := randomToken()
	if err != nil {
		internal(w)
		return
	}
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO steam_flows(state,ticket_hash,telegram_id,expires_at) VALUES(?,?,?,?)`, state, tokenHash(ticket), id, time.Now().Add(10*time.Minute).Unix())
	if err != nil {
		internal(w)
		return
	}
	respond(w, map[string]string{"url": a.cfg.PublicURL + "/steam/handoff?ticket=" + ticket})
}

func (a *app) steamReturnTo(state string) string {
	return a.cfg.PublicURL + "/steam/callback?state=" + state
}

func (a *app) steamHandoff(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	if len(ticket) != 64 {
		fail(w, 400, "Ссылка входа недействительна.")
		return
	}
	var state string
	var id int64
	err := a.db.QueryRowContext(r.Context(), `UPDATE steam_flows SET handed_off=1 WHERE ticket_hash=? AND handed_off=0 AND consumed=0 AND expires_at>? RETURNING state,telegram_id`, tokenHash(ticket), time.Now().Unix()).Scan(&state, &id)
	if err != nil {
		fail(w, 400, "Ссылка входа истекла или уже использована. Начните вход заново.")
		return
	}
	u, err := a.db.user(r.Context(), id)
	if err != nil {
		internal(w)
		return
	}
	secureCookie(w, flowCookie, state, 600)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "strict-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://steamcommunity.com; frame-ancestors 'none'")
	_ = handoffPage.Execute(w, u)
}

func (a *app) steamContinue(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(flowCookie)
	if err != nil || len(c.Value) != 64 {
		fail(w, 400, "Начните вход заново из приложения Telegram.")
		return
	}
	state := c.Value
	var valid int
	if err := a.db.QueryRowContext(r.Context(), `SELECT 1 FROM steam_flows WHERE state=? AND handed_off=1 AND consumed=0 AND expires_at>?`, state, time.Now().Unix()).Scan(&valid); err != nil {
		fail(w, 400, "Ссылка входа истекла или уже использована.")
		return
	}
	v := url.Values{
		"openid.ns": {openIDNamespace}, "openid.mode": {"checkid_setup"},
		"openid.return_to": {a.steamReturnTo(state)}, "openid.realm": {a.cfg.PublicURL + "/"},
		"openid.identity":   {"http://specs.openid.net/auth/2.0/identifier_select"},
		"openid.claimed_id": {"http://specs.openid.net/auth/2.0/identifier_select"},
	}
	http.Redirect(w, r, steamProvider+"?"+v.Encode(), http.StatusSeeOther)
}

func (a *app) verifySteam(ctx context.Context, v url.Values, state string, now time.Time) (string, string, error) {
	bad := errors.New("invalid Steam assertion")
	for _, values := range v {
		if len(values) != 1 {
			return "", "", bad
		}
	}
	if len(state) != 64 || v.Get("openid.ns") != openIDNamespace || v.Get("openid.mode") != "id_res" || v.Get("openid.op_endpoint") != steamProvider || v.Get("openid.return_to") != a.steamReturnTo(state) {
		return "", "", bad
	}
	identity := v.Get("openid.claimed_id")
	const prefix = "https://steamcommunity.com/openid/id/"
	if !strings.HasPrefix(identity, prefix) || v.Get("openid.identity") != identity {
		return "", "", bad
	}
	id := strings.TrimPrefix(identity, prefix)
	if len(id) != 17 {
		return "", "", bad
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return "", "", bad
		}
	}
	n, err := strconv.ParseUint(id, 10, 64)
	// Only individual public-universe Steam accounts are accepted.
	if err != nil || n>>56 != 1 || (n>>52)&15 != 1 || (n>>32)&0xfffff != 1 || uint32(n) == 0 {
		return "", "", bad
	}
	signed := map[string]bool{}
	for _, field := range strings.Split(v.Get("openid.signed"), ",") {
		signed[field] = true
	}
	for _, field := range []string{"op_endpoint", "claimed_id", "identity", "return_to", "response_nonce", "assoc_handle"} {
		if !signed[field] || v.Get("openid."+field) == "" {
			return "", "", bad
		}
	}
	nonce := v.Get("openid.response_nonce")
	if len(nonce) <= 20 || len(nonce) > 256 {
		return "", "", bad
	}
	date, err := time.Parse(time.RFC3339, nonce[:20])
	if err != nil || date.Before(now.Add(-10*time.Minute)) || date.After(now.Add(30*time.Second)) {
		return "", "", bad
	}
	post := url.Values{}
	for k, values := range v {
		if strings.HasPrefix(k, "openid.") {
			post[k] = append([]string(nil), values...)
		}
	}
	post.Set("openid.mode", "check_authentication")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, steamProvider, strings.NewReader(post.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.client.Do(req)
	if err != nil {
		return "", "", bad
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", "", bad
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil || len(body) > 8192 {
		return "", "", bad
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		k, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), ":")
		if !ok {
			return "", "", bad
		}
		if _, exists := fields[k]; exists {
			return "", "", bad
		}
		fields[k] = value
	}
	if fields["is_valid"] != "true" || fields["ns"] != openIDNamespace {
		return "", "", bad
	}
	return id, nonce, nil
}

func (a *app) steamCallback(w http.ResponseWriter, r *http.Request) {
	v, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(w, 400, "Некорректный ответ Steam.")
		return
	}
	state := v.Get("state")
	c, err := r.Cookie(flowCookie)
	if err != nil || len(state) != 64 || !hmac.Equal([]byte(c.Value), []byte(state)) {
		fail(w, 400, "Откройте ссылку входа в том же браузере и повторите попытку.")
		return
	}
	var valid int
	err = a.db.QueryRowContext(r.Context(), `SELECT 1 FROM steam_flows WHERE state=? AND handed_off=1 AND consumed=0 AND expires_at>?`, state, time.Now().Unix()).Scan(&valid)
	if err != nil {
		fail(w, 400, "Вход истек или уже завершен. Начните заново.")
		return
	}
	steam, nonce, err := a.verifySteam(r.Context(), v, state, time.Now())
	if err != nil {
		fail(w, 400, "Не удалось подтвердить вход через Steam.")
		return
	}
	id, err := a.db.link(r.Context(), state, nonce, steam)
	if err != nil {
		fail(w, 409, "Не удалось привязать Steam: учетная запись уже привязана или вход использован.")
		return
	}
	if _, err := a.session(w, r, id); err != nil {
		internal(w)
		return
	}
	secureCookie(w, flowCookie, "", -1)
	http.Redirect(w, r, a.cfg.PublicURL+"/?linked=1", http.StatusSeeOther)
}

func safeError(status int) error { return fmt.Errorf("remote API status %d", status) }
