package gc

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/state"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func TestServerTeamChangesDoNotTerminateWarMatches(t *testing.T) {
	for _, battle := range []bool{false, true} {
		for _, departed := range []bool{false, true} {
			g, err := New(Deps{})
			if err != nil {
				t.Fatal(err)
			}
			s := g.sessions.Open(session.Server, 100, nil)
			g.ServerConnected(s)
			l := &Lobby{ID: 1, MatchID: 2, Group: casualGroup, State: lobbyRun, ServerID: s.SteamID}
			if battle {
				l.War = &state.War{AttackerTeam: int32(mm.Red)}
			}
			l.addMember(10, mm.Red, 0, time.Now())
			l.addMember(11, mm.Red, 0, time.Now())
			g.lobbies[l.ID] = l
			g.servers[s.SteamID].LobbyID = l.ID
			// A client exit can arrive before the server's team-change job.
			if departed {
				g.dropMember(l, 10)
			}
			req := &gcpb.CMsgGCChangeMatchPlayerTeamsRequest{
				LobbyId: proto.Uint64(l.ID), MatchId: proto.Uint64(l.MatchID),
				Member: []*gcpb.CMsgGCChangeMatchPlayerTeamsRequest_Member{
					{MemberId: proto.Uint64(10), NewTeam: gcpb.TF_GC_TEAM_TF_GC_TEAM_INVADERS.Enum()},
					{MemberId: proto.Uint64(11), NewTeam: gcpb.TF_GC_TEAM_TF_GC_TEAM_INVADERS.Enum()},
				},
			}
			check := func(want bool) {
				t.Helper()
				raw, err := wire.Encode(uint32(gcpb.ETFGCMsg_k_EMsgGC_ChangeMatchPlayerTeamsRequest), req, 123)
				if err != nil {
					t.Fatal(err)
				}
				g.Handle(s, raw)
				frames, err := s.Poll(0, 0, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				for i := len(frames) - 1; i >= 0; i-- {
					msg, err := wire.Decode(frames[i].Data)
					if err != nil {
						t.Fatal(err)
					}
					if msg.Type != uint32(gcpb.ETFGCMsg_k_EMsgGC_ChangeMatchPlayerTeamsResponse) {
						continue
					}
					var reply gcpb.CMsgGCChangeMatchPlayerTeamsResponse
					if err := proto.Unmarshal(msg.Body, &reply); err != nil {
						t.Fatal(err)
					}
					if reply.GetSuccess() != want {
						t.Fatalf("war=%v departed=%v: success=%v, want %v", battle, departed, reply.GetSuccess(), want)
					}
					return
				}
				t.Fatal("missing team-change reply")
			}
			check(true)
			if l.member(11).Team != mm.Blu || (departed && l.member(10) != nil) {
				t.Fatal("team change lost or departed player resurrected")
			}
			// Reject malformed batches atomically and stale match IDs.
			req.Member[0].NewTeam = gcpb.TF_GC_TEAM_TF_GC_TEAM_DEFENDERS.Enum()
			req.Member[1].NewTeam = gcpb.TF_GC_TEAM_TF_GC_TEAM_NOTEAM.Enum()
			check(false)
			if l.member(11).Team != mm.Blu || (!departed && l.member(10).Team != mm.Blu) {
				t.Fatal("invalid batch partially changed teams")
			}
			req.MatchId = proto.Uint64(999)
			check(false)
		}
	}
}

func TestServerReconnectOnlyResumesItsCurrentMatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		matchID *uint64
		resume  bool
		setup   bool
	}{
		{"legacy", nil, true, false},
		{"transport reconnect", proto.Uint64(2), true, false},
		{"new process", proto.Uint64(0), false, false},
		{"different match", proto.Uint64(3), false, false},
		{"rolling transition reconnect", proto.Uint64(1), true, true},
		{"previous match after go live", proto.Uint64(1), false, false},
		{"fresh process during transition", proto.Uint64(0), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := New(Deps{})
			if err != nil {
				t.Fatal(err)
			}
			s := g.sessions.Open(session.Server, 100, nil)
			g.ServerConnected(s)
			l := &Lobby{ID: 1, MatchID: 2, Group: casualGroup, State: lobbyRun, ServerID: s.SteamID}
			l.PreviousMatchID = 1
			if tc.setup {
				l.State = lobbySetup
			}
			l.addMember(10, mm.Red, 0, time.Now())
			g.lobbies[l.ID] = l
			g.servers[s.SteamID].LobbyID = l.ID
			g.publishLobby(l)
			next := g.sessions.Open(session.Server, 100, func(s *session.Session) { s.MatchID = tc.matchID })
			lobbyID := g.ServerConnected(next)
			if tc.resume {
				if lobbyID != l.ID || g.lobbies[l.ID] != l || g.servers[100].Session != next {
					t.Fatal("running match lost on transport reconnect")
				}
			} else if lobbyID != 0 || g.lobbies[l.ID] != nil || g.servers[100].LobbyID != 0 || g.servers[100].Ready {
				t.Fatal("new process was rebound to an unplayable RUN lobby")
			}
		})
	}
}

// Teardown and stale-roster heartbeats must not make a new lobby live or
// overwrite reservations/connections belonging to its current roster.
func TestServerHeartbeatRejectsStaleAndDisconnectState(t *testing.T) {
	for _, rolling := range []bool{false, true} {
		for _, phase := range []string{"teardown", "stale roster", "current"} {
			t.Run(phase+map[bool]string{false: "/initial", true: "/rolling"}[rolling], func(t *testing.T) {
				g, err := New(Deps{})
				if err != nil {
					t.Fatal(err)
				}
				s := g.sessions.Open(session.Server, 100, nil)
				g.ServerConnected(s)
				g.servers[s.SteamID].Connect = "127.0.0.1:27015"
				l := &Lobby{ID: 1, MatchID: 2, MMVersion: 3, Group: casualGroup, State: lobbySetup, ServerID: s.SteamID, Map: "ctf_2fort"}
				if rolling {
					l.PreviousMatchID = 1
				}
				l.addMember(10, mm.Red, 0, time.Now())
				before := l.member(10).Connect
				g.lobbies[l.ID] = l
				g.servers[s.SteamID].LobbyID = l.ID
				status := &gcpb.CMsgGameServerMatchmakingStatus{
					MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_ACTIVE_MATCH_REQUESTING_LATE_JOIN.Enum(),
					Map:              proto.String(l.Map), MatchGroup: proto.Int32(l.Group), LobbyMmVersion: proto.Uint32(l.MMVersion), SlotsFree: proto.Uint32(8),
					Players: []*gcpb.CMsgGameServerMatchmakingStatus_Player{{SteamId: proto.Uint64(10), ConnectState: gcpb.CMsgGameServerMatchmakingStatus_CONNECTED.Enum()}},
				}
				if phase == "teardown" {
					status.GameState = gcpb.TF_GC_GameState_TF_GC_GAMESTATE_DISCONNECT.Enum()
				}
				if phase == "stale roster" {
					status.LobbyMmVersion = proto.Uint32(2)
				}
				raw, err := wire.Encode(uint32(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus), status, wire.NoJob)
				if err != nil {
					t.Fatal(err)
				}
				g.Handle(s, raw)
				if phase == "current" {
					if l.State != lobbyRun || l.member(10).Connect != gcpb.CTFLobbyPlayerProto_CONNECTED {
						t.Fatal("current heartbeat did not start match")
					}
				} else if l.State != lobbySetup || l.member(10).Connect != before || g.servers[s.SteamID].LateJoinSlots != 0 {
					t.Fatal("obsolete heartbeat modified new match")
				}
			})
		}
	}
}
