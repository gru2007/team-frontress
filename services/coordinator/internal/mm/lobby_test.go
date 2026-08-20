package mm

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/greyline-frontress/coordinator/internal/hostelect"
	"github.com/greyline-frontress/coordinator/internal/pool"
	"github.com/greyline-frontress/coordinator/internal/war"
)

// newTestMaker builds a matchmaker over the real theater, sized for 2v2 with
// no hold-out wait, which is the smallest thing that forms a battle.
func newTestMaker(t *testing.T) (*Matchmaker, *pool.Pool) {
	t.Helper()
	theater, err := war.LoadTheater(filepath.Join("..", "..", "theater.industrial.json"))
	if err != nil {
		t.Fatalf("theater: %v", err)
	}
	wlog, err := war.OpenLog(filepath.Join(t.TempDir(), "war.jsonl"))
	if err != nil {
		t.Fatalf("war log: %v", err)
	}
	engine, err := war.NewEngine(theater, wlog, war.DefaultRules())
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	servers := pool.New(45 * time.Second)
	cfg := DefaultConfig()
	cfg.MinTeamSize = 2
	cfg.TeamSizes = []int{2}
	cfg.FormWait = 0
	return New(cfg, slog.New(slog.DiscardHandler), engine, servers), servers
}

func deploy(t *testing.T, m *Matchmaker, steamID uint64, side war.Side, contract bool) *Player {
	t.Helper()
	p := m.Hello(steamID, "merc", side, "", hostelect.Capabilities{})
	if _, err := m.Deploy(p, "", contract, ""); err != nil {
		t.Fatalf("deploy %d: %v", steamID, err)
	}
	return p
}

// runningBattle forms one battle on a dedicated server and takes it all the
// way to live, which is the state a latecomer would be joining.
func runningBattle(t *testing.T, m *Matchmaker, servers *pool.Pool) (*Match, string, string) {
	t.Helper()
	id, token, err := servers.Register(pool.Registration{
		Name: "dedi", ConnectAddress: "10.0.0.1:27015", Capacity: 24,
	}, time.Now())
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	deploy(t, m, 1, war.SideRed, true)
	deploy(t, m, 2, war.SideBlu, true)
	deploy(t, m, 3, war.SideRed, true)
	deploy(t, m, 4, war.SideBlu, true)
	m.Tick()

	var match *Match
	for _, candidate := range m.matches {
		match = candidate
	}
	if match == nil {
		t.Fatal("no battle formed from four queued players")
	}
	// Room to grow, whichever battlefield the war picked.
	match.Plan.MaxPlayers = 8
	// Take the assignment off the mailbox the way a real agent would, so what
	// is left there afterwards is only what happened next.
	if _, ok, err := servers.Poll(context.Background(), id, token, time.Second); err != nil || !ok {
		t.Fatalf("the battle was never handed to its server (ok=%v, err=%v)", ok, err)
	}
	if err := m.ServerState(id, match.ID, "ready", 4, ""); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := m.ServerState(id, match.ID, "live", 4, ""); err != nil {
		t.Fatalf("live: %v", err)
	}
	return match, id, token
}

// TestARunningBattleTakesTheNextPlayerWhoDeploys is lobby expansion: somebody
// who deploys thirty seconds after a battle formed joins that battle rather
// than waiting for a second one to fill up beside it.
func TestARunningBattleTakesTheNextPlayerWhoDeploys(t *testing.T) {
	m, servers := newTestMaker(t)
	match, serverID, token := runningBattle(t, m, servers)
	before := len(match.Slots)

	late := deploy(t, m, 5, war.SideRed, true)
	m.Tick()

	if late.State != StateAssigned {
		t.Fatalf("a latecomer was left in the queue beside a battle with room: state = %s", late.State)
	}
	if late.MatchID != match.ID {
		t.Fatalf("latecomer joined %q, want the running battle %q", late.MatchID, match.ID)
	}
	if len(match.Slots) != before+1 {
		t.Fatalf("roster = %d slots, want %d", len(match.Slots), before+1)
	}

	// They must be told where to go, or joining is theoretical.
	var ready *MatchInfo
	for _, ev := range late.drain(0) {
		if ev.Type == EventMatchReady {
			ready = ev.Match
		}
	}
	if ready == nil || ready.Connect == "" {
		t.Fatal("latecomer was seated without being sent a match_ready with a connect address")
	}

	// And the server has to know who is arriving, or it has no idea which
	// team the war put them on.
	cmd, ok, err := servers.Poll(context.Background(), serverID, token, time.Second)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !ok || cmd.Type != pool.CommandRoster || len(cmd.Roster) != 1 || cmd.Roster[0].SteamID != 5 {
		t.Fatalf("server was not told about the new player: %+v (ok=%v)", cmd, ok)
	}
}

func TestRosterDeliveryFailureLeavesLatecomerQueued(t *testing.T) {
	m, servers := newTestMaker(t)
	match, serverID, _ := runningBattle(t, m, servers)
	before := len(match.Slots)

	// Fill the bounded mailbox so the next roster update cannot be queued.
	for i := 0; i < 4; i++ {
		if err := servers.Send(serverID, pool.Command{Type: pool.CommandRoster, MatchID: match.ID}); err != nil {
			t.Fatalf("fill mailbox %d: %v", i, err)
		}
	}

	late := deploy(t, m, 50, war.SideRed, true)
	m.Tick()
	if late.State != StateQueued || late.MatchID != "" {
		t.Fatalf("latecomer was sent despite failed roster delivery: state=%s match=%q", late.State, late.MatchID)
	}
	if len(match.Slots) != before {
		t.Fatalf("failed roster delivery changed slots from %d to %d", before, len(match.Slots))
	}
}

// TestARunningBattleStopsTakingPlayersAtCapacity keeps expansion honest: the
// battlefield's own limit is the limit.
func TestARunningBattleStopsTakingPlayersAtCapacity(t *testing.T) {
	m, servers := newTestMaker(t)
	match, _, _ := runningBattle(t, m, servers)
	match.Plan.MaxPlayers = len(match.Slots)

	late := deploy(t, m, 5, war.SideRed, true)
	m.Tick()

	if late.MatchID == match.ID {
		t.Fatal("a player was pushed into a battle that was already full")
	}
}

// TestALatecomerOnlyCrossesSidesUnderAContract: growing a battle must not
// quietly conscript somebody onto the side they did not sign up for. The only
// room here is on the thin side, and this player refused a contract, so they
// wait rather than get moved.
func TestALatecomerOnlyCrossesSidesUnderAContract(t *testing.T) {
	m, servers := newTestMaker(t)
	match, _, _ := runningBattle(t, m, servers)

	red, blu := match.sideCounts()
	if red == blu {
		match.Slots = append(match.Slots, &Slot{SteamID: 99, Side: war.SideRed, Team: war.SideRed})
		red++
	}
	heavier := war.SideRed
	if blu > red {
		heavier = war.SideBlu
	}

	late := deploy(t, m, 5, heavier, false)
	m.Tick()

	if late.MatchID == match.ID {
		t.Fatalf("a player who refused a contract was moved off %s to balance a battle", heavier)
	}
}

// TestSomebodyWhoDeploysDuringABootJoinsThatBattle is the "seamless" half of
// lobby expansion. A map takes half a minute to load; anybody who deploys in
// that half minute used to watch from the queue until there were enough of
// them to start a second battle beside the first.
func TestSomebodyWhoDeploysDuringABootJoinsThatBattle(t *testing.T) {
	m, servers := newTestMaker(t)
	id, token, err := servers.Register(pool.Registration{
		Name: "dedi", ConnectAddress: "10.0.0.1:27015", Capacity: 24,
	}, time.Now())
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	for steamID := uint64(1); steamID <= 4; steamID++ {
		side := war.SideRed
		if steamID%2 == 0 {
			side = war.SideBlu
		}
		deploy(t, m, steamID, side, true)
	}
	m.Tick()

	var match *Match
	for _, candidate := range m.matches {
		match = candidate
	}
	if match == nil || match.State != MatchBooting {
		t.Fatalf("expected a battle loading its map, got %v", match)
	}
	match.Plan.MaxPlayers = 8
	if _, ok, err := servers.Poll(context.Background(), id, token, time.Second); err != nil || !ok {
		t.Fatalf("the battle was never handed to its server (ok=%v, err=%v)", ok, err)
	}
	before := len(match.Slots)

	late := deploy(t, m, 5, war.SideRed, true)
	m.Tick()

	if late.MatchID != match.ID {
		t.Fatalf("a player who deployed during the boot was left in the queue (state %s, match %q)",
			late.State, late.MatchID)
	}
	if len(match.Slots) != before+1 {
		t.Fatalf("roster = %d, want %d", len(match.Slots), before+1)
	}

	// They must not be handed a connect address for a server that is still
	// loading — that is the silent "connection never happens" failure.
	for _, ev := range late.drain(0) {
		if ev.Type == EventMatchReady {
			t.Fatal("a latecomer was sent to a battle whose server had not reported ready")
		}
		if ev.Type == EventMatchState && ev.Match != nil && ev.Match.Connect != "" {
			t.Fatal("a battle still booting handed out a connect address")
		}
	}

	// And when it does come up, they are on the roster that gets sent there.
	if err := m.ServerState(id, match.ID, "ready", 5, ""); err != nil {
		t.Fatalf("ready: %v", err)
	}
	sawReady := false
	for _, ev := range late.drain(0) {
		if ev.Type == EventMatchReady && ev.Match != nil && ev.Match.Connect != "" {
			sawReady = true
		}
	}
	if !sawReady {
		t.Fatal("the latecomer was never sent to the battle they had been seated in")
	}
}

// TestAnAssignmentCarriesTheMuster: the server has to be told to hold the
// round, or a battle starts around whoever loaded the map fastest.
func TestAnAssignmentCarriesTheMuster(t *testing.T) {
	m, servers := newTestMaker(t)
	match, _, _ := runningBattle(t, m, servers)
	as := m.assignmentFor(match)
	if as.MinPlayers != len(match.Slots) {
		t.Fatalf("assignment waits for %d players, roster is %d", as.MinPlayers, len(match.Slots))
	}
	if as.MusterTimeoutS <= 0 {
		t.Fatal("an assignment with no muster timeout would hold a battle for a player who never comes")
	}
	if as.MaxPlayers < as.MinPlayers {
		t.Fatalf("capacity %d is below the roster %d", as.MaxPlayers, as.MinPlayers)
	}
}

// TestTheRosterGateFollowsWhetherIdentitiesAreProven is the failure this cost
// an evening: the game server was told to turn away anybody not on the roster,
// while the coordinator was running dev auth — where a client states whichever
// SteamID it likes, so the roster names accounts the server will never see and
// "not on the roster" is everybody.
func TestTheRosterGateFollowsWhetherIdentitiesAreProven(t *testing.T) {
	m, servers := newTestMaker(t)
	match, _, _ := runningBattle(t, m, servers)

	if as := m.assignmentFor(match); as.VerifiedIdentities {
		t.Fatal("a coordinator that trusts whatever a client claims must not tell a " +
			"server to turn people away for not matching its roster")
	}

	m.cfg.VerifiedIdentities = true
	if as := m.assignmentFor(match); !as.VerifiedIdentities {
		t.Fatal("with proven identities the roster is worth gating on, and the " +
			"assignment must say so")
	}
}

// TestAFailedBattleKeepsTheReasonTheServerGave: when a battle a player was
// standing on ends, the coordinator's log is the only account anybody gets of
// why. Replacing what the server actually said with a sentence of our own
// turns every distinct failure into the same unhelpful line.
func TestAFailedBattleKeepsTheReasonTheServerGave(t *testing.T) {
	m, servers := newTestMaker(t)
	match, serverID, _ := runningBattle(t, m, servers)

	if err := m.ServerState(serverID, match.ID, "failed", 0,
		"listen server has no Steam FakeIP"); err != nil {
		t.Fatalf("failed state: %v", err)
	}
	if match.State != MatchAborted {
		t.Fatalf("a server that gave up left the battle in %s", match.State)
	}

	var told string
	for _, s := range match.Slots {
		p := m.players[s.SteamID]
		if p == nil {
			continue
		}
		for _, ev := range p.drain(0) {
			if ev.Type == EventQueue && ev.Queue != nil && ev.Queue.Message != "" {
				told = ev.Queue.Message
			}
		}
		break
	}
	if !strings.Contains(told, "no Steam FakeIP") {
		t.Fatalf("the roster was told %q, which does not contain what the server said", told)
	}
}
