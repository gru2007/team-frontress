package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/support"
)

func (a *API) adminAuthenticated(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if a.cfg.AdminKey == "" {
		http.NotFound(w, r)
		return false
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), []byte(a.cfg.AdminKey)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}
func (a *API) adminPayments(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	if a.gc.Commerce() == nil {
		writeJSON(w, 503, map[string]string{"error": "Steam commerce is disabled or fake"})
		return
	}
	writeJSON(w, 200, a.gc.Commerce().View())
}
func (a *API) adminPaymentOperation(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthenticated(w, r) {
		return
	}
	if a.gc.Commerce() == nil {
		writeJSON(w, 503, map[string]string{"error": "Steam commerce is disabled or fake"})
		return
	}
	actor, err := strconv.ParseUint(r.Header.Get("X-Frontress-Actor"), 10, 64)
	if err != nil || !steamauth.ValidIndividual(actor) {
		writeJSON(w, 400, map[string]string{"error": "verified admin actor required"})
		return
	}
	var op support.Operation
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&op); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid operation"})
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "one operation required"})
		return
	}
	op.Actor = strconv.FormatUint(actor, 10)
	if op.Action == "refund" {
		id, err := strconv.ParseUint(op.Order, 10, 64)
		if err != nil || id == 0 {
			writeJSON(w, 400, map[string]string{"error": "a nonzero order ID is required for a full-order refund"})
			return
		}
	}
	if op.Action == "cancel_subscription" {
		id, e := strconv.ParseUint(op.SteamID, 10, 64)
		ag, ae := strconv.ParseUint(op.Agreement, 10, 64)
		if e != nil || ae != nil || ag == 0 || !steamauth.ValidIndividual(id) {
			writeJSON(w, 400, map[string]string{"error": "invalid player or agreement"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	result, err := a.gc.Commerce().Execute(ctx, op)
	if err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, result)
}
