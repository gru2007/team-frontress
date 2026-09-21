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
	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/gcwire"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/players"
	"github.com/gru2007/team-frontress/services/coordinator/internal/steamauth"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

type fakeMM struct{}

func (*fakeMM) Enqueue(t *mm.Ticket) (*mm.Ticket, error)             { t.ID = "ticket-1"; return t, nil }
func (*fakeMM) Cancel(string) error                                  { return nil }
func (*fakeMM) Status(string) (wire.QueueStatus, error)              { return wire.QueueStatus{}, nil }
func (*fakeMM) MatchAssignment(string) (*wire.Assignment, bool)      { return nil, false }
func (*fakeMM) ReportResult(context.Context, wire.MatchResult) error { return nil }
func (*fakeMM) QueuedPlayers() map[wire.MatchGroup]int {
	return map[wire.MatchGroup]int{wire.MatchGroupCasual12v12: 3}
}
func (*fakeMM) OpenMatches() map[wire.MatchGroup]int {
	return map[wire.MatchGroup]int{wire.MatchGroupCasual12v12: 1}
}
func (*fakeMM) FreeServers() int { return 3 }
func (*fakeMM) Population() int  { return 5 }
func (*fakeMM) LiveMatches() int { return 1 }

func newTestAPI(t *testing.T) http.Handler {
	t.Helper()
	cfg := config.Defaults()
	cfg.Secret = "server-secret"
	rec, err := players.New("")
	if err != nil {
		t.Fatalf("players: %v", err)
	}
	s := New(cfg, &fakeMM{}, steamauth.DevVerifier{}, nil, rec,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s.Handler()
}

func request(t *testing.T, h http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestStatusIsPublic(t *testing.T) {
	w := request(t, newTestAPI(t), http.MethodGet, "/v1/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var st wire.Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.OnlinePlayers != 5 || st.LiveMatches != 1 || st.FreeServers != 3 || st.QueuedPlayers["7"] != 3 {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestPlayerProgressIsServed(t *testing.T) {
	h := newTestAPI(t)
	rr := request(t, h, http.MethodGet, "/v1/player/76561198000000001", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	var got wire.PlayerProgress
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Level != 1 || got.XP != 0 || got.LevelXPTotal <= 0 {
		t.Fatalf("unexpected new-player progress: %+v", got)
	}
	if rr := request(t, h, http.MethodGet, "/v1/player/not-a-steamid", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad SteamID status = %d, want 400", rr.Code)
	}
}

func TestLegacyJSONControlPlaneIsGone(t *testing.T) {
	h := newTestAPI(t)
	for _, path := range []string{"/v1/queue", "/v1/gs/register", "/v1/gs/heartbeat", "/v1/gs/result"} {
		if got := request(t, h, http.MethodPost, path, []byte("{}")); got.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, got.Code)
		}
	}
}

func TestGCExchangeRunsThroughHTTP(t *testing.T) {
	hello, err := gcwire.Encode(4006, nil, &gcproto.CMsgClientHello{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(gcwire.ExchangeRequest{
		Protocol: 1,
		Role:     "client",
		SteamID:  "76561198000000001",
		Messages: []gcwire.Message{hello},
	})
	if err != nil {
		t.Fatal(err)
	}
	rr := request(t, newTestAPI(t), http.MethodPost, "/v1/gc/exchange", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body)
	}
	var response gcwire.ExchangeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Connected || response.SessionID == "" || len(response.Messages) != 2 {
		t.Fatalf("unexpected GC response: %+v", response)
	}
	packet, err := gcwire.Decode(response.Messages[0])
	if err != nil || packet.Type != 4004 {
		t.Fatalf("first GC reply = type %d, err %v", packet.Type, err)
	}
}
