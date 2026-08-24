package mm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/war"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// fakeSetup stands in for RCON.
type fakeSetup struct {
	mu       sync.Mutex
	setups   []Spec
	fail     error
	players  int
	askedFor int
	teardown int
}

func (f *fakeSetup) Setup(_ context.Context, _ *pool.Server, spec Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.setups = append(f.setups, spec)
	return nil
}

func (f *fakeSetup) PlayerCount(context.Context, *pool.Server) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.askedFor++
	return f.players, true
}

func (f *fakeSetup) Teardown(context.Context, *pool.Server) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardown++
	return nil
}

func (f *fakeSetup) lastSpec(t *testing.T) Spec {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.setups) == 0 {
		t.Fatal("no server was ever set up")
	}
	return f.setups[len(f.setups)-1]
}

func testConfig(min, ideal, max, patient int) config.Config {
	cfg := config.Defaults()
	cfg.Secret = "s"
	cfg.MatchGroups = []config.MatchGroupConfig{{
		MatchGroup:   wire.MatchGroupCasual12v12,
		Name:         "Casual",
		Enabled:      true,
		MinPlayers:   min,
		IdealPlayers: ideal,
		MaxPlayers:   max,
		PatientSecs:  patient,
		Maps:         []string{"koth_product_final", "cp_process_final"},
		ServerConfig: "server_casual",
	}}
	return cfg
}

func testPool(servers int) *pool.Pool {
	var list []config.StaticServer
	for i := 0; i < servers; i++ {
		list = append(list, config.StaticServer{
			Name:    fmt.Sprintf("srv%d", i),
			Connect: fmt.Sprintf("10.0.0.%d:27015", i+1),
			RCON:    "rcon",
		})
	}
	p := &pool.Pool{}
	p.AddProvider(pool.NewStatic(config.ProviderConfig{Kind: "static", Servers: list}))
	return p
}

func newTestMM(t *testing.T, cfg config.Config, servers int) (*Matchmaker, *fakeSetup, *time.Time) {
	t.Helper()
	setup := &fakeSetup{}
	clock := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	m := New(cfg, testPool(servers), setup, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.now = func() time.Time { return clock }
	n := 0
	m.newID = func() string { n++; return fmt.Sprintf("id%d", n) }
	return m, setup, &clock
}

func roster(ids ...string) []wire.AssignedPlayer {
	out := make([]wire.AssignedPlayer, 0, len(ids))
	for _, id := range ids {
		out = append(out, wire.AssignedPlayer{SteamID: wire.SteamID(id)})
	}
	return out
}

func party(t *testing.T, m *Matchmaker, leader string, size int, maps ...string) *Ticket {
	t.Helper()
	var ids []string
	for i := 0; i < size; i++ {
		ids = append(ids, fmt.Sprintf("%s%d", leader, i))
	}
	tk, err := m.Enqueue(&Ticket{
		MatchGroup: wire.MatchGroupCasual12v12,
		Leader:     wire.SteamID(ids[0]),
		Players:    roster(ids...),
		Maps:       maps,
	})
	if err != nil {
		t.Fatalf("enqueue %s: %v", leader, err)
	}
	return tk
}

// settle runs a tick and waits for the boot goroutines it started.
func settle(m *Matchmaker) {
	m.Tick(context.Background())
	// boot() runs in its own goroutine; poll briefly for it to publish.
	for i := 0; i < 200; i++ {
		m.mu.Lock()
		pending := false
		for _, mt := range m.matches {
			if mt.state == msBooting {
				pending = true
			}
		}
		m.mu.Unlock()
		if !pending {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFormsAMatchOnceIdealIsReached(t *testing.T) {
	m, setup, _ := newTestMM(t, testConfig(4, 8, 12, 60), 1)

	a := party(t, m, "7656119800000000", 4)
	b := party(t, m, "7656119811111111", 4)
	settle(m)

	for _, tk := range []*Ticket{a, b} {
		st, err := m.Status(tk.ID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if st.State != wire.QueueStateAssigned {
			t.Fatalf("ticket %s state = %q, want assigned", tk.ID, st.State)
		}
		if st.Assignment == nil || st.Assignment.Connect == "" {
			t.Fatalf("ticket %s has no connect details", tk.ID)
		}
	}

	spec := setup.lastSpec(t)
	if spec.ServerConfig != "server_casual" {
		t.Errorf("server config = %q, want server_casual", spec.ServerConfig)
	}
	if spec.Map == "" {
		t.Error("the match was set up with no map")
	}
	if spec.Password == "" {
		t.Error("the match server was left without a password")
	}
}

// A small population is the whole point: four players must eventually get a
// 2v2 rather than wait forever for the eighth.
func TestSettlesForMinPlayersAfterPatience(t *testing.T) {
	m, _, clock := newTestMM(t, testConfig(4, 12, 24, 60), 1)

	a := party(t, m, "7656119800000000", 2)
	b := party(t, m, "7656119811111111", 2)

	settle(m)
	if st, _ := m.Status(a.ID); st.State != wire.QueueStateSearching {
		t.Fatalf("state = %q before patience runs out, want searching", st.State)
	}

	// The client polls while it waits, which is what keeps the ticket alive.
	for waited := 0; waited < 70; waited += 10 {
		*clock = clock.Add(10 * time.Second)
		m.Status(a.ID)
		m.Status(b.ID)
		settle(m)
	}

	for _, tk := range []*Ticket{a, b} {
		st, _ := m.Status(tk.ID)
		if st.State != wire.QueueStateAssigned {
			t.Fatalf("ticket %s state = %q after patience, want assigned", tk.ID, st.State)
		}
	}
}

func TestNeverSplitsAParty(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(4, 6, 6, 0), 1)

	a := party(t, m, "7656119800000000", 3)
	b := party(t, m, "7656119811111111", 3)
	settle(m)

	st, _ := m.Status(a.ID)
	if st.Assignment == nil {
		t.Fatal("no assignment")
	}
	teamOfID := map[wire.SteamID]wire.Team{}
	for _, p := range st.Assignment.Roster {
		teamOfID[p.SteamID] = p.Team
	}
	for _, tk := range []*Ticket{a, b} {
		want := teamOfID[tk.Players[0].SteamID]
		for _, p := range tk.Players {
			if teamOfID[p.SteamID] != want {
				t.Fatalf("party %s was split across teams", tk.Leader)
			}
		}
	}
}

func TestPartyTooBigForATeamIsRefused(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(4, 8, 8, 0), 1)
	_, err := m.Enqueue(&Ticket{
		MatchGroup: wire.MatchGroupCasual12v12,
		Leader:     "76561198000000000",
		Players:    roster("76561198000000000", "76561198000000001", "76561198000000002", "76561198000000003", "76561198000000004"),
	})
	if err == nil {
		t.Fatal("a party of five was accepted into a group with teams of four")
	}
}

func TestNoServerRequeuesTheParties(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(4, 4, 8, 0), 0) // no servers at all

	a := party(t, m, "7656119800000000", 2)
	b := party(t, m, "7656119811111111", 2)
	settle(m)

	for _, tk := range []*Ticket{a, b} {
		st, _ := m.Status(tk.ID)
		if st.State != wire.QueueStateSearching {
			t.Fatalf("ticket %s state = %q, want searching after a failed boot", tk.ID, st.State)
		}
	}
}

func TestFailedSetupReleasesTheServer(t *testing.T) {
	m, setup, _ := newTestMM(t, testConfig(4, 4, 8, 0), 1)
	setup.fail = errors.New("rcon refused")

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)

	if free := m.pool.Free(); free != 1 {
		t.Fatalf("free servers = %d, want 1: a server we could not set up must go back", free)
	}
}

func TestQueueTicketExpiresWhenTheClientStopsPolling(t *testing.T) {
	cfg := testConfig(4, 12, 24, 600)
	cfg.Timing.TicketTTLSecs = 30
	m, _, clock := newTestMM(t, cfg, 1)

	a := party(t, m, "7656119800000000", 2)
	*clock = clock.Add(31 * time.Second)
	m.Tick(context.Background())

	st, err := m.Status(a.ID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.State != wire.QueueStateExpired {
		t.Fatalf("state = %q, want expired", st.State)
	}
}

func TestRequeueReplacesTheOldTicket(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(4, 12, 24, 600), 1)

	first := party(t, m, "7656119800000000", 2)
	second := party(t, m, "7656119800000000", 2)
	if first.ID == second.ID {
		t.Fatal("re-queueing returned the same ticket id")
	}
	if st, _ := m.Status(first.ID); st.State != wire.QueueStateCancelled {
		t.Fatalf("old ticket state = %q, want cancelled", st.State)
	}
	if got := m.QueuedPlayers()[wire.MatchGroupCasual12v12]; got != 2 {
		t.Fatalf("queued players = %d, want 2: the old ticket is still counted", got)
	}
}

func TestEmptyServerEndsTheMatchAndReturnsIt(t *testing.T) {
	cfg := testConfig(4, 4, 8, 0)
	cfg.Pool.IdleEndSecs = 60
	m, setup, clock := newTestMM(t, cfg, 1)

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)

	if m.LiveMatches() != 1 {
		t.Fatalf("live matches = %d, want 1", m.LiveMatches())
	}

	*clock = clock.Add(61 * time.Second)
	m.Tick(context.Background())

	if m.LiveMatches() != 0 {
		t.Fatalf("live matches = %d, want 0 after the idle timeout", m.LiveMatches())
	}
	if free := m.pool.Free(); free != 1 {
		t.Fatalf("free servers = %d, want 1", free)
	}
	if setup.teardown == 0 {
		t.Error("the server was released without being torn down")
	}
}

func TestReportedResultEndsTheMatchOnce(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(4, 4, 8, 0), 1)
	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)

	var matchID string
	m.mu.Lock()
	for id := range m.matches {
		matchID = id
	}
	m.mu.Unlock()

	res := wire.MatchResult{MatchID: matchID, Winner: wire.TeamBlu, BluScore: 3}
	if err := m.ReportResult(context.Background(), res); err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := m.ReportResult(context.Background(), res); err != nil {
		t.Fatalf("a duplicate result should be accepted quietly, got %v", err)
	}
	if m.LiveMatches() != 0 {
		t.Fatalf("live matches = %d, want 0", m.LiveMatches())
	}
}

// The war only moves when the coordinator can tell who won, and "who won" is a
// game team while the war thinks in attacker and defender. This is the seam
// where that translation happens.
func TestAWarBattleAdvancesTheFrontOnTheAttackersWin(t *testing.T) {
	dir := t.TempDir()
	theaterPath := filepath.Join(dir, "theater.json")
	if err := os.WriteFile(theaterPath, []byte(`{
	  "id": "t",
	  "nodes": [
	    {"id":"a","name":"A","owner":"RED","plan":[{"kind":"assault","map":"pl_a","attacker_team":3}]},
	    {"id":"b","name":"B","owner":"BLU","plan":[{"kind":"assault","map":"pl_b","attacker_team":3}]}
	  ],
	  "edges": [["a","b"]],
	  "hq": {"RED":"a","BLU":"b"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	theater, err := war.LoadTheater(theaterPath)
	if err != nil {
		t.Fatalf("theater: %v", err)
	}
	warLog, past, err := war.OpenLog(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	defer warLog.Close()
	engine, err := war.NewEngine(theater, warLog, past)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	cfg := testConfig(4, 4, 8, 0)
	cfg.MatchGroups[0].Maps = nil // the war chooses
	setup := &fakeSetup{}
	m := New(cfg, testPool(1), setup, engine, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	n := 0
	m.newID = func() string { n++; return fmt.Sprintf("id%d", n) }

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)

	var mt *Match
	m.mu.Lock()
	for _, v := range m.matches {
		mt = v
	}
	m.mu.Unlock()
	if mt == nil {
		t.Fatal("no match was formed")
	}
	if mt.War == nil {
		t.Fatal("the war was on but the match carries no briefing")
	}
	if mt.Map != "pl_a" && mt.Map != "pl_b" {
		t.Fatalf("map = %q, want one the theater named", mt.Map)
	}
	if mt.War.AttackerTeam != wire.TeamBlu {
		t.Fatalf("attacker wears %s, want BLU as the theater says", mt.War.AttackerTeam)
	}

	defenderNode := mt.War.NodeID
	attacker := war.Side(mt.War.AttackerWar)

	if err := m.ReportResult(context.Background(), wire.MatchResult{
		MatchID: mt.ID, Winner: wire.TeamBlu, BluScore: 3,
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	if got := engine.Owner(defenderNode); got != attacker {
		t.Fatalf("%s is owned by %s, want %s: the attacker's win did not take the node",
			defenderNode, got, attacker)
	}
}

// The same battle, lost. Nothing about the war may move.
func TestADefensiveWinLeavesTheMapAlone(t *testing.T) {
	dir := t.TempDir()
	theaterPath := filepath.Join(dir, "theater.json")
	if err := os.WriteFile(theaterPath, []byte(`{
	  "id": "t",
	  "nodes": [
	    {"id":"a","name":"A","owner":"RED","plan":[{"kind":"assault","map":"pl_a","attacker_team":3}]},
	    {"id":"b","name":"B","owner":"BLU","plan":[{"kind":"assault","map":"pl_b","attacker_team":3}]}
	  ],
	  "edges": [["a","b"]],
	  "hq": {"RED":"a","BLU":"b"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	theater, _ := war.LoadTheater(theaterPath)
	warLog, past, _ := war.OpenLog(filepath.Join(dir, "events.jsonl"))
	defer warLog.Close()
	engine, _ := war.NewEngine(theater, warLog, past)

	cfg := testConfig(4, 4, 8, 0)
	cfg.MatchGroups[0].Maps = nil
	m := New(cfg, testPool(1), &fakeSetup{}, engine, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	n := 0
	m.newID = func() string { n++; return fmt.Sprintf("id%d", n) }

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)

	var mt *Match
	m.mu.Lock()
	for _, v := range m.matches {
		mt = v
	}
	m.mu.Unlock()

	node := mt.War.NodeID
	ownerBefore := engine.Owner(node)

	// RED holds: the attacker wore BLU, so a RED win is a defensive win.
	if err := m.ReportResult(context.Background(), wire.MatchResult{
		MatchID: mt.ID, Winner: wire.TeamRed, RedScore: 3,
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	if got := engine.Owner(node); got != ownerBefore {
		t.Fatalf("%s changed hands on a defensive win", node)
	}
}

// emptyProvider always answers "nothing free", and says why.
type emptyProvider struct{ reason string }

func (e emptyProvider) Kind() string { return "empty" }
func (e emptyProvider) Acquire(context.Context, pool.Request) (*pool.Server, error) {
	return nil, pool.NoServerReason{Provider: "empty", Reason: e.reason}
}
func (e emptyProvider) Release(context.Context, *pool.Server) error { return nil }

func TestWaitingForAServerTellsThePlayerWhy(t *testing.T) {
	cfg := testConfig(4, 4, 8, 0)
	setup := &fakeSetup{}
	p := &pool.Pool{}
	p.AddProvider(emptyProvider{reason: "every server is booked."})
	m := New(cfg, p, setup, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	a := party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	m.Tick(context.Background())

	// boot() runs off the tick, so give it a moment to reach the wait.
	var st wire.QueueStatus
	for i := 0; i < 500; i++ {
		st, _ = m.Status(a.ID)
		if strings.Contains(st.Detail, "booked") {
			break
		}
		time.Sleep(time.Millisecond)
	}

	if st.State != wire.QueueStateSearching {
		t.Fatalf("state = %q, want searching: there is nothing to connect to yet", st.State)
	}
	if st.Detail == "" {
		t.Fatal("a match that formed and cannot start said nothing about it")
	}
	if !strings.Contains(st.Detail, "every server is booked.") {
		t.Fatalf("detail = %q, want the provider's own reason in it", st.Detail)
	}
}

func TestQueueDetailIsEmptyWhileStillSearching(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(4, 12, 24, 600), 1)

	a := party(t, m, "7656119800000000", 2)
	m.Tick(context.Background())

	st, _ := m.Status(a.ID)
	if st.Detail != "" {
		t.Fatalf("detail = %q, want nothing: the queue is just short of players", st.Detail)
	}
}
