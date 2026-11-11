package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/history"
)

func TestPortalLiveViewAndAdminEndMatch(t *testing.T) {
	cfg := testConfig()
	cfg.AdminKey = strings.Repeat("k", 32)
	_, base, stop := startGCWith(t, cfg, nil)
	defer stop()

	do := func(method, path, body string, auth bool, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer "+cfg.AdminKey)
		}
		req.Header.Set("X-Frontress-Actor", "76561197960265729")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	if code := do("GET", "/admin/v1/live", "", false, nil); code != 401 {
		t.Fatal("live view without a key", code)
	}
	if code := do("GET", "/v1/players?sort=rating", "", false, nil); code != 400 {
		t.Fatal("unknown sort accepted", code)
	}
	var players struct {
		Players []map[string]any `json:"players"`
		Total   int              `json:"total"`
	}
	if code := do("GET", "/v1/players", "", false, &players); code != 200 || players.Players == nil || players.Total != 0 {
		t.Fatal("empty player list", code, players)
	}

	gs := newPeer(t, base, gsID)
	gs.server()
	gs.send(msg(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus), &gcpb.CMsgGameServerMatchmakingStatus{
		MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_EMPTY.Enum(),
	})
	a, b := newPeer(t, base, alice), newPeer(t, base, bob)
	a.client()
	b.client()
	for _, p := range []*peer{a, b} {
		p.until("a party", func() bool { return p.party() != nil })
		p.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch), &gcpb.CMsgPartyQueueForMatch{
			MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
		})
	}
	a.until("a lobby", func() bool { return a.lobby() != nil })
	matchID := strconv.FormatUint(a.lobby().GetMatchId(), 10)

	var live struct {
		Servers []struct {
			State   string `json:"state"`
			MatchID string `json:"match_id"`
		} `json:"servers"`
		Matches []struct {
			MatchID string `json:"match_id"`
			Members []any  `json:"members"`
		} `json:"matches"`
	}
	if code := do("GET", "/admin/v1/live", "", true, &live); code != 200 {
		t.Fatal("live view", code)
	}
	if len(live.Servers) != 1 || live.Servers[0].State != "busy" || live.Servers[0].MatchID != matchID ||
		len(live.Matches) != 1 || live.Matches[0].MatchID != matchID || len(live.Matches[0].Members) != 2 {
		t.Fatalf("live view: %+v", live)
	}

	if code := do("POST", "/admin/v1/matches/"+matchID+"/end", `{"reason":""}`, true, nil); code != 400 {
		t.Fatal("ended without a reason", code)
	}
	if code := do("POST", "/admin/v1/matches/123/end", `{"reason":"test"}`, true, nil); code != 404 {
		t.Fatal("ended a match that does not exist", code)
	}
	if code := do("POST", "/admin/v1/matches/"+matchID+"/end", `{"reason":"server crashed"}`, true, nil); code != 200 {
		t.Fatal("end match", code)
	}
	a.until("the lobby gone", func() bool { return a.lobby() == nil })
	var archived history.Match
	if code := do("GET", "/v1/matches/"+matchID, "", false, &archived); code != 200 ||
		archived.State != "ended" || !strings.Contains(archived.Reason, "server crashed") {
		t.Fatalf("archived match: %d %+v", code, archived)
	}
}
