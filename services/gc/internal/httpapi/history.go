package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
)

func (a *API) matchList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, limit := 0, 20
	var err error
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil || offset < 0 || offset > 1000000 {
			writeJSON(w, 400, map[string]string{"error": "invalid offset"})
			return
		}
	}
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			writeJSON(w, 400, map[string]string{"error": "limit must be 1..100"})
			return
		}
	}
	steamID := q.Get("steam_id")
	if steamID != "" {
		id, err := strconv.ParseUint(steamID, 10, 64)
		if err != nil || !steamauth.ValidIndividual(id) {
			writeJSON(w, 400, map[string]string{"error": "invalid SteamID64"})
			return
		}
	}
	state := q.Get("state")
	if state != "" && state != "setup" && state != "run" && state != "ended" {
		writeJSON(w, 400, map[string]string{"error": "invalid state"})
		return
	}
	writeJSON(w, http.StatusOK, a.gc.History().List(steamID, state, offset, limit))
}

func (a *API) matchDetail(w http.ResponseWriter, r *http.Request) {
	m, ok := a.gc.History().Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "match not found"})
		return
	}
	writeJSON(w, http.StatusOK, m)
}
