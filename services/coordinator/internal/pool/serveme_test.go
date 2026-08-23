package pool

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
)

// fakeServeme implements enough of the serveme.tf reservation API to exercise
// the three-step flow the real one documents.
type fakeServeme struct {
	mu       sync.Mutex
	free     bool
	created  bool
	deleted  bool
	lastBody map[string]any
}

func (f *fakeServeme) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/reservations/new", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"reservation": map[string]any{
			"starts_at": "2026-08-23T12:00:00Z",
			"ends_at":   "2026-08-23T14:00:00Z",
		}})
	})
	mux.HandleFunc("POST /api/reservations/find_servers", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := map[string]any{"reservation": map[string]any{"starts_at": "2026-08-23T12:00:00Z"}}
		if f.free {
			out["servers"] = []map[string]any{{"id": 64, "name": "FritzBrigade #10"}}
		} else {
			out["servers"] = []map[string]any{}
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /api/reservations", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastBody = body
		f.created = true
		res, _ := body["reservation"].(map[string]any)
		writeJSON(w, map[string]any{"reservation": map[string]any{
			"id":       12345,
			"password": res["password"],
			"rcon":     res["rcon"],
			"status":   "Started",
			"server": map[string]any{
				"id": 64, "name": "FritzBrigade #10",
				"ip": "10.0.0.9", "port": "27015", "ip_and_port": "10.0.0.9:27015",
			},
		}})
	})
	mux.HandleFunc("DELETE /api/reservations/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deleted = true
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newFakeServeme(t *testing.T, free bool) (*fakeServeme, *Serveme) {
	t.Helper()
	f := &fakeServeme{free: free}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	p := NewServeme(config.ProviderConfig{
		Kind: "serveme", BaseURL: srv.URL, APIKey: "key", ReserveMins: 120, Region: "eu",
	})
	p.Client = srv.Client()
	return f, p
}

func TestServemeReservesAndReleases(t *testing.T) {
	f, p := newFakeServeme(t, true)
	ctx := context.Background()

	s, err := p.Acquire(ctx, Request{MatchID: "m1", Players: 12, Minutes: 90})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if s.Connect != "10.0.0.9:27015" {
		t.Fatalf("connect = %q, want the reserved server's address", s.Connect)
	}
	if s.RCON == "" {
		t.Fatal("the reservation came back with no rcon password")
	}
	if !s.Ephemeral {
		t.Error("a serveme reservation must be ephemeral: there is no free list to return it to")
	}
	if s.Region != "eu" {
		t.Errorf("region = %q, want the provider's", s.Region)
	}
	if !f.created {
		t.Fatal("no reservation was created")
	}

	// A reservation with no password is a public server: anyone in the
	// browser can walk into a matchmade game.
	res := f.lastBody["reservation"].(map[string]any)
	if pw, _ := res["password"].(string); pw == "" {
		t.Fatal("the reservation was created without a password")
	}
	if rc, _ := res["rcon"].(string); rc != s.RCON {
		t.Fatalf("rcon = %q, want the one we reserved with (%q)", s.RCON, rc)
	}

	s.Provider = "serveme"
	if err := p.Release(ctx, s); err != nil {
		t.Fatalf("release: %v", err)
	}
	if !f.deleted {
		t.Fatal("releasing the server did not end the reservation")
	}
}

func TestServemeWithNothingFreeIsNotAnError(t *testing.T) {
	_, p := newFakeServeme(t, false)
	_, err := p.Acquire(context.Background(), Request{MatchID: "m1"})
	if !errors.Is(err, ErrNoServer) {
		t.Fatalf("err = %v, want ErrNoServer so the next provider gets a turn", err)
	}
}

func TestServemeSurfacesAnAPIFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad api key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := NewServeme(config.ProviderConfig{Kind: "serveme", BaseURL: srv.URL, APIKey: "nope"})
	p.Client = srv.Client()

	_, err := p.Acquire(context.Background(), Request{MatchID: "m"})
	if err == nil {
		t.Fatal("an unauthorized reservation API looked like success")
	}
	if errors.Is(err, ErrNoServer) {
		t.Fatal("a broken API was reported as an empty one, which hides the problem")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want the status code in it", err)
	}
}

func TestRCONAddrPrefersTheRealAddress(t *testing.T) {
	// An SDR-fronted reservation gives players one address and admins another.
	s := &Server{Provider: "serveme", Connect: "sdr.valve:1234", Handle: "12345|10.0.0.9:27015"}
	if got := RCONAddr(s); got != "10.0.0.9:27015" {
		t.Fatalf("RCONAddr = %q, want the server's own address", got)
	}
	plain := &Server{Provider: "static", Connect: "10.0.0.1:27015"}
	if got := RCONAddr(plain); got != "10.0.0.1:27015" {
		t.Fatalf("RCONAddr = %q, want the connect address", got)
	}
}
