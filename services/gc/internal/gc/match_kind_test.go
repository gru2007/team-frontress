package gc

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/history"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/players"
	"github.com/gru2007/team-frontress/services/gc/internal/quests"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/state"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// Casual matches, ranked matches and war battles all move rating, experience
// and quests the same way; only a war battle counts towards war quests.
func TestMatchResultCreditsEveryKind(t *testing.T) {
	const winner, loser = steamauth.MinIndividual + 1, steamauth.MinIndividual + 2
	// A day on which the winner has a war quest, so it is seen to move or not.
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	warQuest := ""
	for warQuest == "" {
		now = now.Add(24 * time.Hour)
		for _, q := range quests.For(winner, now, nil) {
			if q.Kind == quests.WarWins {
				warQuest = q.Key
			}
		}
	}

	for _, tc := range []struct {
		kind  string
		group int32
		war   *state.War
	}{
		{history.KindCasual, casualGroup, nil},
		{history.KindRanked, ladderGroup, nil},
		{history.KindWar, casualGroup, &state.War{Campaign: "old-campaign", NodeID: "n1", NodeName: "Rust Valley"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			g, err := New(Deps{Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			srv := g.sessions.Open(session.Server, 90071992547409921, func(s *session.Session) { s.ServerName = "test" })
			g.ServerConnected(srv)
			l := &Lobby{ID: 1, MatchID: 100, Group: tc.group, State: lobbyRun, ServerID: srv.SteamID,
				FormedAt: now, StartedAt: now, War: tc.war}
			l.addMember(winner, mm.Red, 0, now)
			l.addMember(loser, mm.Blu, 0, now)
			g.lobbies[l.ID] = l
			g.servers[srv.SteamID].LobbyID = l.ID

			played := uint32(matchPlayerPlayed)
			raw, err := wire.Encode(uint32(gcpb.ETFGCMsg_k_EMsgGC_Match_Result), &gcpb.CMsgGC_Match_Result{
				MatchId: proto.Uint64(l.MatchID), MatchGroup: proto.Int32(tc.group),
				Status:      gcpb.CMsgGC_Match_Result_MATCH_SUCCEEDED.Enum(),
				WinningTeam: proto.Uint32(2), RedScore: proto.Uint32(1),
				Players: []*gcpb.CMsgGC_Match_Result_Player{
					{SteamId: proto.Uint64(winner), Team: proto.Uint32(2), Flags: proto.Uint32(played), Kills: proto.Uint32(7)},
					{SteamId: proto.Uint64(loser), Team: proto.Uint32(3), Flags: proto.Uint32(played)},
				},
			}, wire.NoJob)
			if err != nil {
				t.Fatal(err)
			}
			g.Handle(srv, raw)

			w, lo := g.players.Get(winner), g.players.Get(loser)
			if w.Matches != 1 || w.Wins != 1 || lo.Matches != 1 || lo.Losses != 1 {
				t.Fatalf("match not recorded: winner %+v loser %+v", w, lo)
			}
			if w.Rating <= players.DefaultRating || lo.Rating >= players.DefaultRating {
				t.Fatalf("rating did not move: winner %d loser %d", w.Rating, lo.Rating)
			}
			if w.XP() < players.XPPerMatch+players.XPWinBonus || lo.XP() < players.XPPerMatch {
				t.Fatalf("experience: winner %d loser %d", w.XP(), lo.XP())
			}
			wantWar := 0
			if tc.kind == history.KindWar {
				wantWar = 1
			}
			if got := w.Quests[warQuest]; got != wantWar {
				t.Fatalf("war quest progress %d, want %d", got, wantWar)
			}
			m, _ := g.history.Get("100")
			if m.Kind != tc.kind || m.Result == nil {
				t.Fatalf("archive: %+v", m)
			}
			for _, p := range m.Result.Players {
				if p.Excluded || p.RatingChange == 0 || p.XPChange == 0 {
					t.Fatalf("archived result row: %+v", p)
				}
			}
		})
	}
}
