package pool

import (
	"testing"
	"time"
)

func TestRegisterIsDedicatedAndTrusted(t *testing.T) {
	p := New(45 * time.Second)
	id, _, err := p.Register(Registration{ConnectAddress: "10.0.0.1:27015"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s, ok := p.Get(id)
	if !ok {
		t.Fatal("server not found after registration")
	}
	if s.Kind != KindDedicated || !s.Trusted {
		t.Fatalf("dedicated registration got Kind=%q Trusted=%v, want dedicated/trusted", s.Kind, s.Trusted)
	}
}

// TestRegisterElectedHostIsUntrustedAndAddressless is the security property
// the whole P2P design rests on: a player's session can never register itself
// as trusted infrastructure, and it does not need an address upfront the way
// a dedicated server does, because its own engine has not allocated one yet.
func TestRegisterElectedHostIsUntrustedAndAddressless(t *testing.T) {
	p := New(45 * time.Second)
	id, token, err := p.RegisterElectedHost(Registration{}, "match-1", time.Now())
	if err != nil {
		t.Fatalf("RegisterElectedHost with no address: %v", err)
	}
	s, ok := p.Get(id)
	if !ok {
		t.Fatal("server not found after RegisterElectedHost")
	}
	if s.Kind != KindP2P || s.Trusted {
		t.Fatalf("elected host got Kind=%q Trusted=%v, want p2p/untrusted", s.Kind, s.Trusted)
	}
	if s.Status != StatusReserved || s.MatchID != "match-1" {
		t.Fatalf("elected host is not reserved for its match: status=%s match=%s", s.Status, s.MatchID)
	}
	if s.ConnectAddress != "" {
		t.Fatalf("elected host has an address before SetConnectAddress: %q", s.ConnectAddress)
	}

	if err := p.SetConnectAddress(id, token, "169.254.1.2:27015"); err != nil {
		t.Fatalf("SetConnectAddress: %v", err)
	}
	s, _ = p.Get(id)
	if s.ConnectAddress != "169.254.1.2:27015" {
		t.Fatalf("address did not stick: %q", s.ConnectAddress)
	}

	if err := p.SetConnectAddress(id, "wrong-token", "1.2.3.4:1"); err != ErrBadToken {
		t.Fatalf("SetConnectAddress with the wrong token: got %v, want ErrBadToken", err)
	}
}

func TestRegisterElectedHostRequiresAMatchID(t *testing.T) {
	p := New(45 * time.Second)
	if _, _, err := p.RegisterElectedHost(Registration{}, "", time.Now()); err == nil {
		t.Fatal("RegisterElectedHost accepted an empty match id")
	}
}

// TestCompletedP2PHostIsNotReused ensures a one-battle listen server cannot be
// assigned a new roster while its owner is returning to the menu.
func TestCompletedP2PHostIsNotReused(t *testing.T) {
	p := New(45 * time.Second)
	now := time.Now()

	p2pID, p2pToken, err := p.RegisterElectedHost(Registration{}, "earlier-match", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetConnectAddress(p2pID, p2pToken, "169.254.1.2:27015"); err != nil {
		t.Fatal(err)
	}
	p.Release(p2pID, true) // back to idle, as if it had just hosted one battle

	dedicatedID, _, err := p.Register(Registration{ConnectAddress: "10.0.0.9:27015"}, now)
	if err != nil {
		t.Fatal(err)
	}

	best, err := p.Reserve("next-match", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if best.ID != dedicatedID {
		t.Fatalf("Reserve picked %q (kind %q), want the dedicated server %q", best.ID, best.Kind, dedicatedID)
	}

	// With the dedicated server taken there must be no reusable P2P member.
	if _, err := p.Reserve("another-match", "", now); err != ErrNoServer {
		t.Fatalf("completed P2P host was reusable: got %v, want ErrNoServer", err)
	}
	if _, ok := p.Get(p2pID); ok {
		t.Fatal("completed one-battle P2P server remained in the pool")
	}
}

// TestExpireSilentUsesTheShorterP2PTimeout confirms a player's machine going
// quiet is noticed sooner than a dedicated server's, per SetP2POfflineAfter.
func TestExpireSilentUsesTheShorterP2PTimeout(t *testing.T) {
	p := New(45 * time.Second)
	p.SetP2POfflineAfter(5 * time.Second)
	now := time.Now()

	dedicatedID, dedicatedToken, err := p.Register(Registration{ConnectAddress: "10.0.0.9:27015"}, now)
	if err != nil {
		t.Fatal(err)
	}
	p2pID, p2pToken, err := p.RegisterElectedHost(Registration{}, "m1", now)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.SetConnectAddress(p2pID, p2pToken, "169.254.1.2:27015")
	// Both go quiet at the same moment, 10 seconds ago: past the P2P timeout,
	// well inside the dedicated one.
	quiet := now.Add(-10 * time.Second)
	if err := p.Heartbeat(dedicatedID, dedicatedToken, StatusIdle, "", "", 0, quiet); err != nil {
		t.Fatal(err)
	}
	if err := p.Heartbeat(p2pID, p2pToken, StatusIdle, "", "", 0, quiet); err != nil {
		t.Fatal(err)
	}

	p.ExpireSilent(now)

	if s, _ := p.Get(p2pID); s.Status != StatusOffline {
		t.Fatalf("p2p server status = %s, want offline", s.Status)
	}
	if s, _ := p.Get(dedicatedID); s.Status == StatusOffline {
		t.Fatal("dedicated server was expired using the P2P timeout")
	}
}

// TestABootingServerIsNotExpiredByTheHeartbeatRule is the timeout that ended
// battles on the testbed before they ever started. A player-hosted server
// heartbeats from the game's own menu page, and the game is busy loading the
// map — so it goes quiet for exactly as long as the boot takes, which is
// exactly when the heartbeat rule was killing it. Silence during boot means
// nothing; the boot deadline is what bounds that phase.
func TestABootingServerIsNotExpiredByTheHeartbeatRule(t *testing.T) {
	start := time.Now()
	p := New(45 * time.Second)
	p.SetP2POfflineAfter(20 * time.Second)
	p.SetBootGrace(2 * time.Minute)

	id, token, err := p.RegisterElectedHost(Registration{
		Name: "a player", ConnectAddress: "", Capacity: 8,
	}, "b-1", start)
	if err != nil {
		t.Fatalf("register elected host: %v", err)
	}

	// Halfway through a slow map load: well past the P2P silence budget, well
	// inside the boot grace.
	if lost := p.ExpireSilent(start.Add(50 * time.Second)); len(lost) != 0 {
		t.Fatalf("a host still loading its map was declared gone after 50s: %v", lost)
	}
	if s, _ := p.Get(id); s.Status == StatusOffline {
		t.Fatal("a booting host was marked offline by the heartbeat rule")
	}

	// Once it is live, the ordinary rule applies again.
	if err := p.Heartbeat(id, token, StatusLive, "b-1", "koth_suijin", 4, start.Add(60*time.Second)); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	lost := p.ExpireSilent(start.Add(60*time.Second + 25*time.Second))
	if len(lost) != 1 || lost[0] != "b-1" {
		t.Fatalf("a live host that went silent for 25s should take its battle with it: %v", lost)
	}
}
