package mm

import (
	"context"
	"sync"
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// fakeGCPusher stands in for gcparty.Manager, recording what would have been
// pushed over a server's GC session without needing the real SO/EMsg wiring.
type fakeGCPusher struct {
	mu      sync.Mutex
	pushes  []ServerRosterSpec
	pushIDs []uint64
	cleared []uint64
}

func (f *fakeGCPusher) PushMatchRoster(serverSteamID uint64, spec ServerRosterSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushIDs = append(f.pushIDs, serverSteamID)
	f.pushes = append(f.pushes, spec)
}

func (f *fakeGCPusher) ClearMatchRoster(serverSteamID uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, serverSteamID)
}

func (f *fakeGCPusher) lastPush(t *testing.T) (uint64, ServerRosterSpec) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pushes) == 0 {
		t.Fatal("no roster was ever pushed over GC")
	}
	return f.pushIDs[len(f.pushIDs)-1], f.pushes[len(f.pushes)-1]
}

func (f *fakeGCPusher) pushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pushes)
}

func (f *fakeGCPusher) clearCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cleared)
}

// withServerIdentity wires connect -> steamID into the config the way an
// operator would in coordinator.json's gc.server_identities.
func withServerIdentity(cfg *Matchmaker, connect, steamID string) {
	if cfg.cfg.GC.ServerIdentities == nil {
		cfg.cfg.GC.ServerIdentities = map[string]string{}
	}
	cfg.cfg.GC.ServerIdentities[connect] = steamID
}

func TestBootPushesRosterOverGCNotRCON(t *testing.T) {
	m, setup, _ := newTestMM(t, testConfig(2, 2, 4, 0), 1)
	pusher := &fakeGCPusher{}
	m.SetGCPusher(pusher)
	withServerIdentity(m, "10.0.0.1:27015", "76561198000000999")

	a := party(t, m, "7656119801000000", 1)
	party(t, m, "7656119802000000", 1)
	settle(m)

	if a.matchID == "" {
		t.Fatal("match was not formed")
	}
	if setup.lastSpec(t).Roster == nil {
		t.Fatal("sanity: RCON setup should still receive the spec for stock convars")
	}

	steamID, spec := pusher.lastPush(t)
	if steamID != 76561198000000999 {
		t.Fatalf("pushed to steamID %d, want the configured server identity", steamID)
	}
	if spec.MatchID != a.matchID {
		t.Fatalf("pushed match %q, want %q", spec.MatchID, a.matchID)
	}
	if spec.Connect != "10.0.0.1:27015" {
		t.Fatalf("pushed connect %q, want the server's address", spec.Connect)
	}
	if len(spec.Roster) != 2 {
		t.Fatalf("pushed roster has %d players, want 2", len(spec.Roster))
	}
}

func TestBootSkipsGCPushForUnconfiguredServer(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(2, 2, 4, 0), 1)
	pusher := &fakeGCPusher{}
	m.SetGCPusher(pusher)
	// Deliberately no ServerIdentities entry for 10.0.0.1:27015.

	party(t, m, "7656119801000000", 1)
	party(t, m, "7656119802000000", 1)
	settle(m)

	if n := pusher.pushCount(); n != 0 {
		t.Fatalf("pushed %d rosters for a server with no configured GC identity, want 0", n)
	}
}

func TestBackfillPushesUpdatedFullRoster(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(2, 2, 4, 0), 1)
	pusher := &fakeGCPusher{}
	m.SetGCPusher(pusher)
	withServerIdentity(m, "10.0.0.1:27015", "76561198000000999")

	a := party(t, m, "7656119803000000", 1)
	party(t, m, "7656119804000000", 1)
	settle(m)
	if a.matchID == "" {
		t.Fatal("initial match was not formed")
	}
	afterBoot := pusher.pushCount()

	pending, err := m.Enqueue(&Ticket{
		MatchGroup:     wire.MatchGroupCasual12v12,
		Leader:         "7656119805000000",
		Players:        []wire.AssignedPlayer{{SteamID: "7656119805000000"}},
		StandbyMatchID: a.matchID,
	})
	if err != nil {
		t.Fatalf("standby enqueue: %v", err)
	}
	waitAssigned(t, m, pending)

	if pusher.pushCount() <= afterBoot {
		t.Fatal("backfill did not push an updated roster over GC")
	}
	_, spec := pusher.lastPush(t)
	if len(spec.Roster) != 3 {
		t.Fatalf("backfilled push carries %d players, want the full roster of 3", len(spec.Roster))
	}
}

func TestTeardownClearsGCRoster(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(2, 2, 4, 0), 1)
	pusher := &fakeGCPusher{}
	m.SetGCPusher(pusher)
	withServerIdentity(m, "10.0.0.1:27015", "76561198000000999")

	a := party(t, m, "7656119806000000", 1)
	party(t, m, "7656119807000000", 1)
	settle(m)
	if a.matchID == "" {
		t.Fatal("match was not formed")
	}

	if err := m.ReportResult(context.Background(), wire.MatchResult{
		MatchID: a.matchID,
		Winner:  wire.TeamRed,
	}); err != nil {
		t.Fatalf("report result: %v", err)
	}

	if n := pusher.clearCount(); n != 1 {
		t.Fatalf("cleared %d GC rosters on teardown, want 1", n)
	}
}

// RCONSetup.AddPlayers is now a deliberate no-op: seating a backfill happens
// over GC (see gcpusher.go), not a bespoke RCON command that only one of our
// own servers could answer.
func TestRCONAddPlayersNoLongerSendsAnything(t *testing.T) {
	setup := &RCONSetup{}
	if err := setup.AddPlayers(context.Background(), nil, "m1", roster("76561198000000001")); err != nil {
		t.Fatalf("AddPlayers = %v, want nil (it must not try to dial RCON at all)", err)
	}
}
