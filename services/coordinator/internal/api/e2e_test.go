package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/api"
	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/steamauth"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// fakeSetup answers for RCON.
type fakeSetup struct{ players int }

func (f *fakeSetup) Setup(context.Context, *pool.Server, mm.Spec) error { return nil }
func (f *fakeSetup) AddPlayers(context.Context, *pool.Server, string, []wire.AssignedPlayer) error {
	return nil
}

func (f *fakeSetup) PlayerCount(context.Context, *pool.Server) (int, bool) {
	return f.players, true
}
func (f *fakeSetup) Teardown(context.Context, *pool.Server) error { return nil }

// TestTwoSoloPlayersGetAMatch drives the real HTTP API against the real
// matchmaker: two clients queue, poll, and are told to connect to the same
// server on opposite teams. It is the whole client protocol in one test.
func TestTwoSoloPlayersGetAMatch(t *testing.T) {
	cfg := config.Defaults()
	cfg.Secret = "s"
	cfg.MatchGroups = []config.MatchGroupConfig{{
		MatchGroup:   wire.MatchGroupCasual12v12,
		Name:         "Casual",
		Enabled:      true,
		MinPlayers:   2,
		IdealPlayers: 2,
		MaxPlayers:   4,
		Maps:         []string{"koth_product_final"},
	}}
	cfg.Pool.Providers = []config.ProviderConfig{{
		Kind:    "static",
		Servers: []config.StaticServer{{Name: "eu1", Connect: "10.0.0.1:27015", RCON: "r"}},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}

	srvPool, err := pool.New(cfg.Pool, pool.NewRegistry(time.Minute))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	matchmaker := mm.New(cfg, srvPool, &fakeSetup{players: 2}, nil, log)
	h := api.New(cfg, matchmaker, steamauth.DevVerifier{}, nil, nil, nil, log).Handler()

	queue := func(id string) string {
		t.Helper()
		body, _ := json.Marshal(wire.QueueRequest{
			MatchGroup: wire.MatchGroupCasual12v12,
			Leader:     wire.SteamID(id),
			Players:    []wire.QueuePlayer{{SteamID: wire.SteamID(id), Name: id}},
			Maps:       []string{"koth_product_final"},
		})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/queue", strings.NewReader(string(body))))
		if w.Code != http.StatusOK {
			t.Fatalf("queue %s: HTTP %d: %s", id, w.Code, w.Body)
		}
		var resp wire.QueueResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.TicketID
	}

	poll := func(ticket string) wire.QueueStatus {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/queue/"+ticket, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("poll %s: HTTP %d: %s", ticket, w.Code, w.Body)
		}
		var st wire.QueueStatus
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}

	a := queue("76561198000000001")
	b := queue("76561198000000002")

	// Tick and poll the way a client does, until both are assigned.
	var stA, stB wire.QueueStatus
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		matchmaker.Tick(context.Background())
		stA, stB = poll(a), poll(b)
		if stA.State == wire.QueueStateAssigned && stB.State == wire.QueueStateAssigned {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if stA.State != wire.QueueStateAssigned || stB.State != wire.QueueStateAssigned {
		t.Fatalf("states = %q / %q, want both assigned", stA.State, stB.State)
	}
	if stA.Assignment.MatchID != stB.Assignment.MatchID {
		t.Fatalf("different matches: %s vs %s", stA.Assignment.MatchID, stB.Assignment.MatchID)
	}
	if stA.Assignment.Connect != "10.0.0.1:27015" {
		t.Fatalf("connect = %q, want the pooled server", stA.Assignment.Connect)
	}
	if stA.Assignment.Password == "" {
		t.Error("the match server was handed out without a password")
	}
	if stA.Assignment.Map != "koth_product_final" {
		t.Errorf("map = %q, want the one both asked for", stA.Assignment.Map)
	}
	if stA.Assignment.Team == stB.Assignment.Team {
		t.Fatalf("both players were put on %s", stA.Assignment.Team)
	}
	if len(stA.Assignment.Roster) != 2 {
		t.Fatalf("roster has %d players, want 2", len(stA.Assignment.Roster))
	}

	// And the match ends when the server that ran it says so.
	res, _ := json.Marshal(wire.MatchResult{
		MatchID: stA.Assignment.MatchID, Secret: "s", Winner: wire.TeamBlu, BluScore: 3,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/gs/result", strings.NewReader(string(res))))
	if w.Code != http.StatusNoContent {
		t.Fatalf("result: HTTP %d: %s", w.Code, w.Body)
	}
	if matchmaker.LiveMatches() != 0 {
		t.Fatalf("live matches = %d after the result, want 0", matchmaker.LiveMatches())
	}
	if srvPool.Free() != 1 {
		t.Fatalf("free servers = %d after the match, want 1", srvPool.Free())
	}
}
