package war

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A four-node line: RED HQ — MIDDLE — DEPOT — BLU HQ.
func testTheater() *Theater {
	stage := func(kind, m string) Stage {
		return Stage{Kind: kind, Map: m, AttackerTeam: 3}
	}
	return &Theater{
		ID:   "test",
		Name: "Test Line",
		Nodes: []Node{
			{ID: "a_redhq", Name: "RED HQ", Owner: SideRED, Plan: []Stage{stage("assault", "pl_a")}},
			{ID: "b_middle", Name: "Middle", Owner: SideRED, Plan: []Stage{
				stage("breakthrough", "cp_b1"), stage("assault", "pl_b2"),
			}},
			{ID: "c_depot", Name: "Depot", Owner: SideBLU, Plan: []Stage{stage("assault", "pl_c")}},
			{ID: "d_bluhq", Name: "BLU HQ", Owner: SideBLU, Plan: []Stage{stage("assault", "pl_d")}},
		},
		Edges:              [][2]string{{"a_redhq", "b_middle"}, {"b_middle", "c_depot"}, {"c_depot", "d_bluhq"}},
		HQ:                 map[Side]string{SideRED: "a_redhq", SideBLU: "d_bluhq"},
		FrontsByPopulation: []int{16, 32},
	}
}

func newTestEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "war-events.jsonl")
	l, past, err := OpenLog(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	e, err := NewEngine(testTheater(), l, past)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return e, path
}

func TestTheaterValidates(t *testing.T) {
	if err := testTheater().Validate(); err != nil {
		t.Fatalf("the test theater is invalid: %v", err)
	}

	bad := testTheater()
	bad.Nodes[1].Plan = nil
	if err := bad.Validate(); err == nil {
		t.Error("a node with no stage plan was accepted")
	}

	bad = testTheater()
	bad.Edges = append(bad.Edges, [2]string{"b_middle", "nowhere"})
	if err := bad.Validate(); err == nil {
		t.Error("an edge to a node that does not exist was accepted")
	}
}

func TestWarWidthFollowsPopulation(t *testing.T) {
	th := testTheater()
	for _, tc := range []struct{ pop, want int }{
		{0, 1}, {15, 1}, {16, 2}, {31, 2}, {32, 3}, {100, 3},
	} {
		if got := th.WantFronts(tc.pop); got != tc.want {
			t.Errorf("WantFronts(%d) = %d, want %d", tc.pop, got, tc.want)
		}
	}
}

func TestReconcileOpensAndClosesFronts(t *testing.T) {
	e, _ := newTestEngine(t)

	fronts, err := e.Reconcile(4)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(fronts) != 1 {
		t.Fatalf("fronts = %d at four players, want 1", len(fronts))
	}

	fronts, err = e.Reconcile(20)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(fronts) != 2 {
		t.Fatalf("fronts = %d at twenty players, want 2", len(fronts))
	}

	fronts, _ = e.Reconcile(4)
	if len(fronts) != 1 {
		t.Fatalf("fronts = %d after the population fell, want 1", len(fronts))
	}
}

func TestFrontOnlyOpensWhereTheSidesMeet(t *testing.T) {
	e, _ := newTestEngine(t)
	fronts, _ := e.Reconcile(4)
	f := fronts[0]

	// b_middle (RED) and c_depot (BLU) are the only adjacent pair held by
	// different sides, so the offensive must be on one of them.
	if f.NodeID != "b_middle" && f.NodeID != "c_depot" {
		t.Fatalf("front opened on %s, which is not on the front line", f.NodeID)
	}
	if f.Attacker != e.Owner(f.NodeID).Other() {
		t.Fatalf("the node's own owner is attacking it")
	}
}

func TestAttackerWinningEveryStageCapturesAndAdvances(t *testing.T) {
	e, _ := newTestEngine(t)
	fronts, _ := e.Reconcile(4)
	f := fronts[0]

	node, _ := e.theater.Node(f.NodeID)
	attacker := f.Attacker
	for i := 0; i < len(node.Plan); i++ {
		b, err := e.NextBattle(f.ID)
		if err != nil {
			t.Fatalf("next battle at stage %d: %v", i, err)
		}
		if b.StageIndex != i {
			t.Fatalf("stage = %d, want %d", b.StageIndex, i)
		}
		if err := e.ApplyResult(f.ID, "match", true); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}

	if got := e.Owner(f.NodeID); got != attacker {
		t.Fatalf("%s is owned by %s after the offensive cleared, want %s", f.NodeID, got, attacker)
	}
	after := e.Fronts()
	if len(after) != 1 {
		t.Fatalf("fronts after the capture = %d, want 1 (the war moved on)", len(after))
	}
	if after[0].NodeID == f.NodeID {
		t.Fatal("the front stayed on the node that was just captured")
	}
	if after[0].Attacker != attacker {
		t.Fatalf("the new front is attacked by %s, want the side that just advanced", after[0].Attacker)
	}
}

func TestDefenderHoldingTheFirstStageBreaksTheOffensive(t *testing.T) {
	e, _ := newTestEngine(t)
	fronts, _ := e.Reconcile(4)
	f := fronts[0]
	owner := e.Owner(f.NodeID)

	if err := e.ApplyResult(f.ID, "match", false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := e.Owner(f.NodeID); got != owner {
		t.Fatalf("%s changed hands on a defensive win", f.NodeID)
	}
	for _, open := range e.Fronts() {
		if open.ID == f.ID {
			t.Fatal("the broken offensive is still open")
		}
	}
}

func TestDefenderPushesAMultiStageOffensiveBack(t *testing.T) {
	e, _ := newTestEngine(t)
	// Force the front onto the two-stage node.
	e.mu.Lock()
	ev, err := e.log.Append(Event{Kind: EventFrontOpened, FrontID: "front-99", NodeID: "b_middle", Side: SideBLU})
	if err != nil {
		e.mu.Unlock()
		t.Fatalf("append: %v", err)
	}
	e.apply(ev)
	e.mu.Unlock()

	if err := e.ApplyResult("front-99", "m1", true); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if b, _ := e.NextBattle("front-99"); b.StageIndex != 1 {
		t.Fatalf("stage = %d after a win, want 1", b.StageIndex)
	}
	if err := e.ApplyResult("front-99", "m2", false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	b, err := e.NextBattle("front-99")
	if err != nil {
		t.Fatalf("next battle: %v", err)
	}
	if b.StageIndex != 0 {
		t.Fatalf("stage = %d after a defensive win, want the offensive pushed back to 0", b.StageIndex)
	}
}

func TestTheLogIsTheTruth(t *testing.T) {
	e, path := newTestEngine(t)
	fronts, _ := e.Reconcile(4)
	f := fronts[0]
	if err := e.ApplyResult(f.ID, "m1", true); err != nil {
		t.Fatalf("apply: %v", err)
	}
	wantOwner := map[string]Side{}
	for _, n := range e.theater.Nodes {
		wantOwner[n.ID] = e.Owner(n.ID)
	}
	wantFronts := e.Fronts()
	wantCampaign := e.Campaign()

	// Reopen from the same log, as a restart would.
	l2, past, err := OpenLog(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer l2.Close()
	e2, err := NewEngine(testTheater(), l2, past)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if e2.Campaign() != wantCampaign {
		t.Fatalf("campaign = %q after replay, want %q — a restart started a new war", e2.Campaign(), wantCampaign)
	}
	for id, want := range wantOwner {
		if got := e2.Owner(id); got != want {
			t.Errorf("%s owner = %s after replay, want %s", id, got, want)
		}
	}
	got := e2.Fronts()
	if len(got) != len(wantFronts) {
		t.Fatalf("fronts = %d after replay, want %d", len(got), len(wantFronts))
	}
	for i := range got {
		if got[i] != wantFronts[i] {
			t.Errorf("front %d = %+v after replay, want %+v", i, got[i], wantFronts[i])
		}
	}
}

func TestLoadTheaterRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theater.json")

	raw, _ := json.Marshal(map[string]any{"id": "x", "nodes": []any{}})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTheater(path); err == nil {
		t.Error("a theater with no nodes was loaded")
	}
}
