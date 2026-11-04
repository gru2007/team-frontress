package war

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Engine holds the live war. Its state is a fold over the event log, so the
// only way to change the war is to append an event.
type Engine struct {
	mu       sync.Mutex
	theater  *Theater
	log      *Log
	campaign string
	winner   Side
	endedAt  int64
	owner    map[string]Side // node id -> owner
	fronts   map[string]*Front
	nextID   int
	now      func() time.Time
	// recent is the tail of the log, newest last, for the war screen's
	// front-line report.
	recent  []Event
	results map[string]bool
}

// recentEvents is how much of the log the war screen can show.
const recentEvents = 40

// NewEngine builds an engine over a theater and its log, replaying whatever
// the log already contains.
func NewEngine(t *Theater, l *Log, past []Event) (*Engine, error) {
	e := &Engine{
		theater: t,
		log:     l,
		owner:   map[string]Side{},
		fronts:  map[string]*Front{},
		now:     time.Now,
		results: map[string]bool{},
	}
	for _, n := range t.Nodes {
		e.owner[n.ID] = n.Owner
	}
	for _, ev := range past {
		e.apply(ev)
	}
	if e.campaign == "" {
		ev, err := e.log.Append(Event{
			Kind:     EventCampaignStarted,
			Campaign: fmt.Sprintf("%s-%d", t.ID, time.Now().Unix()),
		})
		if err != nil {
			return nil, err
		}
		e.apply(ev)
	}
	// Finish a durable capture interrupted before its follow-up events.
	for _, f := range e.Fronts() {
		if e.owner[f.NodeID] == f.Attacker {
			ev, err := e.log.Append(Event{Kind: EventFrontClosed, FrontID: f.ID, NodeID: f.NodeID, Note: "recovered capture"})
			if err != nil {
				return nil, err
			}
			e.apply(ev)
		}
	}
	if e.winner == "" {
		var victory *Event
		for _, side := range []Side{SideRED, SideBLU} {
			node := t.HQ[side]
			owner := e.owner[node]
			if node == "" || owner == "" || owner == side {
				continue
			}
			if victory != nil {
				return nil, fmt.Errorf("both headquarters were captured in a legacy campaign; reconcile its winner before upgrading")
			}
			ev := Event{Kind: EventCampaignWon, Campaign: e.campaign, Side: owner, NodeID: node}
			for _, old := range past {
				if old.Kind == EventNodeCaptured && old.NodeID == node && old.Side == owner {
					ev.At = old.At
				}
			}
			victory = &ev
		}
		if victory != nil {
			ev, err := e.log.Append(*victory)
			if err != nil {
				return nil, err
			}
			e.apply(ev)
		}
	}
	return e, nil
}

// apply folds one event into state. It must stay total: an event kind it does
// not know is ignored, so an older binary can still read a newer log.
func (e *Engine) apply(ev Event) {
	if ev.MatchID != "" {
		e.results[ev.MatchID] = true
	}
	e.recent = append(e.recent, ev)
	if len(e.recent) > recentEvents {
		e.recent = append([]Event(nil), e.recent[len(e.recent)-recentEvents:]...)
	}
	switch ev.Kind {
	case EventCampaignStarted:
		e.winner, e.endedAt = "", 0
		e.campaign = ev.Campaign
		// A new campaign resets ownership to the theater's own starting line.
		for _, n := range e.theater.Nodes {
			e.owner[n.ID] = n.Owner
		}
		e.fronts = map[string]*Front{}
		e.recent = []Event{ev}
	case EventCampaignWon:
		e.winner, e.endedAt = ev.Side, ev.At
		e.fronts = map[string]*Front{}
	case EventFrontOpened:
		e.fronts[ev.FrontID] = &Front{
			ID:       ev.FrontID,
			NodeID:   ev.NodeID,
			Attacker: ev.Side,
			OpenedAt: ev.At,
			Momentum: MaxMomentum,
		}
		if n := frontNum(ev.FrontID); n > e.nextID {
			e.nextID = n
		}
	case EventStageWon:
		if f, ok := e.fronts[ev.FrontID]; ok {
			f.StageIndex = ev.Stage
		}
	case EventStageLost:
		if f, ok := e.fronts[ev.FrontID]; ok {
			f.StageIndex = ev.Stage
			f.Momentum--
		}
	case EventNodeCaptured:
		e.owner[ev.NodeID] = ev.Side
	case EventFrontClosed, EventOffensiveBroken:
		delete(e.fronts, ev.FrontID)
	}
}

// Campaign returns the current campaign id.
func (e *Engine) Campaign() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.campaign
}

// Snapshot is the war at one moment, for drawing it.
type Snapshot struct {
	Winner   Side
	EndedAt  int64
	Theater  *Theater
	Campaign string
	Owner    map[string]Side
	Fronts   []Front
	// Recent is the end of the war log, newest first.
	Recent []Event
}

// Snapshot copies the state the war screen needs. The theater is shared and
// must not be modified.
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Snapshot{Theater: e.theater, Campaign: e.campaign, Owner: make(map[string]Side, len(e.owner)), Winner: e.winner, EndedAt: e.endedAt}
	for k, v := range e.owner {
		s.Owner[k] = v
	}
	for _, f := range e.fronts {
		s.Fronts = append(s.Fronts, *f)
	}
	sort.Slice(s.Fronts, func(i, j int) bool { return s.Fronts[i].ID < s.Fronts[j].ID })
	for i := len(e.recent) - 1; i >= 0; i-- {
		s.Recent = append(s.Recent, e.recent[i])
	}
	return s
}

// Owner returns which side holds a node.
func (e *Engine) Owner(nodeID string) Side {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owner[nodeID]
}

// Fronts returns the open fronts, ordered by id so callers see a stable list.
func (e *Engine) Fronts() []Front {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Front, 0, len(e.fronts))
	for _, f := range e.fronts {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// WantFronts is how wide the war should be for this many players online.
//
// The rule is the same one the design states: more people online means more of
// the war is live at once, so eight players are not scattered across six
// fronts. An empty fronts_by_population means one front, always.
func (t *Theater) WantFronts(population int) int {
	n := 1
	for _, threshold := range t.FrontsByPopulation {
		if population >= threshold {
			n++
		}
	}
	return n
}

// Reconcile opens or closes fronts so the war is as wide as the population
// warrants. It returns the fronts that are now open.
//
// servers is the GC's pool: a front needs somewhere to be fought, so there are
// never more fronts than servers (but always one, so the war has a front to
// show even while no server is up).
func (e *Engine) Reconcile(population, servers int) ([]Front, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.winner != "" {
		return []Front{}, nil
	}

	want := e.theater.WantFronts(population)
	if servers < want {
		want = servers
	}
	if want < 1 {
		want = 1
	}
	for len(e.fronts) > want {
		// Close the newest front nobody has fought for yet. A front with
		// progress (a stage won, momentum spent) stays: people fought for
		// it, and a quiet hour -- or a coordinator restarting with nobody
		// online yet -- must not throw that away.
		var newest *Front
		for _, f := range e.fronts {
			if f.StageIndex > 0 || f.Momentum < MaxMomentum {
				continue
			}
			if newest == nil || f.ID > newest.ID {
				newest = f
			}
		}
		if newest == nil {
			break
		}
		ev, err := e.log.Append(Event{
			Kind: EventFrontClosed, FrontID: newest.ID, NodeID: newest.NodeID,
			Note: "population",
		})
		if err != nil {
			return nil, err
		}
		e.apply(ev)
	}
	for len(e.fronts) < want {
		node, attacker, ok := e.pickContestedNodeLocked()
		if !ok {
			break // nowhere left to attack; the war is as wide as it gets
		}
		e.nextID++
		ev, err := e.log.Append(Event{
			Kind:    EventFrontOpened,
			FrontID: fmt.Sprintf("front-%d", e.nextID),
			NodeID:  node,
			Side:    attacker,
		})
		if err != nil {
			return nil, err
		}
		e.apply(ev)
	}

	out := make([]Front, 0, len(e.fronts))
	for _, f := range e.fronts {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// pickContestedNodeLocked finds a node one side can attack: it is held by one
// side and adjacent to a node held by the other, and no front is on it yet.
//
// The order is deterministic (node id) so the same theater and the same log
// produce the same war on every machine.
func (e *Engine) pickContestedNodeLocked() (nodeID string, attacker Side, ok bool) {
	busy := map[string]bool{}
	for _, f := range e.fronts {
		busy[f.NodeID] = true
	}
	nodes := append([]Node(nil), e.theater.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	for _, n := range nodes {
		if busy[n.ID] {
			continue
		}
		defender := e.owner[n.ID]
		for _, adj := range e.theater.Neighbours(n.ID) {
			if e.owner[adj] == defender.Other() {
				return n.ID, defender.Other(), true
			}
		}
	}
	return "", "", false
}

// Battle is what the war wants played next on a front.
type Battle struct {
	Front      Front
	NodeName   string
	Stage      Stage
	StageIndex int
	StageCount int
	Attacker   Side
	Defender   Side
	// AttackerTeam is the stage's own answer. A battlefield may override it,
	// because whether the attacker has to wear BLU is a property of the map,
	// not of the offensive.
	AttackerTeam int32
	// Fields is where this battle may be fought. One entry when the stage
	// pins a map, the kind's whole pool otherwise. The matchmaker chooses
	// from it -- it is the half that knows how many people are in the queue
	// and what maps they voted for.
	Fields []Battlefield
	// Conditions it is fought under (see Conditions).
	Conditions []string
}

// ConditionsFor is what the next battle on a front is fought under: the
// sector's own conditions, then the war's. An offensive on its last point of
// momentum is out of breath (long waves for everyone); the last stage of an
// assault on a headquarters is a last stand (reserves close by, short waves).
// The war's word on respawn waves replaces the sector's.
func (t *Theater) ConditionsFor(n Node, f Front) []string {
	var out []string
	add := func(c string) {
		for _, have := range out {
			if have == c {
				return
			}
		}
		out = append(out, c)
	}
	waves := ""
	if f.Momentum == 1 {
		waves = "longwaves"
	}
	if hq := t.HQ[f.Attacker.Other()]; hq == n.ID && f.StageIndex == len(n.Plan)-1 {
		waves = "quickwaves"
	}
	for _, c := range n.Conditions {
		if waves != "" && (c == "longwaves" || c == "quickwaves") {
			continue
		}
		add(c)
	}
	if waves != "" {
		add(waves)
	}
	return out
}

// NextBattle returns the battle the front is currently fighting.
func (e *Engine) NextBattle(frontID string) (Battle, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f, ok := e.fronts[frontID]
	if !ok {
		return Battle{}, fmt.Errorf("no front %q", frontID)
	}
	n, ok := e.theater.Node(f.NodeID)
	if !ok {
		return Battle{}, fmt.Errorf("front %s: no node %q", frontID, f.NodeID)
	}
	if f.StageIndex < 0 || f.StageIndex >= len(n.Plan) {
		return Battle{}, fmt.Errorf("front %s: stage %d is outside %s's plan", frontID, f.StageIndex, n.ID)
	}
	stage := n.Plan[f.StageIndex]
	fields := e.theater.FieldsFor(stage)
	if len(fields) == 0 {
		return Battle{}, fmt.Errorf("front %s: stage %d (%q) of %s has nowhere to be fought",
			frontID, f.StageIndex, stage.Kind, n.ID)
	}
	return Battle{
		Front:        *f,
		NodeName:     n.Name,
		Stage:        stage,
		StageIndex:   f.StageIndex,
		StageCount:   len(n.Plan),
		Attacker:     f.Attacker,
		Defender:     f.Attacker.Other(),
		AttackerTeam: stage.AttackerTeam,
		Fields:       fields,
		Conditions:   e.theater.ConditionsFor(n, *f),
	}, nil
}

// ApplyResult records a finished battle.
//
// The attacker winning the last stage captures the node and moves the front on
// to a neighbour it can still attack. A lost battle costs the offensive a
// point of momentum and a stage (none below the first); the one that takes
// its last point breaks it and the front closes, and Reconcile opens another
// one somewhere the population wants it.
func (e *Engine) ApplyResult(frontID, matchID string, attackerWon bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if matchID != "" && e.results[matchID] {
		return nil
	}
	if e.winner != "" {
		return nil
	}

	f, ok := e.fronts[frontID]
	if !ok {
		return fmt.Errorf("no front %q", frontID)
	}
	n, ok := e.theater.Node(f.NodeID)
	if !ok {
		return fmt.Errorf("front %s: no node %q", frontID, f.NodeID)
	}

	if !attackerWon {
		if f.Momentum <= 1 {
			ev, err := e.log.Append(Event{
				Kind: EventOffensiveBroken, FrontID: f.ID, NodeID: f.NodeID,
				Side: f.Attacker, MatchID: matchID,
			})
			if err != nil {
				return err
			}
			e.apply(ev)
			return nil
		}
		back := f.StageIndex - 1
		if back < 0 {
			back = 0
		}
		ev, err := e.log.Append(Event{
			Kind: EventStageLost, FrontID: f.ID, NodeID: f.NodeID,
			Side: f.Attacker.Other(), Stage: back, MatchID: matchID,
		})
		if err != nil {
			return err
		}
		e.apply(ev)
		return nil
	}

	if f.StageIndex+1 < len(n.Plan) {
		ev, err := e.log.Append(Event{
			Kind: EventStageWon, FrontID: f.ID, NodeID: f.NodeID,
			Side: f.Attacker, Stage: f.StageIndex + 1, MatchID: matchID,
		})
		if err != nil {
			return err
		}
		e.apply(ev)
		return nil
	}

	// Final stage cleared: the node changes hands.
	captured, err := e.log.Append(Event{
		Kind: EventNodeCaptured, FrontID: f.ID, NodeID: f.NodeID,
		Side: f.Attacker, MatchID: matchID,
	})
	if err != nil {
		return err
	}
	e.apply(captured)

	attacker := f.Attacker
	closed, err := e.log.Append(Event{
		Kind: EventFrontClosed, FrontID: f.ID, NodeID: f.NodeID, Side: attacker,
		Note: "captured",
	})
	if err != nil {
		return err
	}
	e.apply(closed)
	if e.theater.HQ[attacker.Other()] == captured.NodeID {
		won, err := e.log.Append(Event{Kind: EventCampaignWon, Campaign: e.campaign, Side: attacker, NodeID: captured.NodeID, MatchID: matchID})
		if err != nil {
			return err
		}
		e.apply(won)
		return nil
	}

	// Push the front deeper: the first neighbour of the captured node still
	// held by the defender becomes the next objective.
	for _, adj := range e.theater.Neighbours(captured.NodeID) {
		if e.owner[adj] != attacker.Other() {
			continue
		}
		e.nextID++
		ev, err := e.log.Append(Event{
			Kind: EventFrontOpened, FrontID: fmt.Sprintf("front-%d", e.nextID),
			NodeID: adj, Side: attacker, Note: "advance",
		})
		if err != nil {
			return err
		}
		e.apply(ev)
		break
	}
	return nil
}

func frontNum(id string) int {
	var n int
	if _, err := fmt.Sscanf(id, "front-%d", &n); err != nil {
		return 0
	}
	return n
}
