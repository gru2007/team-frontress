package gc

import (
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/players"
	"github.com/gru2007/team-frontress/services/gc/internal/quests"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
	"google.golang.org/protobuf/proto"
	"reflect"
	"testing"
	"time"
)

func TestDepartedMatchResultProgression(t *testing.T) {
	const departed, present = steamauth.MinIndividual + 101, steamauth.MinIndividual + 102
	for _, tc := range []struct {
		name                                                        string
		flags                                                       uint32
		kicked, serverKicked, allLeft, expired, credited, gcAbandon bool
	}{
		{name: "played leaver", flags: matchPlayerPlayed | 1, credited: true},
		{name: "late join leaver", flags: matchPlayerPlayed | 1 | 2, credited: true},
		{name: "abandon", flags: matchPlayerPlayed | matchPlayerAbandoned | 1},
		{name: "GC abandon before server flag", flags: matchPlayerPlayed | 1, gcAbandon: true},
		{name: "vote kick archived", flags: matchPlayerPlayed | 1, kicked: true},
		{name: "vote kick server reason", flags: matchPlayerPlayed | 1, serverKicked: true},
		{name: "never played", flags: 64},
		{name: "never active", flags: matchPlayerPlayed | matchPlayerNeverActive},
		{name: "last players left before queued result", flags: matchPlayerPlayed | 1, allLeft: true, credited: true},
		{name: "expired final result", flags: matchPlayerPlayed | 1, allLeft: true, expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
			g, err := New(Deps{Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			srv := g.sessions.Open(session.Server, 90071992547409921, func(s *session.Session) { s.ServerName = "test" })
			g.ServerConnected(srv)
			l := &Lobby{ID: 1, MatchID: 100, Group: casualGroup, State: lobbyRun, ServerID: srv.SteamID, FormedAt: now, StartedAt: now, everConnected: true}
			l.addMember(departed, mm.Red, 0, now)
			l.addMember(present, mm.Blu, 0, now)
			g.lobbies[l.ID] = l
			g.servers[srv.SteamID].LobbyID = l.ID
			g.archiveMatch(l, nil, "")
			if tc.kicked {
				l.kicked = map[uint64]bool{departed: true}
			}
			g.leaveLobby(l, departed, tc.gcAbandon)
			if tc.kicked {
				l.kicked = nil
			}
			if tc.allLeft {
				g.leaveLobby(l, present, false)
			}
			if tc.expired {
				now = now.Add(departedResultGrace + time.Second)
				g.tickLobbies()
			}
			msg := &gcpb.CMsgGC_Match_Result{MatchId: proto.Uint64(100), MatchGroup: proto.Int32(casualGroup), Status: gcpb.CMsgGC_Match_Result_MATCH_SUCCEEDED.Enum(), WinningTeam: proto.Uint32(2),
				Players: []*gcpb.CMsgGC_Match_Result_Player{
					{SteamId: proto.Uint64(departed), Team: proto.Uint32(2), Flags: proto.Uint32(tc.flags), Kills: proto.Uint32(21), Damage: proto.Uint32(5000), ClassesPlayed: proto.Uint32(1 << 1)},
					{SteamId: proto.Uint64(present), Team: proto.Uint32(3), Flags: proto.Uint32(matchPlayerPlayed)},
					{SteamId: proto.Uint64(departed), Team: proto.Uint32(2), Flags: proto.Uint32(matchPlayerPlayed)},
					{SteamId: proto.Uint64(departed + 1000), Team: proto.Uint32(2), Flags: proto.Uint32(matchPlayerPlayed)},
				}}
			if tc.serverKicked {
				msg.Players[0].LeaveReason = gcpb.TFMatchLeaveReason_TFMatchLeaveReason_VOTE_KICK.Enum()
			}
			raw, err := wire.Encode(uint32(gcpb.ETFGCMsg_k_EMsgGC_Match_Result), msg, wire.NoJob)
			if err != nil {
				t.Fatal(err)
			}
			other := g.sessions.Open(session.Server, srv.SteamID+1, nil)
			g.ServerConnected(other)
			g.Handle(other, raw)
			if g.players.Get(departed).Matches != 0 {
				t.Fatal("accepted another server's result")
			}
			g.Handle(srv, raw)
			rec := g.players.Get(departed)
			if tc.credited {
				if rec.Matches != 1 || rec.Wins != 1 || rec.XP() < players.XPPerMatch+players.XPWinBonus || rec.Rating <= players.DefaultRating {
					t.Fatalf("departed player not credited: %+v", rec)
				}
				progress := 0
				for _, q := range quests.For(departed, now, nil) {
					progress += rec.Quests[q.Key]
				}
				if progress == 0 {
					t.Fatal("no quest progress for played match")
				}
			} else if rec.Matches != 0 || rec.XP() != 0 || len(rec.Quests) != 0 {
				t.Fatalf("excluded player credited: %+v", rec)
			}
			archived, _ := g.history.Get("100")
			if tc.kicked {
				for _, member := range archived.Members {
					if member.SteamID == lobbyKey(departed) && !member.VoteKicked {
						t.Fatal("result lost archived vote-kick")
					}
				}
			}
			if !tc.expired {
				if archived.Result == nil || len(archived.Result.Players) != 2 {
					t.Fatalf("result roster: %+v", archived.Result)
				}
				if archived.Result.Players[0].Excluded == tc.credited {
					t.Fatal("wrong exclusion in history")
				}
				if tc.allLeft && (archived.State != "ended" || archived.EndedAt == nil) {
					t.Fatal("late result reopened closed history")
				}
			} else if archived.Result != nil {
				t.Fatal("accepted expired result")
			}
			g.Handle(srv, raw)
			if after := g.players.Get(departed); !reflect.DeepEqual(rec, after) {
				t.Fatal("replayed result changed progression")
			}
		})
	}
}
