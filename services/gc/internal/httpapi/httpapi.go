// Package httpapi is the GC's HTTP surface.
//
// The transport (/gc/v1/*) carries the game's GC messages byte for byte. It is
// three calls, chosen because they work through ISteamHTTP -- which both the
// client and the dedicated server already have, with TLS and proxies handled
// by Steam -- and need nothing else in the engine:
//
//	POST /gc/v1/session   authenticate; returns a session token
//	POST /gc/v1/send      a batch of messages to the GC
//	GET  /gc/v1/recv      long-poll: messages from the GC, acknowledging the last batch
//
// Requests and responses are plain key=value lines (session) and binary
// frames (send/recv, see package wire). Everything else here is public JSON
// for menus and other projects.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gc"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// ProtocolVersion is bumped when the transport changes incompatibly. A game
// that sends a different one is told to update rather than misbehaving.
const ProtocolVersion = 1

// API serves HTTP.
type API struct {
	gc   *gc.GC
	auth steamauth.Verifier
	cfg  config.Config
	log  *slog.Logger
	poll time.Duration
}

// New builds the API.
func New(g *gc.GC, auth steamauth.Verifier, log *slog.Logger) *API {
	cfg := g.Config()
	return &API{gc: g, auth: auth, cfg: cfg, log: log, poll: cfg.Timing.Poll()}
}

// Handler is the whole mux.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /gc/v1/session", a.session)
	mux.HandleFunc("POST /gc/v1/send", a.send)
	mux.HandleFunc("GET /gc/v1/recv", a.recv)
	mux.HandleFunc("GET /v1/status", a.status)
	mux.HandleFunc("GET /v1/matches", a.matchList)
	mux.HandleFunc("GET /v1/matches/{id}", a.matchDetail)
	mux.HandleFunc("GET /admin/v1/payments", a.adminPayments)
	mux.HandleFunc("POST /admin/v1/payments/operations", a.adminPaymentOperation)
	mux.HandleFunc("GET /admin/v1/anticheat", a.adminAntiCheatSanctions)
	mux.HandleFunc("GET /admin/v1/anticheat/{steamid}", a.adminAntiCheatPlayer)
	mux.HandleFunc("POST /admin/v1/anticheat/{steamid}", a.adminAntiCheatAction)
	mux.HandleFunc("GET /admin/v1/reports", a.adminReportCases)
	mux.HandleFunc("GET /admin/v1/reports/{steamid}", a.adminReportCase)
	mux.HandleFunc("POST /admin/v1/reports/{steamid}", a.adminReportAction)
	mux.HandleFunc("GET /admin/{$}", a.adminConsole)
	mux.HandleFunc("GET /admin/{file}", a.adminConsole)
	mux.HandleFunc("GET /v1/war", a.war)
	mux.HandleFunc("GET /v1/player/{steamid}", a.playerRecord)
	mux.HandleFunc("GET /v1/player/{steamid}/quests", a.playerQuests)
	mux.HandleFunc("GET /v1/clans", a.clanList)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

func writeKV(w http.ResponseWriter, code int, kv ...string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		// Values are single-line by construction; strip anything that is not.
		v := strings.NewReplacer("\n", " ", "\r", " ").Replace(kv[i+1])
		fmt.Fprintf(&b, "%s=%s\n", kv[i], v)
	}
	_, _ = io.WriteString(w, b.String())
}

func (a *API) session(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		writeKV(w, http.StatusBadRequest, "error", "bad request")
		return
	}
	if v, _ := strconv.Atoi(r.PostForm.Get("protocol")); v != ProtocolVersion {
		writeKV(w, http.StatusUpgradeRequired, "error",
			fmt.Sprintf("this coordinator speaks protocol %d; update the game", ProtocolVersion))
		return
	}
	steamID, err := strconv.ParseUint(r.PostForm.Get("steam_id"), 10, 64)
	if err != nil || steamID == 0 {
		writeKV(w, http.StatusBadRequest, "error", "steam_id is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	switch r.PostForm.Get("kind") {
	case "client":
		var id uint64
		var appID uint32
		var err error
		if verifier, ok := a.auth.(interface {
			VerifyWithApp(context.Context, uint64, string) (uint64, uint32, error)
		}); ok {
			id, appID, err = verifier.VerifyWithApp(ctx, steamID, r.PostForm.Get("ticket"))
		} else {
			id, err = a.auth.Verify(ctx, steamID, r.PostForm.Get("ticket"))
		}
		if err != nil {
			a.log.Info("client refused", "steamid", steamID, "err", err)
			code := http.StatusUnauthorized
			if !errors.Is(err, steamauth.ErrRejected) {
				code = http.StatusServiceUnavailable // Steam itself failed; try again
			}
			writeKV(w, code, "error", "Steam could not confirm who you are")
			return
		}
		s := a.gc.Sessions().Open(session.Client, id, func(s *session.Session) { s.AppID = appID })
		var lobbyID uint64
		if err := a.gc.Call(ctx, func() { lobbyID = a.gc.ClientConnected(s, a.auth.Verified()) }); err != nil {
			writeKV(w, http.StatusServiceUnavailable, "error", "coordinator busy")
			return
		}
		writeKV(w, http.StatusOK, "session", s.ID, "steam_id", strconv.FormatUint(id, 10),
			"poll_ms", strconv.Itoa(int(a.poll.Milliseconds())), "name", a.cfg.Name, "lobby_id", strconv.FormatUint(lobbyID, 10))

	case "server":
		key, ok := a.serverKey(r.PostForm.Get("key"))
		if !ok {
			a.log.Info("server refused: bad key", "steamid", steamID, "remote", r.RemoteAddr)
			writeKV(w, http.StatusUnauthorized, "error", "unknown server key")
			return
		}
		s := a.gc.Sessions().Open(session.Server, steamID, func(s *session.Session) {
			s.ServerName = firstNonEmpty(r.PostForm.Get("name"), key.Name)
			s.CommunityID = key.Community
			s.AntiCheatAuthority = key.AntiCheatAuthority
			s.Region = firstNonEmpty(r.PostForm.Get("region"), key.Region)
			s.Connect = r.PostForm.Get("connect")
		})
		var lobbyID uint64
		if err := a.gc.Call(ctx, func() { lobbyID = a.gc.ServerConnected(s) }); err != nil {
			writeKV(w, http.StatusServiceUnavailable, "error", "coordinator busy")
			return
		}
		writeKV(w, http.StatusOK, "session", s.ID,
			"poll_ms", strconv.Itoa(int(a.poll.Milliseconds())), "name", a.cfg.Name, "lobby_id", strconv.FormatUint(lobbyID, 10))

	default:
		writeKV(w, http.StatusBadRequest, "error", "kind must be client or server")
	}
}

func (a *API) serverKey(presented string) (config.ServerKey, bool) {
	if presented == "" {
		return config.ServerKey{}, false
	}
	for _, k := range a.cfg.ServerKeys {
		if subtle.ConstantTimeCompare([]byte(k.Key), []byte(presented)) == 1 {
			return k, true
		}
	}
	return config.ServerKey{}, false
}

func (a *API) sessionOf(r *http.Request) *session.Session {
	id := r.Header.Get("X-GC-Session")
	if id == "" {
		return nil
	}
	return a.gc.Sessions().Get(id)
}

func (a *API) send(w http.ResponseWriter, r *http.Request) {
	s := a.sessionOf(r)
	if s == nil {
		writeKV(w, http.StatusUnauthorized, "error", "no such session")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeKV(w, http.StatusRequestEntityTooLarge, "error", "batch too large")
		return
	}
	frames, err := wire.ParseBatch(body)
	if err != nil {
		writeKV(w, http.StatusBadRequest, "error", err.Error())
		return
	}
	fresh, err := s.Accept(frames, a.gc.Sessions().Now())
	if err != nil {
		writeKV(w, http.StatusUnauthorized, "error", "session ended")
		return
	}
	for _, f := range fresh {
		data := f.Data
		a.gc.Do(func() { a.gc.Handle(s, data) })
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) recv(w http.ResponseWriter, r *http.Request) {
	s := a.sessionOf(r)
	if s == nil {
		writeKV(w, http.StatusUnauthorized, "error", "no such session")
		return
	}
	ack, _ := strconv.ParseUint(r.URL.Query().Get("ack"), 10, 64)
	frames, err := s.Poll(ack, a.poll, a.gc.Sessions().Now)
	if err != nil {
		writeKV(w, http.StatusUnauthorized, "error", "session ended")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	if len(frames) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var out []byte
	for _, f := range frames {
		out = wire.AppendFrame(out, f)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *API) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.gc.Status())
}

// war is the war screen's document; see gc.WarView.
func (a *API) war(w http.ResponseWriter, _ *http.Request) {
	v := a.gc.War()
	if v == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no war is running"})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *API) playerRecord(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("steamid"), 10, 64)
	if err != nil || !steamauth.ValidIndividual(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a SteamID64"})
		return
	}
	rec := a.gc.PlayerRecord(id)
	into, need := rec.LevelProgress()
	writeJSON(w, http.StatusOK, map[string]any{
		"steam_id":       strconv.FormatUint(id, 10),
		"matches":        rec.Matches,
		"wins":           rec.Wins,
		"losses":         rec.Losses,
		"abandons":       rec.Abandons,
		"rating":         rec.Rating,
		"xp":             rec.XP(),
		"level":          rec.Level(),
		"level_xp":       into,
		"level_xp_total": need,
	})
}

// playerQuests is a player's daily and weekly quests with their progress.
func (a *API) playerQuests(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("steamid"), 10, 64)
	if err != nil || !steamauth.ValidIndividual(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a SteamID64"})
		return
	}
	writeJSON(w, http.StatusOK, a.gc.PlayerQuests(id))
}

// clanList is the clan leaderboard: best first, members' names left out.
func (a *API) clanList(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, c := range a.gc.Clans().Top(100) {
		sector, wins := c.Banner()
		out = append(out, map[string]any{
			"id": c.ID, "name": c.Name, "tag": c.Tag, "motto": c.Motto, "emblem": c.Emblem, "color": c.Color,
			"open": c.Open, "level": c.Level(), "xp": c.XP, "members": len(c.Members), "battles": c.Battles,
			"wins": c.Wins, "banner_sector": sector, "banner_wins": wins,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"clans": out})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
