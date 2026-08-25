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
	owner    map[string]Side // node id -> owner
	fronts   map[string]*Front
	nextID   int
	now      func() time.Time
}

// NewEngine builds an engine over a theater and its log, replaying whatever
// the log already contains.
func NewEngine(t *Theater, l *Log, past []Event) (*Engine, error) {
	e := &Engine{
		theater: t,
		log:     l,
		owner:   map[string]Side{},
		fronts:  map[string]*Front{},
		now:     time.Now,
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
	return e, nil
}

// apply folds one event into state. It must stay total: an event kind it does
// not know is ignored, so an older binary can still read a newer log.
func (e *Engine) apply(ev Event) {
	switch ev.Kind {
	case EventCampaignStarted:
		e.campaign = ev.Campaign
		// A new campaign resets ownership to the theater's own starting line.
		for _, n := range e.theater.Nodes {
			e.owner[n.ID] = n.Owner
		}
		e.fronts = map[string]*Front{}
	case EventFrontOpened:
		e.fronts[ev.FrontID] = &Front{
			ID:       ev.FrontID,
			NodeID:   ev.NodeID,
			Attacker: ev.Side,
			OpenedAt: ev.At,
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
func (e *Engine) Reconcile(population int) ([]Front, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	want := e.theater.WantFronts(population)
	for len(e.fronts) > want {
		// Close the newest front: the oldest has the most invested in it.
		var newest *Front
		for _, f := range e.fronts {
			if newest == nil || f.ID > newest.ID {
				newest = f
			}
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
	}, nil
}

// ApplyResult records a finished battle.
//
// The attacker winning the last stage captures the node and moves the front on
// to a neighbour it can still attack. The attacker losing the first stage
// breaks the offensive and the front closes; Reconcile will open another one
// somewhere the population wants it.
func (e *Engine) ApplyResult(frontID, matchID string, attackerWon bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	f, ok := e.fronts[frontID]
	if !ok {
		return fmt.Errorf("no front %q", frontID)
	}
	n, ok := e.theater.Node(f.NodeID)
	if !ok {
		return fmt.Errorf("front %s: no node %q", frontID, f.NodeID)
	}

	if !attackerWon {
		if f.StageIndex == 0 {
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
		ev, err := e.log.Append(Event{
			Kind: EventStageLost, FrontID: f.ID, NodeID: f.NodeID,
			Side: f.Attacker.Other(), Stage: f.StageIndex - 1, MatchID: matchID,
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
