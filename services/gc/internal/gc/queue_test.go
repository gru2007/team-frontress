package gc

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func TestBackfillWaitsForServerCapacityAndKeepsReconnectSeat(t *testing.T) {
	now := time.Now()
	grp := config.MatchGroupConfig{MatchGroup: 7, Enabled: true, MinPlayers: 2, MaxPlayers: 6}
	g, err := New(Deps{Config: config.Config{MatchGroups: []config.MatchGroupConfig{grp}}})
	if err != nil {
		t.Fatal(err)
	}
	s := g.sessions.Open(session.Server, 100, nil)
	s.Connect = "127.0.0.1:27015"
	g.ServerConnected(s)
	l := &Lobby{ID: 1, MatchID: 2, Group: grp.MatchGroup, Map: "arena_granary", MaxPlayers: 6,
		State: lobbySetup, FormedAt: now}
	l.addMember(10, mm.Red, 0, now)
	l.addMember(11, mm.Blu, 0, now)
	g.lobbies[l.ID] = l
	g.bindServer(l, g.servers[s.SteamID])

	send := func(kind gcpb.ETFGCMsg, msg proto.Message) {
		t.Helper()
		raw, err := wire.Encode(uint32(kind), msg, wire.NoJob)
		if err != nil {
			t.Fatal(err)
		}
		g.Handle(s, raw)
	}
	report := &gcpb.CMsgGameServerMatchmakingStatus{
		MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_ACTIVE_MATCH.Enum(),
		Map:              proto.String(l.Map),
		MatchGroup:       proto.Int32(l.Group),
		LobbyMmVersion:   proto.Uint32(l.MMVersion),
		Players: []*gcpb.CMsgGameServerMatchmakingStatus_Player{
			{SteamId: proto.Uint64(10), ConnectState: gcpb.CMsgGameServerMatchmakingStatus_CONNECTED.Enum()},
			{SteamId: proto.Uint64(11), ConnectState: gcpb.CMsgGameServerMatchmakingStatus_RESERVED.Enum()},
		},
	}
	heartbeat := func() { send(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus, report) }
	heartbeat()
	if l.State != lobbyRun {
		t.Fatal("match did not go live")
	}

	player := g.player(12)
	player.Online = true
	party := g.newParty(12)
	party.Queues[grp.MatchGroup] = now
	g.matchmake()
	if l.member(12) != nil {
		t.Fatal("backfilled before the server requested a late join")
	}

	report.MatchmakingState = gcpb.ServerMatchmakingState_ServerMatchmakingState_ACTIVE_MATCH_REQUESTING_LATE_JOIN.Enum()
	report.LobbyMmVersion = proto.Uint32(l.MMVersion)
	report.SlotsFree = proto.Uint32(1)
	report.Players[0].ConnectState = gcpb.CMsgGameServerMatchmakingStatus_RESERVED.Enum()
	heartbeat()
	g.matchmake()
	if l.member(10).Connect != gcpb.CTFLobbyPlayerProto_DISCONNECTED {
		t.Fatal("lost the disconnected player's reservation")
	}
	if mem := l.member(12); mem == nil || mem.Connect != gcpb.CTFLobbyPlayerProto_RESERVATION_PENDING {
		t.Fatal("did not reserve a seat for the queued player")
	}
	heartbeat()
	if g.lateJoinServer(l) != nil {
		t.Fatal("an older heartbeat reopened a seat already assigned")
	}

	client := g.sessions.Open(session.Client, 12, nil)
	exit, err := wire.Encode(uint32(gcpb.ETFGCMsg_k_EMsgGCExitMatchmaking),
		&gcpb.CMsgExitMatchmaking{LobbyId: proto.Uint64(l.ID), ExplicitAbandon: proto.Bool(true)}, wire.NoJob)
	if err != nil {
		t.Fatal(err)
	}
	g.Handle(client, exit)
	g.Handle(client, exit)
	if l.member(12) != nil || g.players.Get(12).Abandons != 1 {
		t.Fatal("explicit exit did not release the seat and record one abandon")
	}

	report.LobbyMmVersion = proto.Uint32(l.MMVersion)
	heartbeat()
	party.Queues[grp.MatchGroup] = now
	g.matchmake()

	// A delayed server acknowledgement of the exit cannot cancel the new reservation.
	send(gcpb.ETFGCMsg_k_EMsgGCPlayerLeftMatch, &gcpb.CMsgPlayerLeftMatch{
		MatchId: proto.Uint64(l.MatchID), LobbyId: proto.Uint64(l.ID), SteamId: proto.Uint64(12),
		LeaveReason: gcpb.TFMatchLeaveReason_TFMatchLeaveReason_GC_REMOVED.Enum(), WasAbandon: proto.Bool(true),
	})
	if l.member(12) == nil || g.players.Get(12).Abandons != 1 {
		t.Fatal("an acknowledgement of an old exit affected the new reservation")
	}
}
