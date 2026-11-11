package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gru2007/team-frontress/services/gc/internal/gc"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
)

// What the Frontress portal (mm.team-frontress.org) needs beyond the match
// archive and the moderation endpoints: a player list, the live picture of
// the pool, and a way for an admin to end a match.

// playerList is the players who have played.
//
//	GET /v1/players?q=<name or SteamID fragment>&sort=level|matches|wins|recent&offset=0&limit=50
func (a *API) playerList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, limit := 0, 50
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1000000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid offset"})
			return
		}
		offset = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be 1..200"})
			return
		}
		limit = n
	}
	sortBy := q.Get("sort")
	switch sortBy {
	case "", "level", "matches", "wins", "recent":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sort must be level, matches, wins or recent"})
		return
	}
	search := q.Get("q")
	if len(search) > 64 {
		search = search[:64]
	}
	page := a.gc.Players().List(search, sortBy, offset, limit)
	out := make([]map[string]any, 0, len(page.Records))
	for _, rec := range page.Records {
		out = append(out, map[string]any{
			"steam_id":   strconv.FormatUint(rec.SteamID, 10),
			"name":       rec.Name,
			"matches":    rec.Matches,
			"wins":       rec.Wins,
			"losses":     rec.Losses,
			"xp":         rec.XP(),
			"level":      rec.Level(),
			"last_match": rec.LastMatch,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"players": out, "total": page.Total, "offset": offset, "limit": limit})
}

// adminLive is the pool, the running matches and the queues.
//
//	GET /admin/v1/live
func (a *API) adminLive(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	var out gc.Live
	if err := a.gc.Call(r.Context(), func() { out = a.gc.Live() }); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// adminEndMatch ends a running match.
//
//	POST /admin/v1/matches/{id}/end
//	X-Frontress-Actor: <admin SteamID64>
//	{"reason": "..."}
func (a *API) adminEndMatch(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	matchID, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil || matchID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid match ID"})
		return
	}
	actor, err := strconv.ParseUint(r.Header.Get("X-Frontress-Actor"), 10, 64)
	if err != nil || !steamauth.ValidIndividual(actor) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "verified admin actor required"})
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || len(req.Reason) > 300 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a reason (at most 300 characters) is required"})
		return
	}
	var opErr error
	by := "admin:" + strconv.FormatUint(actor, 10)
	if err := a.gc.Call(r.Context(), func() { opErr = a.gc.AdminEndMatch(matchID, by, req.Reason) }); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
		return
	}
	if errors.Is(opErr, gc.ErrNoSuchMatch) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": opErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "ended"})
}
