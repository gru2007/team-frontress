package war

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func testTheater(t *testing.T) *Theater {
	t.Helper()
	th, err := LoadTheater(filepath.Join("..", "..", "theater.industrial.json"))
	if err != nil {
		t.Fatalf("load shipped theater: %v", err)
	}
	return th
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine(testTheater(t), MemoryLog(), DefaultRules())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return e
}

// win drives one battle on the given front to the given winner.
func win(t *testing.T, e *Engine, frontID string, winner Side) Update {
	t.Helper()
	outcome := OutcomeRedWin
	if winner == SideBlu {
		outcome = OutcomeBluWin
	}
	up, err := e.RecordBattle(BattleResult{
		BattleID: randHex(4), FrontID: frontID, Outcome: outcome,
		Map: "cp_process_final", Mode: "5cp", Players: 12,
	})
	if err != nil {
		t.Fatalf("record battle on %s: %v", frontID, err)
	}
	return up
}

func TestShippedTheaterIsValid(t *testing.T) {
	th := testTheater(t)
	if th.HQ(SideRed) == "" || th.HQ(SideBlu) == "" {
		t.Fatal("theater must give both sides a headquarters")
	}
	// Adjacency is declared once but must end up symmetric.
	for _, n := range th.Nodes {
		for _, adj := range n.Adjacent {
			other, _ := th.Node(adj)
			if !contains(other.Adjacent, n.ID) {
				t.Errorf("adjacency %s->%s was not mirrored", n.ID, adj)
			}
		}
	}
}

func TestFreshEngineOpensOneFront(t *testing.T) {
	e := newTestEngine(t)
	if got := e.Snapshot().Campaign.Number; got != 1 {
		t.Fatalf("campaign number = %d, want 1", got)
	}
	fronts := e.ActiveFronts()
	if len(fronts) != 1 {
		t.Fatalf("a fresh world opened %d fronts, want exactly 1", len(fronts))
	}
	f := fronts[0]
	if f.Attacker == SideNone || f.Attacker == f.Defender {
		t.Fatalf("front has nonsense belligerents: %+v", f)
	}
	if len(f.Plan) == 0 || f.Stage != 0 {
		t.Fatalf("front should start at stage 0 of a real plan: %+v", f)
	}
}

// The width of the war is the population rule that keeps a small community
// together instead of spreading it over the whole map.
func TestFrontsScaleWithPopulation(t *testing.T) {
	e := newTestEngine(t)
	for _, tc := range []struct{ pop, want int }{
		{0, 1}, {6, 1}, {15, 1}, {16, 2}, {31, 2}, {32, 3}, {48, 4}, {200, 4},
	} {
		if got := e.frontsFor(tc.pop); got != tc.want {
			t.Errorf("frontsFor(%d) = %d, want %d", tc.pop, got, tc.want)
		}
	}

	if err := e.Reconcile(24, nil); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := len(e.ActiveFronts()); got != 2 {
		t.Fatalf("24 players opened %d fronts, want 2", got)
	}
	// Falling back to a small population must not close a front people have
	// already fought on.
	f := e.ActiveFronts()[0]
	win(t, e, f.ID, f.Attacker)
	if err := e.Reconcile(4, nil); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := e.Front(f.ID); !ok {
		t.Fatal("a front with battles behind it was closed when the population dropped")
	}
}

// The same strategic stage has to be playable at six players and at twenty.
func TestPlanAdaptsToPopulation(t *testing.T) {
	e := newTestEngine(t)
	f := e.ActiveFronts()[0]

	small, err := e.PlanBattle(f.ID, 6)
	if err != nil {
		t.Fatalf("plan at 6: %v", err)
	}
	big, err := e.PlanBattle(f.ID, 12)
	if err != nil {
		t.Fatalf("plan at 12: %v", err)
	}
	if small.StageKind != big.StageKind {
		t.Fatalf("population changed the stage: %s vs %s", small.StageKind, big.StageKind)
	}
	if small.Mode == big.Mode {
		t.Fatalf("six players and twelve got the same format (%s); the small queue should fall back to a skirmish", small.Mode)
	}
	if small.MinPlayers > 6 {
		t.Fatalf("plan for six players needs %d", small.MinPlayers)
	}
}

// A payload map is built for BLU attacking, so a RED offensive on one has to be
// translated into in-game teams.
func TestDirectionalMapsSwapTeams(t *testing.T) {
	p := BattlePlan{Attacker: SideRed, Defender: SideBlu, AttackerTeam: "blu"}
	if got := p.GameTeam(SideRed); got != SideBlu {
		t.Errorf("RED attacking a BLU-attacker map plays as %s, want BLU", got)
	}
	if got := p.GameTeam(SideBlu); got != SideRed {
		t.Errorf("BLU defending plays as %s, want RED", got)
	}
	sym := BattlePlan{Attacker: SideRed, Defender: SideBlu}
	if sym.GameTeam(SideRed) != SideRed || sym.GameTeam(SideBlu) != SideBlu {
		t.Error("a plan with no attacker team must not swap anybody")
	}
}

// The uniform has to mean something. Payload and attack/defend maps force the
// attacker to be BLU and cannot be changed, so rather than a player's colours
// flipping depending on which map came up, the attacking side wears BLU in
// every battle — and their uniform tells them which half of the fight they are
// in.
func TestTheAttackingSideAlwaysWearsTheSameColours(t *testing.T) {
	e := newTestEngine(t)
	f := e.ActiveFronts()[0]

	// A small queue gets a symmetric skirmish map, a big one gets 5CP: neither
	// states an attacker team, and both must still follow the rule.
	for _, players := range []int{6, 12} {
		plan, err := e.PlanBattle(f.ID, players)
		if err != nil {
			t.Fatalf("plan at %d: %v", players, err)
		}
		if plan.AttackerTeam != "blu" {
			t.Fatalf("at %d players the attacker plays as %q, want blu", players, plan.AttackerTeam)
		}
		if got := plan.GameTeam(plan.Attacker); got != SideBlu {
			t.Errorf("at %d players the attacker wears %s", players, got)
		}
		if got := plan.GameTeam(plan.Defender); got != SideRed {
			t.Errorf("at %d players the defender wears %s", players, got)
		}
		// And the result still comes back in war sides.
		outcome, red, blu := plan.TranslateResult(OutcomeBluWin, 1, 3)
		if outcome.Winner() != plan.Attacker {
			t.Errorf("a BLU-team win read as %s, want the attacker %s", outcome, plan.Attacker)
		}
		if plan.Attacker == SideRed && (red != 3 || blu != 1) {
			t.Errorf("scores translated to RED %d BLU %d, want 3 and 1", red, blu)
		}
	}
}

// An operator who prefers the old behaviour can have it back.
func TestAttackerTeamRuleCanBeSwitchedOff(t *testing.T) {
	rules := DefaultRules()
	rules.AttackerTeam = ""
	e, err := NewEngine(testTheater(t), MemoryLog(), rules)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := e.PlanBattle(e.ActiveFronts()[0].ID, 6)
	if err != nil {
		t.Fatal(err)
	}
	// A skirmish map states no attacker team, so each side plays its own colour.
	if plan.AttackerTeam != "" {
		t.Fatalf("with the rule off a symmetric map still forced %q", plan.AttackerTeam)
	}
	if plan.GameTeam(SideRed) != SideRed {
		t.Error("RED did not play as RED with the rule off")
	}
}

func TestOffensiveCapturesNodeAndMovesOn(t *testing.T) {
	e := newTestEngine(t)
	f := e.ActiveFronts()[0]
	stages := len(f.Plan)

	var up Update
	for i := 0; i < stages; i++ {
		up = win(t, e, f.ID, f.Attacker)
	}
	if !up.NodeCaptured {
		t.Fatalf("clearing %d stages did not capture the node: %+v", stages, up)
	}
	if up.NewOwner != f.Attacker {
		t.Fatalf("node went to %s, want %s", up.NewOwner, f.Attacker)
	}
	if e.Snapshot().Campaign.Captures != 1 {
		t.Fatal("capture was not counted on the campaign")
	}
	if _, ok := e.Front(f.ID); ok {
		t.Fatal("a won front stayed open")
	}
	// The war must not go quiet after a capture: the front moves along the graph.
	if got := len(e.ActiveFronts()); got != 1 {
		t.Fatalf("after a capture there are %d fronts, want 1", got)
	}
	if e.ActiveFronts()[0].ID == f.ID {
		t.Fatal("the new front is the old one")
	}
}

func TestDefeatPushesTheOffensiveBackThenBreaksIt(t *testing.T) {
	e := newTestEngine(t)
	f := e.ActiveFronts()[0]
	if len(f.Plan) < 2 {
		t.Skip("this theater's first front is a single-stage plan")
	}

	win(t, e, f.ID, f.Attacker) // stage 1
	if cur, _ := e.Front(f.ID); cur.Stage != 1 {
		t.Fatalf("attacker win left the front at stage %d, want 1", cur.Stage)
	}
	up := win(t, e, f.ID, f.Defender) // back to stage 0, not a reset
	if up.Collapsed {
		t.Fatal("one defensive win at stage 1 collapsed the whole offensive")
	}
	if cur, _ := e.Front(f.ID); cur.Stage != 0 {
		t.Fatalf("defender win left the front at stage %d, want 0", cur.Stage)
	}

	up = win(t, e, f.ID, f.Defender) // broken at the line it started from
	if !up.Collapsed {
		t.Fatalf("a defensive win at stage 0 did not break the offensive: %+v", up)
	}
	if _, ok := e.Front(f.ID); ok {
		t.Fatal("a collapsed front stayed open")
	}
	// The side that held gets to push back where the attack came from.
	fronts := e.ActiveFronts()
	if len(fronts) != 1 {
		t.Fatalf("after a collapse there are %d fronts, want 1", len(fronts))
	}
	if fronts[0].Attacker != f.Defender {
		t.Fatalf("counter-offensive is run by %s, want %s", fronts[0].Attacker, f.Defender)
	}
	if fronts[0].TargetNode != f.SourceNode {
		t.Fatalf("counter-offensive targets %s, want the source %s", fronts[0].TargetNode, f.SourceNode)
	}
}

// Losing ground must eventually put a side under mobilization, and mobilization
// must make its defence cheaper — never fake a result.
func TestMobilizationShortensTheDefence(t *testing.T) {
	e := newTestEngine(t)
	// Drive RED forward until BLU is two territories down.
	for i := 0; i < 12 && e.MobilizedSide() == SideNone; i++ {
		var red *Front
		for _, f := range e.ActiveFronts() {
			if f.Attacker == SideRed {
				red = &f
				break
			}
		}
		if red == nil {
			// No RED offensive open right now; let BLU's attack break so the
			// coordinator picks another front.
			f := e.ActiveFronts()[0]
			win(t, e, f.ID, f.Defender)
			continue
		}
		win(t, e, red.ID, SideRed)
	}
	if e.MobilizedSide() != SideBlu {
		t.Fatalf("BLU lost ground but is not mobilized (mobilized: %s)", e.MobilizedSide())
	}
	for _, f := range e.ActiveFronts() {
		if f.Defender == SideBlu && f.CollapseAtStage != 1 {
			t.Errorf("front %s attacks a mobilized BLU with collapse stage %d, want 1", f.ID, f.CollapseAtStage)
		}
	}
}

func TestCampaignEndsWhenAHeadquartersFalls(t *testing.T) {
	e := newTestEngine(t)
	hq := e.theater.HQ(SideBlu)

	var over Update
	for i := 0; i < 200 && !over.CampaignOver; i++ {
		fronts := e.ActiveFronts()
		if len(fronts) == 0 {
			t.Fatalf("the war ran out of fronts before reaching %s", hq)
		}
		// Always push RED's offensive; if none is open, break BLU's.
		target := fronts[0]
		for _, f := range fronts {
			if f.Attacker == SideRed {
				target = f
				break
			}
		}
		up := win(t, e, target.ID, SideRed)
		if up.CampaignOver {
			over = up
		}
	}
	if !over.CampaignOver {
		t.Fatal("RED never managed to end the campaign")
	}
	if over.CampaignWon != SideRed {
		t.Fatalf("campaign won by %s, want RED", over.CampaignWon)
	}
	snap := e.Snapshot()
	if snap.Campaign.Status != CampaignIntermission {
		t.Fatalf("campaign status %s, want an armistice", snap.Campaign.Status)
	}
	if len(snap.Fronts) != 0 {
		t.Fatalf("%d fronts survived the campaign", len(snap.Fronts))
	}
	// Nothing may be recorded during the armistice.
	if _, err := e.RecordBattle(BattleResult{FrontID: "anything", Outcome: OutcomeRedWin}); err == nil {
		t.Fatal("a battle was accepted after the campaign ended")
	}
}

func TestIntermissionStartsTheNextCampaign(t *testing.T) {
	rules := DefaultRules()
	rules.Intermission = time.Minute
	e, err := NewEngine(testTheater(t), MemoryLog(), rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.endCampaign(SideBlu, "test"); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(8, nil); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().Campaign.Number != 1 {
		t.Fatal("the next campaign started before the armistice was over")
	}

	e.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if err := e.Reconcile(8, nil); err != nil {
		t.Fatal(err)
	}
	snap := e.Snapshot()
	if snap.Campaign.Number != 2 || snap.Campaign.Status != CampaignActive {
		t.Fatalf("campaign %d is %s, want campaign 2 active", snap.Campaign.Number, snap.Campaign.Status)
	}
	if len(snap.Fronts) == 0 {
		t.Fatal("the new campaign opened no fronts")
	}
	// Campaign 2 must not be a replay of campaign 1.
	if got := snap.Nodes; len(got) == 0 {
		t.Fatal("no nodes in the new campaign")
	}
}

// The log is the war: replaying it has to rebuild exactly the same world.
func TestReplayRebuildsTheSameWorld(t *testing.T) {
	log := MemoryLog()
	e, err := NewEngine(testTheater(t), log, DefaultRules())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 15; i++ {
		fronts := e.ActiveFronts()
		if len(fronts) == 0 {
			break
		}
		f := fronts[i%len(fronts)]
		side := f.Attacker
		if i%3 == 0 {
			side = f.Defender
		}
		win(t, e, f.ID, side)
	}
	before := e.Snapshot()

	replayed, err := NewEngine(testTheater(t), log, DefaultRules())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	after := replayed.Snapshot()

	if before.Revision != after.Revision {
		t.Fatalf("revision %d != %d after replay", before.Revision, after.Revision)
	}
	if before.Campaign != after.Campaign {
		t.Fatalf("campaign differs: %+v vs %+v", before.Campaign, after.Campaign)
	}
	if len(before.Fronts) != len(after.Fronts) {
		t.Fatalf("%d fronts became %d after replay", len(before.Fronts), len(after.Fronts))
	}
	for i := range before.Fronts {
		a, b := before.Fronts[i], after.Fronts[i]
		if a.ID != b.ID || a.Stage != b.Stage || a.Status != b.Status || a.Battles != b.Battles {
			t.Fatalf("front %d differs after replay:\n%+v\n%+v", i, a, b)
		}
	}
	for i := range before.Nodes {
		if before.Nodes[i].Owner != after.Nodes[i].Owner {
			t.Fatalf("node %s owner %s != %s after replay",
				before.Nodes[i].ID, before.Nodes[i].Owner, after.Nodes[i].Owner)
		}
	}
}

func TestLogRoundTripsThroughAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "war.jsonl")
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(testTheater(t), log, DefaultRules())
	if err != nil {
		t.Fatal(err)
	}
	f := e.ActiveFronts()[0]
	win(t, e, f.ID, f.Attacker)
	want := e.Snapshot()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := NewEngine(testTheater(t), reopened, DefaultRules())
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	if got := e2.Snapshot(); got.Revision != want.Revision {
		t.Fatalf("revision %d after reopen, want %d", got.Revision, want.Revision)
	}
	if len(reopened.Events()) == 0 {
		t.Fatal("nothing was written to the log file")
	}
}

func TestUnknownOutcomesNeverMoveTheWar(t *testing.T) {
	e := newTestEngine(t)
	f := e.ActiveFronts()[0]
	before := e.Revision()
	for _, o := range []Outcome{OutcomeUnknown, OutcomeAbandoned} {
		if _, err := e.RecordBattle(BattleResult{FrontID: f.ID, Outcome: o}); err == nil {
			t.Fatalf("outcome %s was accepted", o)
		}
	}
	if e.Revision() != before {
		t.Fatal("a rejected result still changed the world")
	}
	// A stalemate is a real result: it costs time on the front and nothing else.
	up, err := e.RecordBattle(BattleResult{FrontID: f.ID, Outcome: OutcomeStalemate})
	if err != nil {
		t.Fatalf("stalemate rejected: %v", err)
	}
	cur, _ := e.Front(f.ID)
	if cur.Stage != 0 || cur.Battles != 1 {
		t.Fatalf("stalemate moved the front: %+v", cur)
	}
	if up.NodeCaptured || up.Collapsed {
		t.Fatalf("stalemate resolved something: %+v", up)
	}
}

func TestResultsOnResolvedFrontsAreRejected(t *testing.T) {
	e := newTestEngine(t)
	f := e.ActiveFronts()[0]
	for i := 0; i < len(f.Plan); i++ {
		win(t, e, f.ID, f.Attacker)
	}
	// A match that outlived its front (a slow server, a retrying agent) must not
	// move a war that has already moved on.
	if _, err := e.RecordBattle(BattleResult{
		BattleID: "late", FrontID: f.ID, Outcome: OutcomeRedWin,
	}); err == nil {
		t.Fatal("a late result was applied to a captured front")
	}
}

func TestTimelineIsTheRecap(t *testing.T) {
	e := newTestEngine(t)
	f := e.ActiveFronts()[0]
	win(t, e, f.ID, f.Attacker)

	events := e.Timeline(0, 100)
	if len(events) < 3 {
		t.Fatalf("timeline has %d events, want at least campaign+front+battle", len(events))
	}
	var kinds []EventKind
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
		if ev.Headline == "" {
			t.Errorf("event %d (%s) has no headline; the recap would show a blank line", ev.Seq, ev.Kind)
		}
		if ev.At.IsZero() {
			t.Errorf("event %d has no timestamp", ev.Seq)
		}
	}
	if kinds[0] != EventCampaignStarted {
		t.Fatalf("the war does not start with a campaign: %v", kinds)
	}
	// Paging must not repeat what the client already has.
	half := events[len(events)/2]
	rest := e.Timeline(half.Seq, 100)
	for _, ev := range rest {
		if ev.Seq <= half.Seq {
			t.Fatalf("Timeline(since=%d) returned seq %d", half.Seq, ev.Seq)
		}
	}
}

func TestNeutralNodeIsFoughtOverByBothSides(t *testing.T) {
	e := newTestEngine(t)
	// The opening leaves Iron Junction neutral; somebody must be able to attack
	// it, otherwise the middle of the map is decorative.
	found := false
	for i := 0; i < 40 && !found; i++ {
		for _, c := range e.candidates() {
			if c.target == "iron_junction" {
				found = true
				if c.attacker == c.defender || c.defender == SideNone {
					t.Fatalf("neutral objective produced a one-sided front: %+v", c)
				}
			}
		}
		if !found {
			t.Fatal("no candidate front targets the neutral objective")
		}
	}
	if got := e.Snapshot().NodeCount["NONE"]; got != 1 {
		t.Fatalf("%d neutral nodes at the start, want 1", got)
	}
}

func ExampleEngine_PlanBattle() {
	th, err := LoadTheater(filepath.Join("..", "..", "theater.industrial.json"))
	if err != nil {
		panic(err)
	}
	e, err := NewEngine(th, MemoryLog(), DefaultRules())
	if err != nil {
		panic(err)
	}
	f := e.ActiveFronts()[0]
	plan, err := e.PlanBattle(f.ID, 12)
	if err != nil {
		panic(err)
	}
	fmt.Println(plan.StageKind, plan.Stage+1, "of", plan.StageCount)
	// Output: breakthrough 1 of 3
}

// A scoreline arrives in in-game teams. On a map where the attacker had to play
// BLU, reading it back wrong would advance the wrong side's offensive.
func TestResultsAreTranslatedBackIntoWarSides(t *testing.T) {
	directional := BattlePlan{Attacker: SideRed, Defender: SideBlu, AttackerTeam: "blu"}
	// RED attacked as the BLU team and won 3-1 on the scoreboard.
	outcome, red, blu := directional.TranslateResult(OutcomeBluWin, 1, 3)
	if outcome != OutcomeRedWin {
		t.Errorf("BLU-team win by the RED attacker read as %s, want RED_WIN", outcome)
	}
	if red != 3 || blu != 1 {
		t.Errorf("scores translated to RED %d BLU %d, want 3 and 1", red, blu)
	}

	symmetric := BattlePlan{Attacker: SideRed, Defender: SideBlu}
	outcome, red, blu = symmetric.TranslateResult(OutcomeBluWin, 1, 3)
	if outcome != OutcomeBluWin || red != 1 || blu != 3 {
		t.Errorf("a symmetric map was translated: %s %d-%d", outcome, red, blu)
	}

	// A stalemate is a stalemate either way round.
	if got, _, _ := directional.TranslateResult(OutcomeStalemate, 2, 2); got != OutcomeStalemate {
		t.Errorf("stalemate became %s", got)
	}
}

// ---------------------------------------------------------------------------
// Map rotation
//
// The war moves whether or not the map does, so a repeated map is invisible to
// every other test here — and is the thing a player notices first. These are
// the tests that failed before the weighted pick landed: at four players a
// logistics front drew the same arena thirty times running.
// ---------------------------------------------------------------------------

// planMap is the map the engine would choose for the next battle on a front.
func planMap(t *testing.T, e *Engine, frontID string, players int) string {
	t.Helper()
	plan, err := e.PlanBattle(frontID, players)
	if err != nil {
		t.Fatalf("plan battle on %s: %v", frontID, err)
	}
	return plan.Map
}

// playMap records a finished battle on a front, so the front remembers the map.
func playMap(t *testing.T, e *Engine, frontID, mapName string, winner Side) {
	t.Helper()
	outcome := OutcomeRedWin
	if winner == SideBlu {
		outcome = OutcomeBluWin
	}
	if _, err := e.RecordBattle(BattleResult{
		BattleID: randHex(4), FrontID: frontID, Outcome: outcome,
		Map: mapName, Mode: "koth", Players: 8,
	}); err != nil {
		t.Fatalf("record battle on %s: %v", frontID, err)
	}
}

func TestSmallQueuesStillGetAVarietyOfMaps(t *testing.T) {
	e := newTestEngine(t)
	f := e.ActiveFronts()[0]

	// Four players is the population this has to work at: the whole point of the
	// project is that a handful of people still get a war worth playing.
	seen := map[string]int{}
	for i := 0; i < 40; i++ {
		seen[planMap(t, e, f.ID, 4)]++
	}

	if len(seen) < 3 {
		t.Fatalf("40 battles at 4 players drew only %d distinct maps (%v) — "+
			"an evening on one map is the failure this rotation exists to prevent",
			len(seen), seen)
	}

	// And no single map may dominate: a pool that is technically varied but
	// picks the same entry four times in five is the same evening.
	for name, n := range seen {
		if n > 30 {
			t.Fatalf("%s came up %d times in 40 battles", name, n)
		}
	}
}

func TestAFrontDoesNotReplayTheMapItJustPlayed(t *testing.T) {
	e := newTestEngine(t)
	f := e.ActiveFronts()[0]

	// Losing keeps the front on the same stage, so this is the exact situation
	// where a naive picker repeats itself: same front, same stage, same queue.
	last := planMap(t, e, f.ID, 4)
	for i := 0; i < 12; i++ {
		playMap(t, e, f.ID, last, f.Defender)

		cur, ok := e.Front(f.ID)
		if !ok {
			// The offensive collapsed; carry on with whatever replaced it.
			if len(e.ActiveFronts()) == 0 {
				t.Fatal("the war went quiet")
			}
			f = e.ActiveFronts()[0]
			last = planMap(t, e, f.ID, 4)
			continue
		}

		next := planMap(t, e, cur.ID, 4)
		if next == last {
			t.Fatalf("battle %d on %s replayed %s immediately", i+2, cur.ID, next)
		}
		last = next
	}
}

func TestTheFrontRemembersItsMapsAcrossAReplay(t *testing.T) {
	// Anti-repeat that lives only in memory would reset every restart, which on
	// a small server is most evenings.
	log := MemoryLog()
	e, err := NewEngine(testTheater(t), log, DefaultRules())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	f := e.ActiveFronts()[0]
	// An attacker win clears a stage and leaves the front open, which is what
	// this test needs; a defender win at stage 0 collapses the offensive.
	playMap(t, e, f.ID, "koth_lakeside_final", f.Attacker)

	before, ok := e.Front(f.ID)
	if !ok {
		t.Fatal("clearing one stage of a multi-stage plan closed the front")
	}
	if !before.PlayedRecently("koth_lakeside_final") {
		t.Fatal("the front did not remember the map it just played")
	}

	replayed, err := NewEngine(testTheater(t), log, DefaultRules())
	if err != nil {
		t.Fatalf("replay engine: %v", err)
	}
	after, ok := replayed.Front(f.ID)
	if !ok {
		t.Fatal("the front did not survive the replay")
	}
	if !after.PlayedRecently("koth_lakeside_final") {
		t.Fatalf("replay lost the map memory: %v", after.RecentMaps)
	}
}

func TestMapMemoryStaysShort(t *testing.T) {
	// It must not grow without bound — a front can host hundreds of battles, and
	// remembering all of them would eventually leave nothing left to pick.
	f := &Front{}
	for i := 0; i < 20; i++ {
		f.RememberMap(fmt.Sprintf("koth_%d", i))
	}
	if len(f.RecentMaps) != FrontMapMemory {
		t.Fatalf("front remembers %d maps, want %d", len(f.RecentMaps), FrontMapMemory)
	}
	if f.RecentMaps[0] != "koth_19" {
		t.Fatalf("newest map is %q, want koth_19", f.RecentMaps[0])
	}
	// Playing the same map twice must not fill the memory with one name.
	f.RememberMap("koth_19")
	if f.RecentMaps[0] != "koth_19" || (len(f.RecentMaps) > 1 && f.RecentMaps[1] == "koth_19") {
		t.Fatalf("duplicate map filled the memory: %v", f.RecentMaps)
	}
}

func TestBiggerQueuesStillGetTheStagesOwnMaps(t *testing.T) {
	// Variety must not come at the cost of the thing it decorates: at a real
	// population the stage's own mode has to show up, not a skirmish fallback.
	e := newTestEngine(t)
	f := e.ActiveFronts()[0]

	modes := map[string]int{}
	for i := 0; i < 30; i++ {
		plan, err := e.PlanBattle(f.ID, 12)
		if err != nil {
			t.Fatalf("plan battle: %v", err)
		}
		modes[plan.Mode]++
	}
	if modes["arena"] > 0 {
		t.Fatalf("12 players drew an arena map %d times; the stage's own pool fits", modes["arena"])
	}
	if modes["5cp"] == 0 {
		t.Fatalf("12 players never drew the breakthrough stage's own mode: %v", modes)
	}
}
