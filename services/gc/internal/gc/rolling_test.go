package gc

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/history"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func TestRollingMatchPersistsNewIDAndIgnoresOldHeartbeat(t *testing.T) {
	now := time.Now()
	grp := config.MatchGroupConfig{MatchGroup: casualGroup, Enabled: true, MinPlayers: 2, MaxPlayers: 6,
		Maps: []string{"cp_badlands", "cp_granary", "cp_process", "cp_snakewater"}}
	g, err := New(Deps{Config: config.Config{MatchGroups: []config.MatchGroupConfig{grp},
		MapIndex: map[string]uint32{"cp_badlands": 1, "cp_granary": 2, "cp_process": 3, "cp_snakewater": 4}}})
	if err != nil {
		t.Fatal(err)
	}
	s := g.sessions.Open(session.Server, 100, nil)
	s.Connect = "127.0.0.1:27015"
	g.ServerConnected(s)
	l := &Lobby{ID: 1, MatchID: 2, Group: grp.MatchGroup, Map: "cp_badlands", MaxPlayers: 6,
		State: lobbyRun, FormedAt: now, StartedAt: now, resultSeen: true}
	l.addMember(10, mm.Red, 0, now)
	l.member(10).Connect = gcpb.CTFLobbyPlayerProto_CONNECTED
	l.NextMaps = g.nextMapOptions(l, grp)
	g.lobbies[l.ID] = l
	g.bindServer(l, g.servers[s.SteamID])
	g.archiveMatch(l, &history.Result{}, "")
	send := func(kind gcpb.ETFGCMsg, body proto.Message) {
		t.Helper()
		raw, err := wire.Encode(uint32(kind), body, wire.NoJob)
		if err != nil {
			t.Fatal(err)
		}
		g.Handle(s, raw)
	}

	// The default when nobody votes is the current map.
	request := &gcpb.CMsgGCNewMatchForLobbyRequest{
		CurrentMatchId: proto.Uint64(l.MatchID), LobbyId: proto.Uint64(l.ID), NextMapId: proto.Uint32(1),
	}
	send(gcpb.ETFGCMsg_k_EMsgGC_NewMatchForLobbyRequest, request)
	next := g.lobbies[l.ID]
	if next.MatchID == l.MatchID || next.State != lobbySetup || next.member(10) == nil || next.ServerID != l.ServerID {
		t.Fatalf("invalid successor: %+v", next)
	}
	saved, err := g.store.Load()
	if err != nil || len(saved) != 1 || saved[0].MatchID != next.MatchID {
		t.Fatalf("successor was not saved: %v (%v)", saved, err)
	}
	send(gcpb.ETFGCMsg_k_EMsgGC_NewMatchForLobbyRequest, request)
	if g.lobbies[l.ID] != next {
		t.Fatal("retry allocated a second successor")
	}

	status := &gcpb.CMsgGameServerMatchmakingStatus{
		MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_ACTIVE_MATCH.Enum(),
		Map:              proto.String(next.Map), MatchGroup: proto.Int32(next.Group), LobbyMmVersion: proto.Uint32(l.MMVersion),
	}
	send(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus, status)
	if next.State != lobbySetup {
		t.Fatal("heartbeat from the old match made the successor joinable")
	}
	status.LobbyMmVersion = proto.Uint32(next.MMVersion)
	send(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus, status)
	if next.State != lobbyRun {
		t.Fatal("the successor did not go live")
	}
}
