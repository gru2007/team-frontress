package mm

import (
	"testing"
	"time"

	"github.com/greyline-frontress/coordinator/internal/hostelect"
	"github.com/greyline-frontress/coordinator/internal/pool"
	"github.com/greyline-frontress/coordinator/internal/war"
)

func hostCapable() hostelect.Capabilities {
	return hostelect.Capabilities{
		CanHost: true, UploadKbps: 20000, CPUScore: 90, MemoryMB: 16384,
		MaxPlayersSupported: 12,
	}
}

// deployHost is deploy() for a client that says it can run a battle itself.
func deployHost(t *testing.T, m *Matchmaker, steamID uint64, side war.Side) *Player {
	t.Helper()
	p := m.Hello(steamID, "merc", side, "", hostCapable())
	if _, err := m.Deploy(p, "", true, ""); err != nil {
		t.Fatalf("deploy %d: %v", steamID, err)
	}
	return p
}

// awaitingHost forms one battle with no dedicated server in the pool, which is
// the path that elects a player to host it.
func awaitingHost(t *testing.T, m *Matchmaker) *Match {
	t.Helper()
	for id := uint64(1); id <= 4; id++ {
		side := war.SideRed
		if id%2 == 0 {
			side = war.SideBlu
		}
		deployHost(t, m, id, side)
	}
	m.Tick()
	for _, match := range m.matches {
		if match.State == MatchAwaitingHost {
			return match
		}
	}
	t.Fatal("no host was offered when the pool had nothing free")
	return nil
}

func p2pAwaitingEvidence(t *testing.T, m *Matchmaker, servers *pool.Pool) (*Match, *Player, string, string) {
	t.Helper()
	match := awaitingHost(t, m)
	host := m.players[match.HostSteamID]
	serverID, token, err := m.ConfirmHost(host, match.ID, pool.Registration{
		Name: "test host", Capacity: 12, GameVersion: "test",
	})
	if err != nil {
		t.Fatalf("confirm host: %v", err)
	}
	if err := servers.SetConnectAddress(serverID, token, "169.254.2.3:27015"); err != nil {
		t.Fatalf("set address: %v", err)
	}
	if err := m.ServerState(serverID, match.ID, "ready", 4, ""); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := m.ServerState(serverID, match.ID, "live", 4, ""); err != nil {
		t.Fatalf("live: %v", err)
	}
	if update, err := m.ServerResult(serverID, war.BattleResult{
		BattleID: match.ID, Outcome: war.OutcomeRedWin, RedScore: 3, BluScore: 1,
	}); err != nil || update != nil {
		t.Fatalf("host result: update=%v err=%v", update, err)
	}
	for _, slot := range match.Slots {
		if slot.SteamID != match.HostSteamID {
			return match, m.players[slot.SteamID], serverID, token
		}
	}
	t.Fatal("P2P battle had no non-host witness")
	return nil, nil, "", ""
}

// TestAHostThatIgnoresAnOfferIsNotOfferedTheNextOne is the loop the testbed
// walked into: one client that cannot answer wins the election, the offer
// times out, the roster goes straight back in the queue, and the same client
// wins again on the next tick — forever, while everyone else on the front sits
// at "already in a battle" and cannot even re-deploy.
func TestAHostThatIgnoresAnOfferIsNotOfferedTheNextOne(t *testing.T) {
	m, _ := newTestMaker(t)
	now := time.Now()
	m.now = func() time.Time { return now }

	first := awaitingHost(t, m)
	ignored := first.HostSteamID
	if ignored == 0 {
		t.Fatal("a battle awaiting a host with no host on it")
	}

	// Nobody answers. The offer times out.
	now = now.Add(m.cfg.HostAcceptDeadline + time.Second)
	m.Tick()
	if first.State != MatchAborted {
		t.Fatalf("an unanswered host offer left the battle in %s", first.State)
	}

	// The next formation must not be the same offer to the same machine.
	for _, p := range m.players {
		if p.SteamID == ignored {
			continue
		}
		if p.State != StateQueued {
			t.Fatalf("player %d was left in %s after the abort instead of back in the queue", p.SteamID, p.State)
		}
	}
	if p := m.players[ignored]; p != nil && p.State == StateQueued {
		t.Fatalf("the host that ignored the offer was put straight back in the queue it will be re-elected from")
	}

	now = now.Add(time.Second)
	m.Tick()
	for _, match := range m.matches {
		if match.State == MatchAwaitingHost && match.HostSteamID == ignored {
			t.Fatal("the same unresponsive machine was elected host again on the next tick")
		}
	}
}

// TestDeployLeavesABattleThatNeverStarted: pressing DEPLOY while stuck in a
// battle nobody has been sent to is the player saying "find me another one",
// and it used to come back "already in a battle" for as long as the front kept
// re-forming around them.
func TestDeployLeavesABattleThatNeverStarted(t *testing.T) {
	m, _ := newTestMaker(t)
	match := awaitingHost(t, m)

	var stuck *Player
	for _, s := range match.Slots {
		if s.SteamID != match.HostSteamID {
			stuck = m.players[s.SteamID]
			break
		}
	}
	if stuck == nil {
		t.Fatal("a formed battle with nobody on it but the host")
	}

	if _, err := m.Deploy(stuck, "", true, ""); err != nil {
		t.Fatalf("DEPLOY out of a battle that never started: %v", err)
	}
	if stuck.State != StateQueued {
		t.Fatalf("player is %s after re-deploying, want queued", stuck.State)
	}
	if stuck.MatchID != "" {
		t.Fatalf("player is still attached to battle %q", stuck.MatchID)
	}
}

// TestDeployWillNotWalkOutOfARunningBattle is the other half: an escape hatch
// out of a battle that is actually being played is not an escape hatch, it is
// a way to leave a game in progress without saying so.
func TestDeployWillNotWalkOutOfARunningBattle(t *testing.T) {
	m, servers := newTestMaker(t)
	match, _, _ := runningBattle(t, m, servers)

	p := m.players[match.Slots[0].SteamID]
	if _, err := m.Deploy(p, "", true, ""); err == nil {
		t.Fatal("DEPLOY pulled a player out of a live battle")
	}
}

// TestReconnectingCannotChangeSidesMidBattle: allegiance is a between-battles
// decision. SetSide has always refused during a battle; hello used to accept
// one anyway, so a crash, a settings save or any second hello could change the
// side the player's own roster slot was written against — and the battle would
// then be counted for a side they were not fighting for.
func TestReconnectingCannotChangeSidesMidBattle(t *testing.T) {
	m, servers := newTestMaker(t)
	match, _, _ := runningBattle(t, m, servers)

	p := m.players[match.Slots[0].SteamID]
	was := p.Side
	other := war.SideBlu
	if was == war.SideBlu {
		other = war.SideRed
	}

	if again := m.Hello(p.SteamID, p.Name, other, "", hostelect.Capabilities{}); again.Side != was {
		t.Fatalf("a hello mid-battle changed allegiance from %s to %s", was, again.Side)
	}
	if err := m.SetSide(p, other); err == nil {
		t.Fatal("SetSide accepted a change during a battle")
	}

	// And once the battle is over it is their choice again.
	m.abort(match, "test")
	if again := m.Hello(p.SteamID, p.Name, other, "", hostelect.Capabilities{}); again.Side != other {
		t.Fatalf("allegiance stayed %s after the battle ended; it is a choice between battles", again.Side)
	}
}

func TestPolicyViolationTaintsP2PResultAndPenalizesHost(t *testing.T) {
	m, servers := newTestMaker(t)
	match, witness, _, _ := p2pAwaitingEvidence(t, m, servers)
	revision := m.war.Revision()
	if update, err := m.RecordResultEvidence(witness, ResultEvidence{
		MatchID: match.ID, Outcome: war.OutcomeRedWin, RedScore: 3, BluScore: 1,
		PolicyViolation: true, ViolationReason: "sv_cheats",
	}); err != nil || update != nil {
		t.Fatalf("tainted evidence: update=%v err=%v", update, err)
	}
	if match.State != MatchFinished || !match.Tainted || match.Resolution != "tainted" {
		t.Fatalf("tainted match did not finish safely: state=%s tainted=%v resolution=%q",
			match.State, match.Tainted, match.Resolution)
	}
	if m.war.Revision() != revision {
		t.Fatal("tainted P2P result changed the war")
	}
	if got := m.hostHistoryLocked(match.HostSteamID).PolicyTaints; got != 1 {
		t.Fatalf("host policy taints = %d, want 1", got)
	}
}

func TestMismatchedP2PResultIsRejectedAndPenalizesHost(t *testing.T) {
	m, servers := newTestMaker(t)
	match, witness, serverID, _ := p2pAwaitingEvidence(t, m, servers)
	revision := m.war.Revision()

	if update, err := m.RecordResultEvidence(witness, ResultEvidence{
		MatchID: match.ID, Outcome: war.OutcomeBluWin, RedScore: 1, BluScore: 3,
	}); err != nil || update != nil {
		t.Fatalf("mismatched evidence: update=%v err=%v", update, err)
	}
	if match.State != MatchFinished || !match.Tainted || match.Resolution != "tainted" {
		t.Fatalf("mismatch did not finish safely: state=%s tainted=%v resolution=%q",
			match.State, match.Tainted, match.Resolution)
	}
	if m.war.Revision() != revision {
		t.Fatal("mismatched P2P result changed the war")
	}
	if got := m.hostHistoryLocked(match.HostSteamID).ResultMismatches; got != 1 {
		t.Fatalf("host result mismatches = %d, want 1", got)
	}
	if _, exists := servers.Get(serverID); exists {
		t.Fatal("finished P2P server remained reusable in the pool")
	}
}

func TestP2PResultWithoutWitnessTimesOutUncounted(t *testing.T) {
	m, servers := newTestMaker(t)
	now := time.Now()
	m.now = func() time.Time { return now }
	match, _, serverID, token := p2pAwaitingEvidence(t, m, servers)
	revision := m.war.Revision()

	now = match.ResultDeadline.Add(time.Second)
	if err := servers.Heartbeat(serverID, token, pool.StatusLive, match.ID, match.Plan.Map, 4, now); err != nil {
		t.Fatalf("keep P2P host alive through evidence deadline: %v", err)
	}
	m.Tick()
	if match.State != MatchFinished || match.Tainted || match.Resolution != "unwitnessed" {
		t.Fatalf("unwitnessed result did not finish safely: state=%s tainted=%v resolution=%q",
			match.State, match.Tainted, match.Resolution)
	}
	if m.war.Revision() != revision {
		t.Fatal("unwitnessed P2P result changed the war")
	}
	if got := m.hostHistoryLocked(match.HostSteamID).ResultTimeouts; got != 1 {
		t.Fatalf("host result timeouts = %d, want 1", got)
	}
	if _, exists := servers.Get(serverID); exists {
		t.Fatal("timed-out P2P server remained reusable in the pool")
	}
}
