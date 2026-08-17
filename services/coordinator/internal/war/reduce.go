package war

import (
	"fmt"
	"time"
)

// State is the whole mutable world. It is only ever changed by apply.
type State struct {
	Revision   uint64
	Seq        uint64
	Campaign   Campaign
	Nodes      map[string]*NodeState
	Fronts     map[string]*Front
	FrontOrder []string
	Mob        *Mobilization
}

func newState() *State {
	return &State{
		Nodes:  make(map[string]*NodeState),
		Fronts: make(map[string]*Front),
	}
}

// Front looks up a front by ID.
func (st *State) Front(id string) (*Front, bool) {
	f, ok := st.Fronts[id]
	return f, ok
}

// ActiveFronts returns the fronts accepting deployments, in the order they
// were opened.
func (st *State) ActiveFronts() []*Front {
	out := make([]*Front, 0, len(st.FrontOrder))
	for _, id := range st.FrontOrder {
		if f, ok := st.Fronts[id]; ok && f.Status == FrontActive {
			out = append(out, f)
		}
	}
	return out
}

// Owner reports who holds a node.
func (st *State) Owner(nodeID string) Side {
	if n, ok := st.Nodes[nodeID]; ok {
		return n.Owner
	}
	return SideNone
}

// NodeCount tallies territory per side, which is both a scoreboard and the
// input to the comeback rule.
func (st *State) NodeCount() map[Side]int {
	out := map[Side]int{SideRed: 0, SideBlu: 0, SideNone: 0}
	for _, n := range st.Nodes {
		out[n.Owner]++
	}
	return out
}

// Advance is the stage rule: what a battle does to a front.
//
// An attacker win clears a stage, and clearing the last one takes the node. A
// defender win pushes the offensive back one stage, except at the line it
// cannot be pushed past, where the offensive breaks outright — that line is
// CollapseAtStage, and it is one stage higher for a defender under DEFENSIVE
// MOBILIZATION. A stalemate consumes a battle and moves nothing.
//
// It is exported because the engine needs to describe an outcome before the
// event that causes it is written, and both must be the same rule.
func Advance(f *Front, winner Side) (stage int, status FrontStatus) {
	switch winner {
	case f.Attacker:
		next := f.Stage + 1
		if next >= len(f.Plan) {
			return len(f.Plan), FrontWon
		}
		return next, f.Status
	case f.Defender:
		if f.Stage <= f.CollapseAtStage {
			return f.Stage, FrontCollapsed
		}
		return f.Stage - 1, f.Status
	default:
		return f.Stage, f.Status
	}
}

// apply is the only writer of State. It must stay pure with respect to the
// event: no clocks, no randomness, no lookups outside the state it is given.
// Everything the coordinator decided is already in the event.
func apply(st *State, ev Event) error {
	switch ev.Kind {
	case EventCampaignStarted:
		d := ev.CampaignStarted
		if d == nil {
			return fmt.Errorf("war: %s event %d has no payload", ev.Kind, ev.Seq)
		}
		st.Campaign = Campaign{
			ID:        d.CampaignID,
			Number:    d.Number,
			Name:      d.Name,
			Theater:   d.Theater,
			Status:    CampaignActive,
			StartedAt: ev.At,
		}
		st.Nodes = make(map[string]*NodeState, len(d.Owners))
		for id, owner := range d.Owners {
			st.Nodes[id] = &NodeState{ID: id, Owner: owner}
		}
		st.Fronts = make(map[string]*Front)
		st.FrontOrder = nil
		st.Mob = nil

	case EventCampaignEnded:
		d := ev.CampaignEnded
		if d == nil {
			return fmt.Errorf("war: %s event %d has no payload", ev.Kind, ev.Seq)
		}
		st.Campaign.Status = CampaignIntermission
		st.Campaign.EndedAt = ev.At
		st.Campaign.Winner = d.Winner

	case EventFrontOpened:
		d := ev.FrontOpened
		if d == nil {
			return fmt.Errorf("war: %s event %d has no payload", ev.Kind, ev.Seq)
		}
		if _, dup := st.Fronts[d.FrontID]; dup {
			return fmt.Errorf("war: front %q opened twice", d.FrontID)
		}
		st.Fronts[d.FrontID] = &Front{
			ID:              d.FrontID,
			Name:            d.Name,
			CampaignID:      d.CampaignID,
			Attacker:        d.Attacker,
			Defender:        d.Defender,
			TargetNode:      d.TargetNode,
			SourceNode:      d.SourceNode,
			Status:          FrontActive,
			Plan:            append([]StageKind(nil), d.Plan...),
			CollapseAtStage: d.CollapseAtStage,
			OpenedAt:        ev.At,
			UpdatedAt:       ev.At,
		}
		st.FrontOrder = append(st.FrontOrder, d.FrontID)

	case EventFrontClosed:
		d := ev.FrontClosed
		if d == nil {
			return fmt.Errorf("war: %s event %d has no payload", ev.Kind, ev.Seq)
		}
		delete(st.Fronts, d.FrontID)
		for i, id := range st.FrontOrder {
			if id == d.FrontID {
				st.FrontOrder = append(st.FrontOrder[:i], st.FrontOrder[i+1:]...)
				break
			}
		}

	case EventBattleRecorded:
		d := ev.Battle
		if d == nil {
			return fmt.Errorf("war: %s event %d has no payload", ev.Kind, ev.Seq)
		}
		st.Campaign.Battles++
		f, ok := st.Fronts[d.FrontID]
		if !ok {
			return fmt.Errorf("war: battle %s recorded on unknown front %q", d.BattleID, d.FrontID)
		}
		f.Battles++
		f.UpdatedAt = ev.At
		switch d.Winner {
		case f.Attacker:
			f.AttackerWins++
		case f.Defender:
			f.DefenderWins++
		}
		f.Stage, f.Status = Advance(f, d.Winner)

	case EventNodeCaptured:
		d := ev.NodeCaptured
		if d == nil {
			return fmt.Errorf("war: %s event %d has no payload", ev.Kind, ev.Seq)
		}
		n, ok := st.Nodes[d.NodeID]
		if !ok {
			return fmt.Errorf("war: capture of unknown node %q", d.NodeID)
		}
		n.Owner = d.To
		n.CapturedAt = ev.At
		n.Captures++
		st.Campaign.Captures++

	case EventMobilization:
		d := ev.Mobilization
		if d == nil {
			return fmt.Errorf("war: %s event %d has no payload", ev.Kind, ev.Seq)
		}
		if d.Active {
			st.Mob = &Mobilization{Side: d.Side, Since: ev.At, Reason: d.Reason}
		} else {
			st.Mob = nil
		}

	case EventStory:
		// Narrative only; no mechanical effect yet.

	default:
		return fmt.Errorf("war: unknown event kind %q at seq %d", ev.Kind, ev.Seq)
	}

	st.Seq = ev.Seq
	st.Revision++
	return nil
}

// snapshot builds the client-facing view. Callers must hold the engine lock.
func (st *State) snapshot(t *Theater, now time.Time) Snapshot {
	frontsByNode := map[string][]string{}
	for _, id := range st.FrontOrder {
		f, ok := st.Fronts[id]
		if !ok || f.Status != FrontActive {
			continue
		}
		frontsByNode[f.TargetNode] = append(frontsByNode[f.TargetNode], f.ID)
		frontsByNode[f.SourceNode] = append(frontsByNode[f.SourceNode], f.ID)
	}

	nodes := make([]SnapshotNode, 0, len(t.Nodes))
	for _, def := range t.Nodes {
		ns := st.Nodes[def.ID]
		if ns == nil {
			ns = &NodeState{ID: def.ID}
		}
		nodes = append(nodes, SnapshotNode{
			ID:         def.ID,
			Name:       def.Name,
			Owner:      ns.Owner,
			HQ:         def.HQ,
			Adjacent:   append([]string(nil), def.Adjacent...),
			X:          def.X,
			Y:          def.Y,
			Contested:  len(frontsByNode[def.ID]) > 0,
			FrontIDs:   frontsByNode[def.ID],
			Captures:   ns.Captures,
			CapturedAt: ns.CapturedAt,
		})
	}

	fronts := make([]Front, 0, len(st.FrontOrder))
	for _, id := range st.FrontOrder {
		if f, ok := st.Fronts[id]; ok {
			fronts = append(fronts, *f.clone())
		}
	}

	counts := map[string]int{}
	for side, n := range st.NodeCount() {
		counts[side.String()] = n
	}

	var mob *Mobilization
	if st.Mob != nil {
		c := *st.Mob
		mob = &c
	}

	return Snapshot{
		Revision:     st.Revision,
		Theater:      t.Info(),
		Campaign:     st.Campaign,
		Nodes:        nodes,
		Fronts:       fronts,
		Mobilization: mob,
		NodeCount:    counts,
		GeneratedAt:  now,
	}
}
