package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/steamauth"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// fakeMM records what the handlers asked for.
type fakeMM struct {
	enqueued  []*mm.Ticket
	enqueErr  error
	cancelled []string
	status    wire.QueueStatus
	statusErr error
	observed  []struct {
		matchID string
		players int
	}
	results []wire.MatchResult
}

func (f *fakeMM) Enqueue(t *mm.Ticket) (*mm.Ticket, error) {
	if f.enqueErr != nil {
		return nil, f.enqueErr
	}
	t.ID = "ticket-1"
	f.enqueued = append(f.enqueued, t)
	return t, nil
}
func (f *fakeMM) Cancel(id string) error { f.cancelled = append(f.cancelled, id); return nil }
func (f *fakeMM) Status(string) (wire.QueueStatus, error) {
	return f.status, f.statusErr
}
func (f *fakeMM) QueuedPlayers() map[wire.MatchGroup]int {
	return map[wire.MatchGroup]int{wire.MatchGroupCasual12v12: 3}
}
func (f *fakeMM) OpenMatches() map[wire.MatchGroup]int {
	return map[wire.MatchGroup]int{wire.MatchGroupCasual12v12: 1}
}
func (f *fakeMM) FreeServers() int { return 3 }
func (f *fakeMM) Population() int  { return 5 }
func (f *fakeMM) LiveMatches() int { return 1 }
func (f *fakeMM) ObserveServer(matchID string, players int) {
	f.observed = append(f.observed, struct {
		matchID string
		players int
	}{matchID, players})
}
func (f *fakeMM) ReportResult(_ context.Context, res wire.MatchResult) error {
	f.results = append(f.results, res)
	return nil
}

func newTestAPI(t *testing.T) (*fakeMM, http.Handler) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Secret = "server-secret"
	m := &fakeMM{}
	s := New(cfg, m, steamauth.DevVerifier{}, pool.NewRegistry(0), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return m, s.Handler()
}

func post(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const (
	steamA = "76561198000000001"
	steamB = "76561198000000002"
)

func TestQueueAcceptsAParty(t *testing.T) {
	m, h := newTestAPI(t)

	w := post(t, h, http.MethodPost, "/v1/queue", wire.QueueRequest{
		MatchGroup: wire.MatchGroupCasual12v12,
		Leader:     steamA,
		Players: []wire.QueuePlayer{
			{SteamID: steamA, Name: "a", Ticket: "deadbeef"},
			{SteamID: steamB, Name: "b"},
		},
		Maps: []string{"koth_product_final"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var resp wire.QueueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TicketID == "" {
		t.Fatal("no ticket id was returned")
	}
	if len(m.enqueued) != 1 {
		t.Fatalf("enqueued %d tickets, want 1", len(m.enqueued))
	}
	if got := len(m.enqueued[0].Players); got != 2 {
		t.Fatalf("party size = %d, want 2", got)
	}
}

func TestQueueRejectsANonSteamID(t *testing.T) {
	_, h := newTestAPI(t)
	w := post(t, h, http.MethodPost, "/v1/queue", wire.QueueRequest{
		MatchGroup: wire.MatchGroupCasual12v12,
		Leader:     "1",
		Players:    []wire.QueuePlayer{{SteamID: "1"}},
	})
	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want a refusal: %s", w.Code, w.Body)
	}
}

func TestQueueRejectsALeaderWhoIsNotInTheParty(t *testing.T) {
	_, h := newTestAPI(t)
	w := post(t, h, http.MethodPost, "/v1/queue", wire.QueueRequest{
		MatchGroup: wire.MatchGroupCasual12v12,
		Leader:     steamA,
		Players:    []wire.QueuePlayer{{SteamID: steamB}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body)
	}
}

func TestQueueDeduplicatesRepeatedMembers(t *testing.T) {
	m, h := newTestAPI(t)
	w := post(t, h, http.MethodPost, "/v1/queue", wire.QueueRequest{
		MatchGroup: wire.MatchGroupCasual12v12,
		Leader:     steamA,
		Players: []wire.QueuePlayer{
			{SteamID: steamA}, {SteamID: steamA}, {SteamID: steamB},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if got := len(m.enqueued[0].Players); got != 2 {
		t.Fatalf("party size = %d, want 2: a repeated member inflated the party", got)
	}
}

func TestServerEndpointsNeedTheSecret(t *testing.T) {
	cases := []struct {
		path string
		body any
	}{
		{"/v1/gs/register", wire.ServerRegistration{Connect: "10.0.0.1:27015", Secret: "wrong"}},
		{"/v1/gs/heartbeat", wire.ServerHeartbeat{Connect: "10.0.0.1:27015", Secret: ""}},
		{"/v1/gs/result", wire.MatchResult{MatchID: "m", Secret: "nope"}},
	}
	for _, tc := range cases {
		_, h := newTestAPI(t)
		w := post(t, h, http.MethodPost, tc.path, tc.body)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", tc.path, w.Code)
		}
	}
}

func TestServerRegistersThenHeartbeatsIntoTheMatch(t *testing.T) {
	m, h := newTestAPI(t)

	if w := post(t, h, http.MethodPost, "/v1/gs/register", wire.ServerRegistration{
		Name: "eu1", Connect: "10.0.0.1:27015", Secret: "server-secret",
	}); w.Code != http.StatusNoContent {
		t.Fatalf("register status = %d: %s", w.Code, w.Body)
	}

	if w := post(t, h, http.MethodPost, "/v1/gs/heartbeat", wire.ServerHeartbeat{
		Connect: "10.0.0.1:27015", Secret: "server-secret", MatchID: "match-7", Players: 8,
	}); w.Code != http.StatusNoContent {
		t.Fatalf("heartbeat status = %d: %s", w.Code, w.Body)
	}
	if len(m.observed) != 1 || m.observed[0].matchID != "match-7" || m.observed[0].players != 8 {
		t.Fatalf("the heartbeat did not reach the match: %+v", m.observed)
	}
}

func TestHeartbeatFromAnUnknownServerAsksItToRegister(t *testing.T) {
	_, h := newTestAPI(t)
	w := post(t, h, http.MethodPost, "/v1/gs/heartbeat", wire.ServerHeartbeat{
		Connect: "10.9.9.9:27015", Secret: "server-secret",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestStatusIsPublic(t *testing.T) {
	_, h := newTestAPI(t)
	w := post(t, h, http.MethodGet, "/v1/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var st wire.Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.OnlinePlayers != 5 || st.LiveMatches != 1 || st.FreeServers != 3 {
		t.Fatalf("status = %+v, want the matchmaker's numbers", st)
	}
	if len(st.MatchGroups) == 0 {
		t.Error("no match groups were advertised")
	}
	if st.QueuedPlayers["7"] != 3 {
		t.Errorf("queued players = %v, want casual 12v12 (7) to be 3", st.QueuedPlayers)
	}
}

func TestMalformedBodyIsRefused(t *testing.T) {
	_, h := newTestAPI(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/queue", bytes.NewReader([]byte("{not json")))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
