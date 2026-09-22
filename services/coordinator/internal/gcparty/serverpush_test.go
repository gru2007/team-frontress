package gcparty

import (
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// TestPushMatchRosterDeliversCSOTFGameServerLobby verifies Manager satisfies
// mm.GCServerPusher by actually pushing a CSOTFGameServerLobby addressed to
// the server's own SteamID -- the mechanism that replaced the fake
// tf_mm_match_begin/tf_mm_match_add RCON commands. This is intentionally the
// same shared-object type and EMsg family a client party already receives;
// the only thing that changes is who the push is addressed to.
func TestPushMatchRosterDeliversCSOTFGameServerLobby(t *testing.T) {
	mmk := newFakeMM()
	m := New(mmk, nil)

	const serverSteamID = 90071992547409921 // an arbitrary GS-flagged SteamID64
	spec := mm.ServerRosterSpec{
		MatchID:    "match-abc",
		MatchGroup: wire.MatchGroup(2),
		Map:        "cp_process_final",
		Connect:    "203.0.113.5:27015",
		Roster: []wire.AssignedPlayer{
			{SteamID: "76561198000000001", Name: "Red One", Team: wire.TeamRed},
			{SteamID: "76561198000000002", Name: "Blu One", Team: wire.TeamBlu},
		},
	}

	m.PushMatchRoster(serverSteamID, spec)

	pushes := drainNow(m, serverSteamID)
	if len(pushes) != 1 {
		t.Fatalf("got %d pushes, want exactly one SO push", len(pushes))
	}
	pu := pushes[0]
	if pu.EMsg != gcproto.EMsgSOUpdate {
		t.Fatalf("EMsg = %v, want EMsgSOUpdate", pu.EMsg)
	}
	if soSingleObjectTypeID(t, pu.Body) != gcproto.SOTypeTFGameServerLobby {
		t.Fatal("pushed SO is not SOTypeTFGameServerLobby")
	}

	lobby := decodeLobby(t, pu.Body)
	if lobby.ServerID == nil || *lobby.ServerID != serverSteamID {
		t.Fatalf("lobby.ServerID = %v, want %d (the server's own identity, not a player's)", lobby.ServerID, serverSteamID)
	}
	if lobby.Connect == nil || *lobby.Connect != spec.Connect {
		t.Fatalf("lobby.Connect = %v, want %q", lobby.Connect, spec.Connect)
	}
	if lobby.State == nil || gcproto.LobbyState(*lobby.State) != LobbyRun {
		t.Fatalf("lobby.State = %v, want LobbyRun", lobby.State)
	}
	if len(lobby.Members) != 2 {
		t.Fatalf("lobby has %d members, want the full 2-player roster", len(lobby.Members))
	}

	// A reconnecting server session must see the same lobby replayed in its
	// OnConnect snapshot, not just in the one push it might have missed.
	snapshot := m.OnConnect(serverSteamID, "")
	var sawInSnapshot bool
	for _, pu := range snapshot {
		if pu.EMsg == gcproto.EMsgSOCacheSubscribed {
			sawInSnapshot = containsLobbySO(t, pu.Body)
		}
	}
	if !sawInSnapshot {
		t.Fatal("a reconnecting server's OnConnect snapshot did not carry its current match lobby")
	}
}

func TestClearMatchRosterDestroysTheLobbySO(t *testing.T) {
	mmk := newFakeMM()
	m := New(mmk, nil)
	const serverSteamID = 90071992547409921

	m.PushMatchRoster(serverSteamID, mm.ServerRosterSpec{MatchID: "match-abc", Connect: "203.0.113.5:27015"})
	drainNow(m, serverSteamID) // clear the create/update push before asserting on the clear

	m.ClearMatchRoster(serverSteamID)
	pushes := drainNow(m, serverSteamID)
	if len(pushes) != 1 {
		t.Fatalf("got %d pushes on clear, want exactly one SODestroy", len(pushes))
	}
	if pushes[0].EMsg != gcproto.EMsgSODestroy {
		t.Fatalf("EMsg = %v, want EMsgSODestroy", pushes[0].EMsg)
	}
	if soSingleObjectTypeID(t, pushes[0].Body) != gcproto.SOTypeTFGameServerLobby {
		t.Fatal("destroyed SO is not SOTypeTFGameServerLobby")
	}

	// Clearing an already-cleared (or never-pushed) server is a silent no-op.
	m.ClearMatchRoster(serverSteamID)
	if pushes := drainNow(m, serverSteamID); len(pushes) != 0 {
		t.Fatalf("clearing twice pushed %d extra messages, want 0", len(pushes))
	}
}

func decodeLobby(t *testing.T, soBody []byte) *gcproto.GameServerLobby {
	t.Helper()
	r := gcproto.NewReader(soBody)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			t.Fatalf("bad SOSingleObject: %v", err)
		}
		if field == 3 { // object_data
			data, err := r.Bytes()
			if err != nil {
				t.Fatalf("bad object_data: %v", err)
			}
			lobby, err := gcproto.UnmarshalGameServerLobby(data)
			if err != nil {
				t.Fatalf("unmarshal lobby: %v", err)
			}
			return lobby
		}
		if err := r.Skip(wt); err != nil {
			t.Fatalf("skip: %v", err)
		}
	}
	t.Fatal("SOSingleObject carried no object_data")
	return nil
}

// containsLobbySO reports whether a marshaled SOCacheSubscribed (field 2,
// repeated SOCacheSubscribedType messages, each with an int32 TypeID at
// field 1) carries an entry for SOTypeTFGameServerLobby. There is no
// generated Unmarshal for SOCacheSubscribed (nothing needs to read one back
// in production, only build one), so this walks the wire format directly,
// the same way soSingleObjectTypeID above does for SOSingleObject.
func containsLobbySO(t *testing.T, subscribedBody []byte) bool {
	t.Helper()
	r := gcproto.NewReader(subscribedBody)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			t.Fatalf("bad SOCacheSubscribed: %v", err)
		}
		if field != 2 {
			if err := r.Skip(wt); err != nil {
				t.Fatalf("skip: %v", err)
			}
			continue
		}
		entry, err := r.Bytes()
		if err != nil {
			t.Fatalf("bad SubscribedType entry: %v", err)
		}
		if subscribedTypeIs(t, entry, gcproto.SOTypeTFGameServerLobby) {
			return true
		}
	}
	return false
}

func subscribedTypeIs(t *testing.T, entry []byte, want int32) bool {
	t.Helper()
	r := gcproto.NewReader(entry)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			t.Fatalf("bad SOCacheSubscribedType: %v", err)
		}
		if field == 1 {
			v, err := r.Varint()
			if err != nil {
				t.Fatalf("bad TypeID: %v", err)
			}
			return int32(v) == want
		}
		if err := r.Skip(wt); err != nil {
			t.Fatalf("skip: %v", err)
		}
	}
	return false
}
