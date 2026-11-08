package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gru2007/team-frontress/services/gc/internal/anticheat"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
)

// adminAntiCheatSanctions lists every active Easy Anti-Cheat sanction.
//
//	GET /admin/v1/anticheat
func (a *API) adminAntiCheatSanctions(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	var out []anticheat.Record
	if err := a.gc.Call(r.Context(), func() { out = a.gc.AntiCheatSanctions() }); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
		return
	}
	if out == nil {
		out = []anticheat.Record{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sanctions": out})
}

// adminAntiCheatPlayer is one player's anti-cheat record: their EOS Product
// User ID, every verdict servers reported, and any sanction.
//
//	GET /admin/v1/anticheat/{steamid}
func (a *API) adminAntiCheatPlayer(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	id, ok := steamIDParam(w, r)
	if !ok {
		return
	}
	var rec anticheat.Record
	if err := a.gc.Call(r.Context(), func() { rec, _ = a.gc.AntiCheatRecord(id) }); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
		return
	}
	if rec.Events == nil {
		rec.Events = []anticheat.Event{}
	}
	writeJSON(w, http.StatusOK, rec)
}

// adminAntiCheatAction bans a player from matchmaking or lifts a ban.
//
//	POST /admin/v1/anticheat/{steamid}
//	X-Frontress-Actor: <admin SteamID64>
//	{"action": "ban", "hours": 72, "reason": "..."}   hours 0: until lifted
//	{"action": "clear", "reason": "appeal accepted"}
//
// This is the GC's own matchmaking ban. A ban EAC enforces on protected
// servers is an EOS sanction and is lifted in the EOS Developer Portal.
func (a *API) adminAntiCheatAction(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	id, ok := steamIDParam(w, r)
	if !ok {
		return
	}
	actor, err := strconv.ParseUint(r.Header.Get("X-Frontress-Actor"), 10, 64)
	if err != nil || !steamauth.ValidIndividual(actor) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "verified admin actor required"})
		return
	}
	var req struct {
		Action string `json:"action"`
		Hours  int    `json:"hours"`
		Reason string `json:"reason"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || len(req.Reason) > 300 || req.Hours < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a reason (at most 300 characters) and hours >= 0 are required"})
		return
	}
	by := "admin:" + strconv.FormatUint(actor, 10)
	var opErr error
	callErr := a.gc.Call(r.Context(), func() {
		switch req.Action {
		case "ban":
			opErr = a.gc.AntiCheatBan(id, req.Hours, req.Reason, by)
		case "clear":
			opErr = a.gc.AntiCheatClear(id, req.Reason, by)
		default:
			opErr = errBadAction
		}
	})
	switch {
	case callErr != nil:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
	case opErr == errBadAction:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be ban or clear"})
	case opErr != nil:
		a.log.Error("anti-cheat admin action", "steamid", id, "err", opErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save"})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"ok": req.Action})
	}
}

type adminError string

func (e adminError) Error() string { return string(e) }

const errBadAction = adminError("bad action")

func steamIDParam(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("steamid"), 10, 64)
	if err != nil || !steamauth.ValidIndividual(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a SteamID64"})
		return 0, false
	}
	return id, true
}
