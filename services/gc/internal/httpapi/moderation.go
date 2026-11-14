package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gru2007/team-frontress/services/gc/internal/clans"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
)

func (a *API) adminClanModeration(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	var out map[string]any
	if err := a.gc.Call(r.Context(), func() { out = a.gc.ClanModeration() }); err != nil {
		writeJSON(w, 503, map[string]string{"error": "coordinator busy"})
		return
	}
	writeJSON(w, 200, out)
}

func (a *API) adminModerationAction(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	actor, err := strconv.ParseUint(r.Header.Get("X-Frontress-Actor"), 10, 64)
	if err != nil || !steamauth.ValidIndividual(actor) {
		writeJSON(w, 400, map[string]string{"error": "verified admin actor required"})
		return
	}
	var req struct {
		Action          string `json:"action"`
		Reason          string `json:"reason"`
		RestrictMembers bool   `json:"restrict_members"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || len(req.Reason) > 300 {
		writeJSON(w, 400, map[string]string{"error": "a reason (at most 300 bytes) is required"})
		return
	}
	op := clans.ModerationAction{Action: req.Action, Reason: req.Reason, Actor: actor, Clan: r.PathValue("clan"), RestrictMembers: req.RestrictMembers}
	if op.Clan != "" {
		if req.Action != "ban" && req.Action != "delete" && req.Action != "unban" || req.Action == "unban" && req.RestrictMembers {
			writeJSON(w, 400, map[string]string{"error": "action must be ban, delete or unban"})
			return
		}
	} else {
		id, ok := steamIDParam(w, r)
		if !ok {
			return
		}
		op.SteamID = id
		if req.RestrictMembers || (req.Action != "restrict_creation" && req.Action != "allow_creation" && req.Action != "mute" && req.Action != "unmute" && req.Action != "clear_chat") {
			writeJSON(w, 400, map[string]string{"error": "action must be restrict_creation, allow_creation, mute, unmute or clear_chat"})
			return
		}
	}
	var opErr error
	callErr := a.gc.Call(r.Context(), func() {
		if req.Action == "clear_chat" {
			a.gc.ClearPlayerChat(op.SteamID, actor, req.Reason)
		} else if req.Action == "mute" || req.Action == "unmute" {
			opErr = a.gc.ModerateChat(op.SteamID, actor, req.Action == "mute", req.Reason)
		} else {
			opErr = a.gc.ModerateClan(op)
		}
	})
	switch {
	case callErr != nil:
		writeJSON(w, 503, map[string]string{"error": "coordinator busy"})
	case errors.Is(opErr, clans.ErrNoClan):
		writeJSON(w, 404, map[string]string{"error": "no such clan or active clan ban"})
	case errors.Is(opErr, clans.ErrModeration):
		writeJSON(w, 400, map[string]string{"error": opErr.Error()})
	case opErr != nil:
		a.log.Error("save moderation", "err", opErr)
		writeJSON(w, 500, map[string]string{"error": "could not save moderation"})
	default:
		writeJSON(w, 200, map[string]string{"ok": "ok"})
	}
}
