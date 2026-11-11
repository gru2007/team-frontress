package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gc"
	"github.com/gru2007/team-frontress/services/gc/internal/journal"
	"github.com/gru2007/team-frontress/services/gc/internal/maps"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
)

// The admin panel's configuration, server keys and journal. See
// gc/settings.go and package journal.

// UseJournal lets the API answer /admin/v1/journal.
func (a *API) UseJournal(j *journal.Store) *API {
	a.journal = j
	return a
}

func adminActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	actor, err := strconv.ParseUint(r.Header.Get("X-Frontress-Actor"), 10, 64)
	if err != nil || !steamauth.ValidIndividual(actor) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "verified admin actor required"})
		return "", false
	}
	return strconv.FormatUint(actor, 10), true
}

// adminSettings is the configuration the panel edits, the read-only rest,
// and the stock maps by mode for its map pickers.
//
//	GET /admin/v1/settings
func (a *API) adminSettings(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	var v gc.SettingsView
	if err := a.gc.Call(r.Context(), func() { v = a.gc.Settings() }); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
		return
	}
	modes := map[string][]string{}
	for _, m := range maps.Modes() {
		modes[m] = maps.InModes(m)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":     v,
		"managed":      config.ManagedSections,
		"restart_only": config.RestartOnly,
		"catalog":      map[string]any{"modes": modes},
	})
}

// adminSaveSettings replaces whole sections and applies them.
//
//	PUT /admin/v1/settings
//	X-Frontress-Actor: <admin SteamID64>
//	{"revision": 4, "note": "...", "sections": {"match_groups": [...], "chat": {...}}}
func (a *API) adminSaveSettings(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	actor, ok := adminActor(w, r)
	if !ok {
		return
	}
	var req struct {
		Revision int                        `json:"revision"`
		Note     string                     `json:"note"`
		Sections map[string]json.RawMessage `json:"sections"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if len(req.Note) > 300 {
		req.Note = req.Note[:300]
	}
	var (
		v     gc.SettingsView
		opErr error
	)
	if err := a.gc.Call(r.Context(), func() { v, opErr = a.gc.SaveSettings(req.Revision, req.Sections, actor, req.Note) }); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
		return
	}
	switch {
	case errors.Is(opErr, gc.ErrStaleSettings):
		writeJSON(w, http.StatusConflict, map[string]string{"error": opErr.Error()})
	case opErr != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": opErr.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"settings": v})
	}
}

// adminSettingsHistory is every saved revision, newest first.
//
//	GET /admin/v1/settings/history?limit=50
func (a *API) adminSettingsHistory(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	history, err := config.SettingsHistory(config.SettingsPath(a.gc.Config().DataDir), limit)
	if err != nil {
		a.log.Error("settings history", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the history"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": history})
}

// adminServerKeys lists the server keys, never the keys themselves, with the
// servers connected with each.
//
//	GET /admin/v1/server-keys
func (a *API) adminServerKeys(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	var out []gc.ServerKeyView
	if err := a.gc.Call(r.Context(), func() { out = a.gc.ServerKeys() }); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// adminChangeServerKey creates, edits, rotates or revokes a server key. A new
// key is in the answer once; revoking or rotating disconnects the servers
// that used the old one.
//
//	POST /admin/v1/server-keys
//	X-Frontress-Actor: <admin SteamID64>
//	{"action": "create", "name": "eu-02", "community": "frontress", "region": "eu", "anticheat_authority": true}
func (a *API) adminChangeServerKey(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	actor, ok := adminActor(w, r)
	if !ok {
		return
	}
	var req gc.ServerKeyChange
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	var (
		key   string
		opErr error
	)
	if err := a.gc.Call(r.Context(), func() { key, opErr = a.gc.ChangeServerKey(req, actor) }); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
		return
	}
	if opErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": opErr.Error()})
		return
	}
	out := map[string]string{"ok": req.Action}
	if key != "" {
		out["key"] = key
	}
	writeJSON(w, http.StatusOK, out)
}

// adminJournal is what the GC logged about players, servers and matches.
//
//	GET /admin/v1/journal?steam_id=&server=&match=&q=&level=info&before=<seq>&limit=100
func (a *API) adminJournal(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	if a.journal == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the journal is off"})
		return
	}
	q := r.URL.Query()
	before, _ := strconv.ParseUint(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	level := q.Get("level")
	switch level {
	case "", "debug", "info", "warn", "error":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "level must be debug, info, warn or error"})
		return
	}
	text := q.Get("q")
	if len(text) > 100 {
		text = text[:100]
	}
	page, err := a.journal.Query(journal.Query{
		Player: q.Get("steam_id"), Server: q.Get("server"), Match: q.Get("match"),
		Text: text, MinLevel: level, Before: before, Limit: limit,
	})
	if err != nil {
		a.log.Error("journal query", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the journal"})
		return
	}
	writeJSON(w, http.StatusOK, page)
}
