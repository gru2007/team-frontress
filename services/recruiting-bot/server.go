package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed web/*
var webFiles embed.FS

type app struct {
	cfg          config
	db           *store
	client       *http.Client
	telegramBase string
}

func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": text})
}
func internal(w http.ResponseWriter) {
	fail(w, 500, "Временная ошибка сервера. Попробуйте позже.")
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if t := strings.Split(r.Header.Get("Content-Type"), ";")[0]; strings.TrimSpace(t) != "application/json" {
		fail(w, 415, "Ожидается JSON.")
		return false
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "Некорректный запрос.")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(w, 400, "Некорректный запрос.")
		return false
	}
	return true
}

func (a *app) me(w http.ResponseWriter, r *http.Request, id int64, token string) {
	if id == 0 {
		respond(w, map[string]any{"user": nil})
		return
	}
	u, err := a.db.user(r.Context(), id)
	if err != nil {
		internal(w)
		return
	}
	_, revoked, err := a.db.keyAccessState(r.Context(), id)
	if err != nil {
		internal(w)
		return
	}
	if revoked {
		u.Key = ""
	}
	pending, err := a.db.joinRequestPending(r.Context(), id)
	if err != nil {
		internal(w)
		return
	}
	body := map[string]any{"user": u, "admin": a.cfg.Admins[id], "group_enabled": a.cfg.TestersChatID != 0, "join_request_pending": pending, "key_revoked": revoked}
	if token != "" {
		body["session_token"] = token
	}
	respond(w, body)
}

func (a *app) require(admin bool, next func(http.ResponseWriter, *http.Request, int64)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := a.authenticated(r)
		if err != nil {
			fail(w, 401, "Откройте приложение через Telegram для входа.")
			return
		}
		if admin && !a.cfg.Admins[id] {
			fail(w, 403, "Доступ запрещен.")
			return
		}
		next(w, r, id)
	}
}

func (a *app) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.PingContext(r.Context()); err != nil {
			fail(w, 503, "Сервис недоступен.")
			return
		}
		respond(w, map[string]string{"status": "ok"})
	})
	m.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		id, err := a.authenticated(r)
		if _, provided := r.Header["Authorization"]; provided && err != nil {
			fail(w, 401, "Откройте приложение через Telegram для входа.")
			return
		}
		a.me(w, r, id, "")
	})
	m.HandleFunc("POST /api/auth/telegram", a.telegramAuth)
	m.HandleFunc("POST /api/steam/start", a.require(false, a.steamStart))
	m.HandleFunc("GET /steam/handoff", a.steamHandoff)
	m.HandleFunc("POST /steam/continue", a.steamContinue)
	m.HandleFunc("GET /steam/callback", a.steamCallback)
	m.HandleFunc("POST /api/claim", a.require(false, func(w http.ResponseWriter, r *http.Request, id int64) {
		_, revoked, err := a.db.keyAccessState(r.Context(), id)
		if err != nil {
			internal(w)
			return
		}
		if revoked {
			fail(w, 403, "Ваш ключ отозван администратором. Новая выдача недоступна до восстановления доступа.")
			return
		}
		key, err := a.db.claim(r.Context(), id)
		if errors.Is(err, errNotLinked) {
			fail(w, 409, "Сначала привяжите Steam.")
			return
		}
		if err != nil {
			internal(w)
			return
		}
		respond(w, map[string]any{"key": key, "waiting": key == ""})
	}))
	m.HandleFunc("POST /api/join-request/approve", a.require(false, a.approveJoinFromMiniApp))
	m.HandleFunc("POST /api/group", a.require(false, func(w http.ResponseWriter, r *http.Request, id int64) {
		if a.cfg.TestersChatID == 0 {
			fail(w, 409, "Группа тестеров пока не настроена.")
			return
		}
		eligible, err := a.db.hasActiveKey(r.Context(), id)
		if err != nil {
			internal(w)
			return
		}
		if !eligible {
			fail(w, 403, "Доступ к группе открыт только участникам с активным выданным ключом.")
			return
		}
		link, err := a.groupInvite(r.Context())
		if err != nil {
			fail(w, 502, "Не удалось создать приглашение. Попробуйте позже.")
			return
		}
		respond(w, map[string]string{"url": link})
	}))
	m.HandleFunc("GET /api/announcements", a.require(false, func(w http.ResponseWriter, r *http.Request, _ int64) {
		items, err := a.db.announcements(r.Context())
		if err != nil {
			internal(w)
			return
		}
		respond(w, map[string]any{"announcements": items})
	}))
	m.HandleFunc("GET /api/admin", a.require(true, a.admin))
	m.HandleFunc("POST /api/admin/keys", a.require(true, func(w http.ResponseWriter, r *http.Request, _ int64) {
		var body struct {
			Keys string `json:"keys"`
		}
		if !decode(w, r, &body) {
			return
		}
		for _, key := range strings.Split(body.Keys, "\n") {
			if len(strings.TrimSpace(key)) > 512 {
				fail(w, 400, "Ключ слишком длинный.")
				return
			}
		}
		n, err := a.db.importKeys(r.Context(), body.Keys)
		if err != nil {
			internal(w)
			return
		}
		respond(w, map[string]int64{"imported": n})
	}))
	m.HandleFunc("POST /api/admin/revoke-key", a.require(true, func(w http.ResponseWriter, r *http.Request, _ int64) {
		var body struct {
			TelegramID int64 `json:"telegram_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.TelegramID <= 0 {
			fail(w, 400, "Некорректный Telegram ID.")
			return
		}
		revoked, removed, err := a.revokeTesterKey(r.Context(), body.TelegramID)
		if err != nil {
			if revoked {
				fail(w, 502, "Ключ отозван, но Telegram пока не подтвердил удаление из группы. Бот повторит удаление автоматически.")
			} else {
				internal(w)
			}
			return
		}
		if !revoked {
			fail(w, 409, "У пользователя нет активного ключа для отзыва.")
			return
		}
		respond(w, map[string]bool{"revoked": true, "removed_from_group": removed})
	}))
	m.HandleFunc("POST /api/admin/restore-key", a.require(true, func(w http.ResponseWriter, r *http.Request, _ int64) {
		var body struct {
			TelegramID int64 `json:"telegram_id"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.TelegramID <= 0 {
			fail(w, 400, "Некорректный Telegram ID.")
			return
		}
		restored, err := a.db.restoreKey(r.Context(), body.TelegramID)
		if err != nil {
			internal(w)
			return
		}
		if !restored {
			fail(w, 409, "У пользователя нет отозванного ключа.")
			return
		}
		respond(w, map[string]bool{"restored": true})
	}))
	m.HandleFunc("POST /api/admin/announcements", a.require(true, func(w http.ResponseWriter, r *http.Request, _ int64) {
		var body struct {
			Text string `json:"text"`
		}
		if !decode(w, r, &body) {
			return
		}
		body.Text = strings.TrimSpace(body.Text)
		if body.Text == "" || !utf8.ValidString(body.Text) || utf8.RuneCountInString(body.Text) > 2000 {
			fail(w, 400, "Объявление должно содержать от 1 до 2000 символов.")
			return
		}
		id, err := a.db.announce(r.Context(), body.Text)
		if err != nil {
			internal(w)
			return
		}
		respond(w, map[string]int64{"id": id})
	}))
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "Маршрут не найден.") })
	assets, _ := fs.Sub(webFiles, "web")
	files := http.FileServer(http.FS(assets))
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fail(w, 405, "Метод не поддерживается.")
			return
		}
		files.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		if len(r.URL.RawQuery) > 16384 {
			fail(w, 414, "Запрос слишком длинный.")
			return
		}
		if r.ContentLength > 1<<20 {
			fail(w, 413, "Запрос слишком большой.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if r.Header.Get("Origin") != a.cfg.PublicURL || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				fail(w, 403, "Недопустимый источник запроса.")
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}

func (a *app) admin(w http.ResponseWriter, r *http.Request, _ int64) {
	if err := a.db.ensureAccessSchema(r.Context()); err != nil {
		internal(w)
		return
	}
	if err := a.db.ensureReferralSchema(r.Context()); err != nil {
		internal(w)
		return
	}
	type participant struct {
		TelegramID int64  `json:"telegram_id"`
		FirstName  string `json:"first_name"`
		SteamID    string `json:"steam_id"`
		HasKey     bool   `json:"has_key"`
		KeyRevoked bool   `json:"key_revoked"`
		ReferrerID int64  `json:"referrer_id"`
		Referrals  int    `json:"referrals"`
	}
	stats := struct {
		Participants  int `json:"participants"`
		Linked        int `json:"linked"`
		AvailableKeys int `json:"available_keys"`
		IssuedKeys    int `json:"issued_keys"`
		RevokedKeys   int `json:"revoked_keys"`
		Referrals     int `json:"referrals"`
	}{}
	err := a.db.QueryRowContext(r.Context(), `SELECT
 (SELECT COUNT(*) FROM users),
 (SELECT COUNT(*) FROM users WHERE steam_id IS NOT NULL),
 (SELECT COUNT(*) FROM keys WHERE telegram_id IS NULL),
 (SELECT COUNT(*) FROM keys k LEFT JOIN key_revocations kr ON kr.key_id=k.id WHERE k.telegram_id IS NOT NULL AND kr.key_id IS NULL),
 (SELECT COUNT(*) FROM key_revocations),
 (SELECT COUNT(*) FROM referrals)`).Scan(&stats.Participants, &stats.Linked, &stats.AvailableKeys, &stats.IssuedKeys, &stats.RevokedKeys, &stats.Referrals)
	if err != nil {
		internal(w)
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT u.telegram_id,u.first_name,COALESCE(u.steam_id,''),
 (k.id IS NOT NULL AND kr.key_id IS NULL),kr.key_id IS NOT NULL,
 COALESCE(r.referrer_id,0),
 (SELECT COUNT(*) FROM referrals rr WHERE rr.referrer_id=u.telegram_id)
FROM users u
LEFT JOIN keys k ON k.telegram_id=u.telegram_id
LEFT JOIN key_revocations kr ON kr.key_id=k.id
LEFT JOIN referrals r ON r.referee_id=u.telegram_id
ORDER BY u.telegram_id`)
	if err != nil {
		internal(w)
		return
	}
	people := []participant{}
	for rows.Next() {
		var p participant
		if err := rows.Scan(&p.TelegramID, &p.FirstName, &p.SteamID, &p.HasKey, &p.KeyRevoked, &p.ReferrerID, &p.Referrals); err != nil {
			rows.Close()
			internal(w)
			return
		}
		people = append(people, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internal(w)
		return
	}
	items, err := a.db.announcements(r.Context())
	if err != nil {
		internal(w)
		return
	}
	respond(w, map[string]any{"stats": stats, "participants": people, "announcements": items})
}
