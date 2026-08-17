// Package httpapi is the coordinator's whole external surface.
//
// Three audiences, one server:
//
//	/api/v1/client/*   the game client: hello, DEPLOY, poll, leave
//	/api/v1/world*     the war map: snapshot, fronts, timeline (read-only)
//	/api/v1/servers/*  the dedicated server agents: register, poll, report
//
// It is plain HTTP with JSON bodies and bearer tokens, deliberately: the custom
// framed-protobuf link it replaces was one more thing to debug at three in the
// morning, and nothing here needs a persistent socket. Clients and agents both
// long-poll, so the coordinator never has to dial back into a home network.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/greyline-frontress/coordinator/internal/mm"
	"github.com/greyline-frontress/coordinator/internal/pool"
	"github.com/greyline-frontress/coordinator/internal/steam"
	"github.com/greyline-frontress/coordinator/internal/war"
)

// Options configures the API.
type Options struct {
	// PoolKey is the shared secret an agent presents to join the server pool.
	PoolKey string
	// AdminKey guards the admin endpoints. Empty means the admin routes are
	// served without a key, which is only safe on a loopback listener.
	AdminKey string
	// MaxPollWait caps how long a long-poll may block.
	MaxPollWait time.Duration
	// ClientVersions, when non-empty, restricts which game builds may connect.
	ClientVersions []string
	// ProtocolVersion clients must present.
	ProtocolVersion int
}

// API holds everything the handlers need.
type API struct {
	log  *slog.Logger
	auth steam.Authenticator
	war  *war.Engine
	mm   *mm.Matchmaker
	pool *pool.Pool
	opts Options
}

// New builds the API.
func New(log *slog.Logger, auth steam.Authenticator, engine *war.Engine, maker *mm.Matchmaker, servers *pool.Pool, opts Options) *API {
	if opts.MaxPollWait <= 0 {
		opts.MaxPollWait = 25 * time.Second
	}
	if opts.ProtocolVersion == 0 {
		opts.ProtocolVersion = 2
	}
	return &API{log: log, auth: auth, war: engine, mm: maker, pool: servers, opts: opts}
}

// Handler returns the router.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health and discovery.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/status", a.status)

	// The war map. Read-only and unauthenticated: the state of the war is not a
	// secret, and a launcher or a website showing it is a feature.
	mux.HandleFunc("GET /api/v1/world", a.world)
	mux.HandleFunc("GET /api/v1/world/fronts", a.fronts)
	mux.HandleFunc("GET /api/v1/world/timeline", a.timeline)

	// The client.
	mux.HandleFunc("POST /api/v1/client/hello", a.hello)
	mux.HandleFunc("POST /api/v1/client/deploy", a.withPlayer(a.deploy))
	mux.HandleFunc("POST /api/v1/client/cancel", a.withPlayer(a.cancel))
	mux.HandleFunc("POST /api/v1/client/leave", a.withPlayer(a.leave))
	mux.HandleFunc("POST /api/v1/client/side", a.withPlayer(a.setSide))
	mux.HandleFunc("GET /api/v1/client/poll", a.withPlayer(a.poll))
	mux.HandleFunc("GET /api/v1/client/self", a.withPlayer(a.self))

	// The server pool.
	mux.HandleFunc("POST /api/v1/servers/register", a.serverRegister)
	mux.HandleFunc("POST /api/v1/servers/heartbeat", a.withServer(a.serverHeartbeat))
	mux.HandleFunc("GET /api/v1/servers/poll", a.withServer(a.serverPoll))
	mux.HandleFunc("POST /api/v1/servers/state", a.withServer(a.serverState))
	mux.HandleFunc("POST /api/v1/servers/result", a.withServer(a.serverResult))
	mux.HandleFunc("POST /api/v1/servers/deregister", a.withServer(a.serverDeregister))

	return logging(a.log, cors(mux))
}

// cors lets a browser page talk to the coordinator.
//
// The client UI is a web page — in the game it is the main menu panel, in
// development it is a browser tab — and it is served from somewhere that is not
// this origin. Allowing any origin is safe here because the API carries no
// cookies and no ambient authority: every authenticated call needs a bearer
// token the page was handed explicitly, and the endpoints that need no token
// are the war map, which is public on purpose.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Greyline-Server")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AdminHandler is the operator's read-only view, served on its own listener.
func (a *API) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /state", a.adminState)
	mux.HandleFunc("GET /world", a.world)
	mux.HandleFunc("GET /timeline", a.timeline)
	mux.HandleFunc("GET /servers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"servers": a.pool.List()})
	})
	mux.HandleFunc("GET /players", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"players": a.mm.Players()})
	})
	mux.HandleFunc("GET /battles", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"battles": a.mm.LiveBattles()})
	})
	mux.HandleFunc("POST /servers/drain", a.adminDrain)
	return a.adminAuth(mux)
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

type helloRequest struct {
	SteamID         uint64 `json:"steam_id"`
	Ticket          string `json:"ticket"`
	Name            string `json:"name"`
	Side            string `json:"side"`
	Region          string `json:"region"`
	ClientVersion   string `json:"client_version"`
	ProtocolVersion int    `json:"protocol_version"`
}

type helloResponse struct {
	Token             string            `json:"token"`
	Player            mm.PlayerView     `json:"player"`
	HeartbeatS        int               `json:"heartbeat_s"`
	PollWaitS         int               `json:"poll_wait_s"`
	WorldRevision     uint64            `json:"world_revision"`
	Campaign          war.Campaign      `json:"campaign"`
	AuthMode          string            `json:"auth_mode"`
	MercenaryContract *mm.ContractOffer `json:"mercenary_contract,omitempty"`
}

func (a *API) hello(w http.ResponseWriter, r *http.Request) {
	var req helloRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.ProtocolVersion != 0 && req.ProtocolVersion != a.opts.ProtocolVersion {
		fail(w, http.StatusBadRequest, fmt.Sprintf(
			"this coordinator speaks protocol %d, the client speaks %d",
			a.opts.ProtocolVersion, req.ProtocolVersion))
		return
	}
	if len(a.opts.ClientVersions) > 0 && !containsString(a.opts.ClientVersions, req.ClientVersion) {
		// A stale build could report results a newer war model cannot read, so
		// it is turned away at the door rather than halfway through a battle.
		fail(w, http.StatusForbidden, "this game build is not accepted by the coordinator")
		return
	}

	ticket, err := decodeTicket(req.Ticket)
	if err != nil {
		fail(w, http.StatusBadRequest, "malformed auth ticket")
		return
	}
	ident, err := a.auth.Authenticate(r.Context(), req.SteamID, ticket)
	if err != nil {
		a.log.Warn("client auth failed", "steam_id", req.SteamID, "err", err)
		fail(w, http.StatusUnauthorized, "steam authentication failed")
		return
	}

	side, _ := war.ParseSide(req.Side)
	p := a.mm.Hello(ident.SteamID, req.Name, side, req.Region)
	cfg := a.mm.Config()

	resp := helloResponse{
		Token:         p.Token(),
		Player:        p.Public(),
		HeartbeatS:    int(cfg.HeartbeatInterval.Seconds()),
		PollWaitS:     int(a.opts.MaxPollWait.Seconds()),
		WorldRevision: a.war.Revision(),
		Campaign:      a.war.Snapshot().Campaign,
		AuthMode:      a.auth.Mode(),
	}
	// If one side is short of bodies, say so here rather than making the player
	// work it out from the map.
	if mob := a.war.MobilizedSide(); mob != war.SideNone && mob != p.Side {
		resp.MercenaryContract = &mm.ContractOffer{
			Side:    mob,
			Message: mob.String() + " needs mercenaries",
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type deployRequest struct {
	FrontID        string `json:"front_id"`
	AcceptContract bool   `json:"accept_contract"`
}

func (a *API) deploy(w http.ResponseWriter, r *http.Request, p *mm.Player) {
	var req deployRequest
	if r.ContentLength > 0 && !readJSON(w, r, &req) {
		return
	}
	status, err := a.mm.Deploy(p, req.FrontID, req.AcceptContract)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": status, "seq": p.Seq()})
}

func (a *API) cancel(w http.ResponseWriter, r *http.Request, p *mm.Player) {
	a.mm.Cancel(p)
	writeJSON(w, http.StatusOK, map[string]any{"queue": a.mm.QueueStatusFor(p)})
}

func (a *API) leave(w http.ResponseWriter, r *http.Request, p *mm.Player) {
	a.mm.Leave(p)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type sideRequest struct {
	Side string `json:"side"`
}

func (a *API) setSide(w http.ResponseWriter, r *http.Request, p *mm.Player) {
	var req sideRequest
	if !readJSON(w, r, &req) {
		return
	}
	side, err := war.ParseSide(req.Side)
	if err != nil || side == war.SideNone {
		fail(w, http.StatusBadRequest, "side must be RED or BLU")
		return
	}
	if err := a.mm.SetSide(p, side); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"player": p.Public()})
}

func (a *API) poll(w http.ResponseWriter, r *http.Request, p *mm.Player) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	wait := a.opts.MaxPollWait
	if raw := r.URL.Query().Get("wait"); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs >= 0 {
			if d := time.Duration(secs) * time.Second; d < wait {
				wait = d
			}
		}
	}
	events := a.mm.Poll(r.Context(), p, since, wait)
	writeJSON(w, http.StatusOK, map[string]any{
		"events":         events,
		"seq":            p.Seq(),
		"world_revision": a.war.Revision(),
		"now":            time.Now().UTC(),
	})
}

func (a *API) self(w http.ResponseWriter, r *http.Request, p *mm.Player) {
	out := map[string]any{
		"player": p.Public(),
		"queue":  a.mm.QueueStatusFor(p),
		"seq":    p.Seq(),
	}
	if info, ok := a.mm.MatchInfoFor(p); ok {
		out["match"] = info
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// World
// ---------------------------------------------------------------------------

func (a *API) world(w http.ResponseWriter, r *http.Request) {
	snap := a.war.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"world":        snap,
		"live_battles": a.mm.LiveBattles(),
		"queued":       a.mm.QueuedOnFront(),
		"population":   a.mm.Population(),
	})
}

// fronts is the DEPLOY screen: where the war is being fought right now, and how
// busy each of those places is.
func (a *API) fronts(w http.ResponseWriter, r *http.Request) {
	fronts := a.war.ActiveFronts()
	queued := a.mm.QueuedOnFront()
	battles := a.mm.LiveBattles()

	type frontView struct {
		war.Front
		NodeName string          `json:"node_name"`
		Queued   int             `json:"queued"`
		Battles  []mm.LiveBattle `json:"battles,omitempty"`
		Players  int             `json:"players_fighting"`
	}
	out := make([]frontView, 0, len(fronts))
	for _, f := range fronts {
		v := frontView{Front: f, NodeName: a.war.NodeName(f.TargetNode), Queued: queued[f.ID]}
		for _, b := range battles {
			if b.FrontID == f.ID {
				v.Battles = append(v.Battles, b)
				v.Players += b.Players
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"fronts":     out,
		"population": a.mm.Population(),
		"campaign":   a.war.Snapshot().Campaign,
	})
}

// timeline is the world recap: what happened while you were away.
func (a *API) timeline(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	events := a.war.Timeline(since, limit)
	if since == 0 && r.URL.Query().Get("tail") != "" {
		events = a.war.Recap(limit)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (a *API) status(w http.ResponseWriter, r *http.Request) {
	total, free, live := a.pool.Counts()
	snap := a.war.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"campaign":       snap.Campaign,
		"theater":        snap.Theater,
		"world_revision": snap.Revision,
		"population":     a.mm.Population(),
		"active_fronts":  len(a.war.ActiveFronts()),
		"live_battles":   len(a.mm.LiveBattles()),
		"servers":        map[string]int{"total": total, "free": free, "busy": live},
		"auth_mode":      a.auth.Mode(),
	})
}

// ---------------------------------------------------------------------------
// Server pool
// ---------------------------------------------------------------------------

func (a *API) serverRegister(w http.ResponseWriter, r *http.Request) {
	if a.opts.PoolKey == "" || bearer(r) != a.opts.PoolKey {
		fail(w, http.StatusUnauthorized, "a valid pool key is required to join the server pool")
		return
	}
	var reg pool.Registration
	if !readJSON(w, r, &reg) {
		return
	}
	id, token, err := a.pool.Register(reg, time.Now())
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	a.log.Info("server joined the pool", "server", id, "name", reg.Name,
		"region", reg.Region, "connect", reg.ConnectAddress)
	writeJSON(w, http.StatusOK, map[string]any{
		"server_id":    id,
		"server_token": token,
		"heartbeat_s":  15,
		"poll_wait_s":  int(a.opts.MaxPollWait.Seconds()),
	})
}

type heartbeatRequest struct {
	Status  string `json:"status"`
	MatchID string `json:"match_id"`
	Map     string `json:"map"`
	Players int    `json:"players"`
}

func (a *API) serverHeartbeat(w http.ResponseWriter, r *http.Request, s *pool.Server) {
	var req heartbeatRequest
	if r.ContentLength > 0 && !readJSON(w, r, &req) {
		return
	}
	if err := a.pool.Heartbeat(s.ID, bearer(r), pool.Status(req.Status), req.MatchID, req.Map, req.Players, time.Now()); err != nil {
		fail(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) serverPoll(w http.ResponseWriter, r *http.Request, s *pool.Server) {
	wait := a.opts.MaxPollWait
	if raw := r.URL.Query().Get("wait"); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			if d := time.Duration(secs) * time.Second; d < wait {
				wait = d
			}
		}
	}
	cmd, ok, err := a.pool.Poll(r.Context(), s.ID, bearer(r), wait)
	if err != nil {
		fail(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !ok {
		// Nothing to do. A bare 204 keeps the agent's loop trivial.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, cmd)
}

type serverStateRequest struct {
	MatchID string `json:"match_id"`
	State   string `json:"state"`
	Players int    `json:"players"`
	Reason  string `json:"reason"`
}

func (a *API) serverState(w http.ResponseWriter, r *http.Request, s *pool.Server) {
	var req serverStateRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := a.mm.ServerState(s.ID, req.MatchID, req.State, req.Players); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type serverResultRequest struct {
	MatchID   string `json:"match_id"`
	Outcome   string `json:"outcome"`
	RedScore  uint32 `json:"red_score"`
	BluScore  uint32 `json:"blu_score"`
	Players   int    `json:"players"`
	DurationS int    `json:"duration_s"`
}

// serverResult is the single point where a game result becomes war history.
// Dedicated servers are the coordinator's own infrastructure and hold the pool
// key, so their word is taken as it stands — there is no player vote here.
func (a *API) serverResult(w http.ResponseWriter, r *http.Request, s *pool.Server) {
	var req serverResultRequest
	if !readJSON(w, r, &req) {
		return
	}
	outcome, err := war.ParseOutcome(req.Outcome)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	update, err := a.mm.ServerResult(s.ID, war.BattleResult{
		BattleID:   req.MatchID,
		Outcome:    outcome,
		RedScore:   req.RedScore,
		BluScore:   req.BluScore,
		Players:    req.Players,
		Duration:   time.Duration(req.DurationS) * time.Second,
		ServerID:   s.ID,
		ReportedAt: time.Now(),
	})
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "war_update": update})
}

func (a *API) serverDeregister(w http.ResponseWriter, r *http.Request, s *pool.Server) {
	if err := a.pool.Deregister(s.ID, bearer(r)); err != nil {
		fail(w, http.StatusUnauthorized, err.Error())
		return
	}
	a.log.Info("server left the pool", "server", s.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// Admin
// ---------------------------------------------------------------------------

func (a *API) adminState(w http.ResponseWriter, r *http.Request) {
	total, free, live := a.pool.Counts()
	writeJSON(w, http.StatusOK, map[string]any{
		"world":      a.war.Snapshot(),
		"population": a.mm.Population(),
		"stats":      a.mm.Stats(),
		"servers":    map[string]int{"total": total, "free": free, "busy": live},
		"battles":    a.mm.LiveBattles(),
	})
}

func (a *API) adminDrain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerID string `json:"server_id"`
		Drain    bool   `json:"drain"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := a.pool.Drain(req.ServerID, req.Drain); err != nil {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.opts.AdminKey != "" && bearer(r) != a.opts.AdminKey {
			fail(w, http.StatusUnauthorized, "admin key required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Middleware and plumbing
// ---------------------------------------------------------------------------

// withPlayer resolves the client's bearer token before the handler runs.
func (a *API) withPlayer(h func(http.ResponseWriter, *http.Request, *mm.Player)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.mm.Authenticate(bearer(r))
		if !ok {
			// The client's session is gone (coordinator restart, or a long
			// silence). Saying so precisely lets it just say hello again.
			fail(w, http.StatusUnauthorized, "unknown or expired session; say hello again")
			return
		}
		h(w, r, p)
	}
}

// withServer resolves an agent's bearer token.
func (a *API) withServer(h func(http.ResponseWriter, *http.Request, *pool.Server)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Greyline-Server")
		if id == "" {
			id = r.URL.Query().Get("server_id")
		}
		s, err := a.pool.Authenticate(id, bearer(r))
		if err != nil {
			if errors.Is(err, pool.ErrUnknownServer) {
				fail(w, http.StatusUnauthorized, "unknown server; register again")
				return
			}
			fail(w, http.StatusUnauthorized, "bad server token")
			return
		}
		h(w, r, s)
	}
}

func logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// Long-polls are the normal case and would drown the log; only report
		// the ones that failed or took an unexpected shape.
		if rec.status >= 400 {
			log.Warn("http", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "ms", time.Since(start).Milliseconds())
		} else {
			log.Debug("http", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return h[7:]
	}
	return ""
}

func decodeTicket(raw string) ([]byte, error) {
	if raw == "" {
		return nil, nil
	}
	return hexOrBase64(raw)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(body)
}

func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		fail(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return false
	}
	return true
}

func fail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Serve runs the API until ctx is cancelled.
func Serve(ctx context.Context, log *slog.Logger, addr string, h http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		// Long-polls hold a request open for the full poll window, so the write
		// timeout has to clear it with room to spare.
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
