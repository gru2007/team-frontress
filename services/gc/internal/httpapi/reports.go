package httpapi

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gru2007/team-frontress/services/gc/internal/gc"
	"github.com/gru2007/team-frontress/services/gc/internal/reports"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
)

// The moderation console: a page that reads the queue below with the admin
// key the moderator types in. The page holds no data itself.
//
//go:embed console
var console embed.FS

// adminConsole serves the moderation console.
//
//	GET /admin/            the page
//	GET /admin/{file}      its script and style
func (a *API) adminConsole(w http.ResponseWriter, r *http.Request) {
	if a.cfg.AdminKey == "" {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("file")
	if name == "" {
		name = "index.html"
	}
	types := map[string]string{
		"index.html":  "text/html; charset=utf-8",
		"console.js":  "text/javascript; charset=utf-8",
		"console.css": "text/css; charset=utf-8",
	}
	ct, ok := types[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	raw, err := console.ReadFile("console/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	_, _ = w.Write(raw)
}

// adminReportCases is the report queue.
//
//	GET /admin/v1/reports?status=open|closed|all&limit=100
//
// Open cases come by priority: reports (once per reporter or party, weighted
// by how reliable each reporter has been) plus what Easy Anti-Cheat recorded.
func (a *API) adminReportCases(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "open", "closed", "all":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be open, closed or all"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []gc.ReportCase
	if err := a.gc.Call(r.Context(), func() { out = a.gc.ReportCases(status, limit) }); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": out})
}

// adminReportCase is one case: every report, the reporters' standing, the
// player's Easy Anti-Cheat record and how earlier cases ended.
//
//	GET /admin/v1/reports/{steamid}
func (a *API) adminReportCase(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	id, ok := steamIDParam(w, r)
	if !ok {
		return
	}
	var (
		out   gc.ReportCaseDetail
		found bool
	)
	if err := a.gc.Call(r.Context(), func() { out, found = a.gc.ReportCaseDetail(id) }); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "nobody reported this player"})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// adminReportAction closes a case.
//
//	POST /admin/v1/reports/{steamid}
//	X-Frontress-Actor: <moderator SteamID64>
//	{"action": "ban", "hours": 0, "reason": "..."}   hours 0: until lifted
//	{"action": "dismiss", "reason": "..."}
//
// A ban here is the GC's matchmaking ban (as POST /admin/v1/anticheat); it
// closes the case as actioned and tells the reporters. A ban Easy Anti-Cheat
// enforces on every protected server is an EOS sanction (Developer Portal).
func (a *API) adminReportAction(w http.ResponseWriter, r *http.Request) {
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
	if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || len(req.Reason) > 300 || req.Hours < 0 || req.Hours > 24*365*10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a reason (at most 300 characters) and 0 <= hours <= 87600 are required"})
		return
	}
	by := "admin:" + strconv.FormatUint(actor, 10)
	var opErr error
	callErr := a.gc.Call(r.Context(), func() {
		switch req.Action {
		case "ban":
			opErr = a.gc.AntiCheatBan(id, req.Hours, req.Reason, by)
		case "dismiss":
			opErr = a.gc.DismissReportCase(id, by, req.Reason)
		default:
			opErr = errBadAction
		}
	})
	switch {
	case callErr != nil:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator busy"})
	case opErr == errBadAction:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be ban or dismiss"})
	case opErr == reports.ErrNothingOpen:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this case has no open reports"})
	case opErr != nil:
		a.log.Error("report case action", "steamid", id, "err", opErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save"})
	default:
		a.log.Info("report case action", "steamid", id, "action", req.Action, "by", by)
		writeJSON(w, http.StatusOK, map[string]string{"ok": req.Action})
	}
}
