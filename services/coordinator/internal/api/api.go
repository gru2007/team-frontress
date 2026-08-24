// Package api is the coordinator's HTTP surface: the game client's queue, the
// public status page, and the endpoints a dedicated server uses to join the
// pool and report what happened.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/steamauth"
	"github.com/gru2007/team-frontress/services/coordinator/internal/war"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// maxBody bounds every request body. A queue request is a few hundred bytes;
// anything near this is not one.
const maxBody = 64 << 10

// Server wires the HTTP handlers to the matchmaker.
type Server struct {
	cfg      config.Config
	mm       Matchmaker
	verifier steamauth.Verifier
	registry *pool.Registry
	war      *war.Engine
	log      *slog.Logger
}

// Matchmaker is the part of mm.Matchmaker the API uses. Narrowing it keeps the
// handlers testable without a server pool.
type Matchmaker interface {
	Enqueue(*mm.Ticket) (*mm.Ticket, error)
	Cancel(id string) error
	Status(id string) (wire.QueueStatus, error)
	QueuedPlayers() map[wire.MatchGroup]int
	OpenMatches() map[wire.MatchGroup]int
	FreeServers() int
	Population() int
	LiveMatches() int
	ObserveServer(matchID string, players int)
	ReportResult(ctx context.Context, res wire.MatchResult) error
}

// New builds the API server.
func New(cfg config.Config, m Matchmaker, v steamauth.Verifier, reg *pool.Registry, w *war.Engine, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: cfg, mm: m, verifier: v, registry: reg, war: w, log: log}
}

// Handler returns the routed HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("POST /v1/queue", s.handleQueue)
	mux.HandleFunc("GET /v1/queue/{id}", s.handleQueueStatus)
	mux.HandleFunc("DELETE /v1/queue/{id}", s.handleQueueCancel)
	mux.HandleFunc("POST /v1/gs/register", s.handleServerRegister)
	mux.HandleFunc("POST /v1/gs/heartbeat", s.handleServerHeartbeat)
	mux.HandleFunc("POST /v1/gs/result", s.handleServerResult)
	return mux
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	queued := map[string]int{}
	for g, n := range s.mm.QueuedPlayers() {
		queued[strconv.Itoa(int(g))] = n
	}

	// Static/registered providers expose a local FreeCount. Serveme is
	// remote and on-demand: Pool.Free() intentionally contributes zero for
	// it, which is a lower bound rather than "no server exists". Tell the
	// client whether the number is exact so the UI does not call that zero
	// "0 servers free".
	serverCapacityKnown := 1
	for _, provider := range s.cfg.Pool.Providers {
		if provider.Kind == "serveme" {
			serverCapacityKnown = 0
			break
		}
	}

	st := wire.Status{
		Name:                s.cfg.Name,
		OnlinePlayers:       s.mm.Population(),
		QueuedPlayers:       queued,
		LiveMatches:         s.mm.LiveMatches(),
		ServerCapacityKnown: serverCapacityKnown,
	}
	open := s.mm.OpenMatches()
	for _, g := range s.cfg.MatchGroups {
		info := wire.MatchGroupInfo{
			MatchGroup:  g.MatchGroup,
			Name:        g.Name,
			Enabled:     g.Enabled,
			Mode:        string(g.EffectiveMode()),
			MinPlayers:  g.MinPlayers,
			MaxPlayers:  g.MaxPlayers,
			Backfill:    g.BBackfills(),
			OpenMatches: open[g.MatchGroup],
			Maps:        g.Maps,
		}
		if r := g.Restrictions; r.Any() {
			info.Restrictions = &wire.GroupRestrictions{
				MaxPartySize:         g.PartyCap(),
				MinPartySize:         r.MinPartySize,
				MinMatchesPlayed:     r.MinMatchesPlayed,
				RequiresVerifiedAuth: r.RequireVerifiedAuth,
				InviteOnly:           len(r.AllowedSteamIDs) > 0,
				AbandonCooldownMins:  r.AbandonCooldownMins,
			}
		}
		st.MatchGroups = append(st.MatchGroups, info)
	}
	// The whole pool, not just the servers that registered themselves: an
	// operator with three static servers and no registrations was being told
	// they had none.
	st.FreeServers = s.mm.FreeServers()
	if s.war != nil {
		ws := &wire.WarStatus{CampaignID: s.war.Campaign()}
		for _, f := range s.war.Fronts() {
			info := wire.FrontInfo{
				FrontID:    f.ID,
				NodeID:     f.NodeID,
				Attacker:   string(f.Attacker),
				StageIndex: f.StageIndex,
			}
			if b, err := s.war.NextBattle(f.ID); err == nil {
				info.NodeName = b.NodeName
				info.StageCount = b.StageCount
				info.StageKind = b.Stage.Kind
				info.Map = b.Stage.Map
			}
			ws.ActiveFronts = append(ws.ActiveFronts, info)
		}
		st.War = ws
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	var req wire.QueueRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Players) == 0 {
		writeErr(w, http.StatusBadRequest, "a queue request needs at least one player")
		return
	}

	// The leader authenticates the whole party. Members are vouched for by the
	// leader, which is what a Steam party lobby already means; if that is not
	// good enough for a deployment, it is running auth.mode webapi and the
	// game enforces the roster by SteamID on connect anyway.
	leaderTicket := ""
	for _, p := range req.Players {
		if p.SteamID == req.Leader {
			leaderTicket = p.Ticket
			break
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	leader, err := s.verifier.Verify(ctx, req.Leader, leaderTicket)
	if err != nil {
		if errors.Is(err, steamauth.ErrRejected) {
			s.log.Warn("rejected queue request", "claimed", req.Leader, "err", err)
			writeErr(w, http.StatusUnauthorized, "steam did not accept that identity")
			return
		}
		s.log.Error("could not verify identity", "err", err)
		writeErr(w, http.StatusBadGateway, "identity check is unavailable")
		return
	}

	players := make([]wire.AssignedPlayer, 0, len(req.Players))
	seen := map[wire.SteamID]bool{}
	for _, p := range req.Players {
		if !steamauth.ValidSteamID(p.SteamID) {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("%q is not a SteamID64", p.SteamID))
			return
		}
		if seen[p.SteamID] {
			continue
		}
		seen[p.SteamID] = true
		players = append(players, wire.AssignedPlayer{SteamID: p.SteamID, Name: p.Name})
	}
	if !seen[leader] {
		writeErr(w, http.StatusBadRequest, "the party leader is not in the party")
		return
	}

	ticket, err := s.mm.Enqueue(&mm.Ticket{
		MatchGroup: req.MatchGroup,
		Leader:     leader,
		Players:    players,
		Maps:       req.Maps,
		LateJoinOK: req.LateJoinOK,
	})
	if err != nil {
		// A group that refused this party on its own terms is not a malformed
		// request: the client showed the queue, the queue said no, and the
		// player is owed the reason.
		if errors.Is(err, mm.ErrRestricted) {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wire.QueueResponse{
		TicketID:    ticket.ID,
		PollAfterMS: s.cfg.Timing.PollAfterMS,
	})
}

func (s *Server) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.mm.Status(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleQueueCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.mm.Cancel(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleServerRegister(w http.ResponseWriter, r *http.Request) {
	var req wire.ServerRegistration
	if !decode(w, r, &req) {
		return
	}
	if !s.serverSecretOK(req.Secret) {
		writeErr(w, http.StatusUnauthorized, "bad server secret")
		return
	}
	if s.registry == nil {
		writeErr(w, http.StatusNotImplemented, "this coordinator does not accept self-registered servers")
		return
	}
	if req.Connect == "" {
		writeErr(w, http.StatusBadRequest, "connect must be set")
		return
	}
	s.registry.Register(pool.Server{
		Name:    req.Name,
		Connect: req.Connect,
		Region:  req.Region,
		STV:     req.STV,
		RCON:    req.RCON,
	})
	s.log.Info("server registered", "server", req.Connect, "name", req.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleServerHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req wire.ServerHeartbeat
	if !decode(w, r, &req) {
		return
	}
	if !s.serverSecretOK(req.Secret) {
		writeErr(w, http.StatusUnauthorized, "bad server secret")
		return
	}
	if s.registry != nil && !s.registry.Heartbeat(req.Connect) && req.MatchID == "" {
		// Unknown server with nothing to say: tell it to register rather than
		// silently dropping its heartbeats forever. A server that names a live
		// match is a different case — an agent on a serveme reservation or a
		// static server never registered and still has a match to keep alive.
		writeErr(w, http.StatusNotFound, "register first")
		return
	}
	if req.MatchID != "" {
		s.mm.ObserveServer(req.MatchID, req.Players)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleServerResult(w http.ResponseWriter, r *http.Request) {
	var req wire.MatchResult
	if !decode(w, r, &req) {
		return
	}
	if !s.serverSecretOK(req.Secret) {
		writeErr(w, http.StatusUnauthorized, "bad server secret")
		return
	}
	if err := s.mm.ReportResult(r.Context(), req); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serverSecretOK compares in constant time, and refuses everything when no
// secret is configured: an empty secret must not become a skeleton key.
func (s *Server) serverSecretOK(got string) bool {
	if s.cfg.Secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Secret)) == 1
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, wire.Error{Error: msg})
}
