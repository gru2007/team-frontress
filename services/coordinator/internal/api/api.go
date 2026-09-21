// Package api is the coordinator's HTTP surface: the game client's queue, the
// public status page, and the endpoints a dedicated server uses to join the
// pool and report what happened.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/gc"
	"github.com/gru2007/team-frontress/services/coordinator/internal/gcwire"
	"github.com/gru2007/team-frontress/services/coordinator/internal/players"
	"github.com/gru2007/team-frontress/services/coordinator/internal/steamauth"
	"github.com/gru2007/team-frontress/services/coordinator/internal/war"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// maxBody permits a bounded batch of native GC packets while preventing an
// unauthenticated caller from making the JSON decoder allocate without limit.
const maxBody = 1 << 20

// Server wires the HTTP handlers to the matchmaker.
type Server struct {
	cfg     config.Config
	mm      Matchmaker
	war     *war.Engine
	players *players.Store
	log     *slog.Logger
	gc      *gc.Server
}

// Matchmaker is the part of mm.Matchmaker the API uses. Narrowing it keeps the
// handlers testable without a server pool.
type Matchmaker interface {
	gc.Matchmaker
	QueuedPlayers() map[wire.MatchGroup]int
	OpenMatches() map[wire.MatchGroup]int
	FreeServers() int
	Population() int
	LiveMatches() int
}

// New builds the API server.
func New(cfg config.Config, m Matchmaker, v steamauth.Verifier, w *war.Engine, rec *players.Store, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, mm: m, war: w, players: rec, log: log}
	s.gc = gc.New(cfg.Secret, m, v, log)
	return s
}

// Handler returns the routed HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gc/exchange", s.handleGCExchange)
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/player/{id}", s.handlePlayer)
	return mux
}

func (s *Server) handleGCExchange(w http.ResponseWriter, r *http.Request) {
	if s.gc == nil {
		writeErr(w, http.StatusServiceUnavailable, "game coordinator is unavailable")
		return
	}
	var req gcwire.ExchangeRequest
	if !decode(w, r, &req) {
		return
	}
	resp, err := s.gc.Exchange(r.Context(), req)
	if err != nil {
		s.log.Warn("GC exchange rejected", "role", req.Role, "err", err)
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, gc.ErrUnauthorized):
			status = http.StatusForbidden
		case errors.Is(err, gc.ErrConflict):
			status = http.StatusConflict
		case errors.Is(err, gc.ErrBadRequest), errors.Is(err, gcwire.ErrMalformedPacket):
			status = http.StatusBadRequest
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePlayer answers what the coordinator remembers about one player: the
// matches, the XP those matches are worth and the level that comes to.
//
// Public and unauthenticated, like the status page. It says nothing a
// scoreboard would not, and requiring a ticket to read your own level would
// mean the menu could not show it until the first queue request of the session.
func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request) {
	if s.players == nil {
		writeErr(w, http.StatusServiceUnavailable, "this coordinator keeps no player records")
		return
	}
	id := wire.SteamID(r.PathValue("id"))
	if !steamauth.ValidSteamID(id) {
		writeErr(w, http.StatusBadRequest, "not a SteamID64")
		return
	}

	rec := s.players.Get(id)
	into, needed := rec.LevelProgress()
	writeJSON(w, http.StatusOK, wire.PlayerProgress{
		SteamID:      id,
		Name:         rec.Name,
		Matches:      rec.Matches,
		Wins:         rec.Wins,
		Losses:       rec.Losses,
		Abandons:     rec.Abandons,
		XP:           rec.XP(),
		Level:        rec.Level(),
		LevelXP:      into,
		LevelXPTotal: needed,
	})
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
	if s.cfg.TF2Pickup.Enabled() {
		serverCapacityKnown = 0
	}
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
			Maps:        g.EffectiveMaps(),
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
