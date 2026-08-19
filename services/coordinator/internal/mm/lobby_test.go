package mm

import (
	"context"
	"log/slog"
	"path/filepath"
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
	if err := m.ServerState(id, match.ID, "ready", 4); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := m.ServerState(id, match.ID, "live", 4); err != nil {
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
