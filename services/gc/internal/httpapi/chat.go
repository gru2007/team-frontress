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

func (a *API) adminChat(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	q := r.URL.Query()
	f := gc.ChatFilter{Source: q.Get("source"), SteamID: q.Get("steam_id"), Q: q.Get("q"), Limit: 100}
	if f.Source != "" && f.Source != "all" && f.Source != "menu" && f.Source != "match" {
		writeJSON(w, 400, map[string]string{"error": "source must be all, menu or match"})
		return
	}
	if f.SteamID != "" {
		id, err := strconv.ParseUint(f.SteamID, 10, 64)
		if err != nil || !steamauth.ValidIndividual(id) {
			writeJSON(w, 400, map[string]string{"error": "invalid player"})
			return
		}
	}
	if raw := q.Get("before"); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid cursor"})
			return
		}
		f.Before = value
	}
	if raw := q.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeJSON(w, 400, map[string]string{"error": "limit must be 1..200"})
			return
		}
		f.Limit = value
	}
	var out map[string]any
	if err := a.gc.Call(r.Context(), func() {
		lines := a.gc.ChatModeration(f)
		next := uint64(0)
		if len(lines) == f.Limit {
			next = lines[len(lines)-1].ID
		}
		cfg := a.gc.Config()
		out = map[string]any{"lines": lines, "next": next, "servers": a.gc.ChatPolicyServers(), "epoch": a.gc.ChatEpoch(), "muted_steam_ids": cfg.Chat.MutedSteamIDs, "disabled": cfg.Chat.Disabled}
	}); err != nil {
		writeJSON(w, 503, map[string]string{"error": "coordinator busy"})
		return
	}
	writeJSON(w, 200, out)
}

func (a *API) adminDeleteChat(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	actor, err := strconv.ParseUint(r.Header.Get("X-Frontress-Actor"), 10, 64)
	if err != nil || !steamauth.ValidIndividual(actor) {
		writeJSON(w, 400, map[string]string{"error": "verified admin actor required"})
		return
	}
	id, err := strconv.ParseUint(r.PathValue("message"), 10, 64)
	if err != nil || id == 0 {
		writeJSON(w, 400, map[string]string{"error": "invalid message ID"})
		return
	}
	var req struct {
		Reason string `json:"reason"`
		Epoch  string `json:"epoch"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "invalid request"})
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || len(req.Reason) > 300 || req.Epoch == "" {
		writeJSON(w, 400, map[string]string{"error": "a reason (at most 300 bytes) is required"})
		return
	}
	var opErr error
	callErr := a.gc.Call(r.Context(), func() { opErr = a.gc.DeleteChatMessage(id, actor, req.Epoch, req.Reason) })
	switch {
	case callErr != nil:
		writeJSON(w, 503, map[string]string{"error": "coordinator busy"})
	case errors.Is(opErr, gc.ErrNoChatMessage):
		writeJSON(w, 404, map[string]string{"error": opErr.Error()})
	case errors.Is(opErr, gc.ErrChatRestarted):
		writeJSON(w, 409, map[string]string{"error": opErr.Error()})
	default:
		writeJSON(w, 200, map[string]string{"ok": "ok"})
	}
}
