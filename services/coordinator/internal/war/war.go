// Package war is the strategic layer: a persistent front line between RED and
// BLU that decides what the next battle should be, and moves when a battle is
// won.
//
// It is groundwork for stage three and is off unless war.enabled is set. Two
// rules keep it honest:
//
//   - the theater is data. Nodes, adjacency, stage plans and maps come from a
//     file the operator writes. Nothing here invents a place or a mission.
//   - the event log is the truth. State is a fold over an append-only log, so
//     a restart resumes the same campaign rather than a fresh one.
package war

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Side is a war faction. It is not a game team: which uniform a side wears in
// a given battle is decided per battle, so neither side is structurally the
// attacker.
type Side string

const (
	SideRED Side = "RED"
	SideBLU Side = "BLU"
)

// Other returns the opposing side.
func (s Side) Other() Side {
	if s == SideRED {
		return SideBLU
	}
	return SideRED
}

// Theater is the campaign map. It is loaded from disk and never mutated.
type Theater struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Nodes []Node `json:"nodes"`
	// Edges are undirected: "a-b" means a front can move either way.
	Edges [][2]string `json:"edges"`
	// HQ is the node each side starts owning and must not lose.
	HQ map[Side]string `json:"hq"`
	// FrontsByPopulation opens more fronts as more people play. Each entry is
	// the player count at which one more front opens, in ascending order.
	FrontsByPopulation []int `json:"fronts_by_population"`
	// Battlefields is the pool a stage of each kind may be fought on, keyed by
	// the stage's Kind. A stage that names no Map draws from its kind's pool.
	//
	// This is what keeps a campaign from being a list of hardcoded maps: a
	// node's plan says "breakthrough, then advance, then assault" and the pool
	// says where each of those can happen and how many people it takes.
	Battlefields map[string][]Battlefield `json:"battlefields,omitempty"`
}

// Battlefield is one place a stage of some kind can be fought, and what it
// costs to fight there.
//
// The player counts are the point of it. A 2v2 on arena_well and a 12v12 on
// pl_swiftwater are both legitimate battles, and the matchmaker cannot know
// which of them the queue can currently fill unless the battlefield says so.
// Zero means "use the match group's own number", which is the sane default for
// a deployment that does not care.
type Battlefield struct {
	Map string `json:"map"`
	// Mode is the operator's word for what is played here. The coordinator
	// passes it through to the briefing and does not interpret it.
	Mode         string `json:"mode,omitempty"`
	MinPlayers   int    `json:"min_players,omitempty"`
	IdealPlayers int    `json:"ideal_players,omitempty"`
	MaxPlayers   int    `json:"max_players,omitempty"`
	// AttackerTeam overrides the stage's own attacker team. An attack/defend
	// or payload map needs the attacker in BLU; a symmetric one does not care.
	AttackerTeam int32 `json:"attacker_team,omitempty"`
}

// Node is one strategic location. A node is not a map: it holds an ordered
// stage plan, and each stage names the battlefield it is fought on.
type Node struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Owner Side    `json:"owner"`
	Plan  []Stage `json:"plan"`
}

// Stage is one battle in a node's offensive.
type Stage struct {
	// Kind is the operator's own word for this stage ("breakthrough",
	// "advance", "assault"). The coordinator only passes it through -- except
	// that it is also the key into the theater's Battlefields pool.
	Kind string `json:"kind"`
	// Map pins this stage to one battlefield. Empty draws from the kind's
	// pool in Theater.Battlefields, which is the usual case: a node should
	// say what kind of fight happens there, not which map it is every time.
	Map string `json:"map,omitempty"`
	// MatchGroup overrides the queue's own group for this stage. Zero means
	// "whatever the players queued for".
	MatchGroup int32 `json:"match_group,omitempty"`
	// AttackerTeam is the game team the attacking side wears here. 3 is BLU,
	// which is what an attack/defend map expects. 0 leaves it to the server.
	AttackerTeam int32 `json:"attacker_team,omitempty"`
}

// Front is an active offensive against one node.
type Front struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	Attacker   Side   `json:"attacker"`
	StageIndex int    `json:"stage_index"`
	OpenedAt   int64  `json:"opened_at"`
}

// LoadTheater reads and validates a theater file.
func LoadTheater(path string) (*Theater, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Theater
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &t, nil
}

// Validate rejects a theater the war engine could not run.
func (t *Theater) Validate() error {
	if len(t.Nodes) == 0 {
		return errors.New("theater has no nodes")
	}
	byID := map[string]bool{}
	for i, n := range t.Nodes {
		if n.ID == "" {
			return fmt.Errorf("nodes[%d]: id must be set", i)
		}
		if byID[n.ID] {
			return fmt.Errorf("nodes[%d]: duplicate id %q", i, n.ID)
		}
		byID[n.ID] = true
		if n.Owner != SideRED && n.Owner != SideBLU {
			return fmt.Errorf("node %s: owner must be RED or BLU", n.ID)
		}
		if len(n.Plan) == 0 {
			return fmt.Errorf("node %s: needs at least one stage", n.ID)
		}
		for j, s := range n.Plan {
			if s.Kind == "" && s.Map == "" {
				return fmt.Errorf("node %s: plan[%d] has neither a kind nor a map", n.ID, j)
			}
			if s.Map == "" && len(t.Battlefields[s.Kind]) == 0 {
				return fmt.Errorf("node %s: plan[%d] is a %q stage with no map, and battlefields has no %q pool to draw one from",
					n.ID, j, s.Kind, s.Kind)
			}
		}
	}
	for kind, pool := range t.Battlefields {
		for j, b := range pool {
			if b.Map == "" {
				return fmt.Errorf("battlefields[%s][%d]: map must be set", kind, j)
			}
			switch {
			case b.MinPlayers < 0 || b.IdealPlayers < 0 || b.MaxPlayers < 0:
				return fmt.Errorf("battlefields[%s][%d] (%s): player counts cannot be negative", kind, j, b.Map)
			case b.MinPlayers%2 != 0 || b.MaxPlayers%2 != 0:
				return fmt.Errorf("battlefields[%s][%d] (%s): min_players and max_players must be even, teams are equal", kind, j, b.Map)
			case b.IdealPlayers != 0 && b.MinPlayers != 0 && b.IdealPlayers < b.MinPlayers:
				return fmt.Errorf("battlefields[%s][%d] (%s): ideal_players %d is below min_players %d", kind, j, b.Map, b.IdealPlayers, b.MinPlayers)
			case b.MaxPlayers != 0 && b.IdealPlayers != 0 && b.MaxPlayers < b.IdealPlayers:
				return fmt.Errorf("battlefields[%s][%d] (%s): max_players %d is below ideal_players %d", kind, j, b.Map, b.MaxPlayers, b.IdealPlayers)
			}
		}
	}
	for i, e := range t.Edges {
		if !byID[e[0]] || !byID[e[1]] {
			return fmt.Errorf("edges[%d]: %s-%s names an unknown node", i, e[0], e[1])
		}
	}
	for _, side := range []Side{SideRED, SideBLU} {
		hq, ok := t.HQ[side]
		if !ok || !byID[hq] {
			return fmt.Errorf("hq[%s] must name a node", side)
		}
	}
	if !sort.IntsAreSorted(t.FrontsByPopulation) {
		return errors.New("fronts_by_population must be ascending")
	}
	return nil
}

// FieldsFor returns the battlefields a stage may be fought on: the one it pins
// itself to, or its kind's whole pool. Never empty for a validated theater.
func (t *Theater) FieldsFor(s Stage) []Battlefield {
	if s.Map != "" {
		return []Battlefield{{Map: s.Map, AttackerTeam: s.AttackerTeam}}
	}
	return t.Battlefields[s.Kind]
}

// Neighbours returns the nodes adjacent to id.
func (t *Theater) Neighbours(id string) []string {
	var out []string
	for _, e := range t.Edges {
		switch id {
		case e[0]:
			out = append(out, e[1])
		case e[1]:
			out = append(out, e[0])
		}
	}
	sort.Strings(out)
	return out
}

// Node returns a node by id.
func (t *Theater) Node(id string) (Node, bool) {
	for _, n := range t.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

//
// events
//

// EventKind names what happened. The log is append-only and every kind here is
// replayed on start, so an existing kind's meaning may never change.
type EventKind string

const (
	EventCampaignStarted EventKind = "campaign_started"
	EventFrontOpened     EventKind = "front_opened"
	EventFrontClosed     EventKind = "front_closed"
	EventStageWon        EventKind = "stage_won"  // attacker took a stage
	EventStageLost       EventKind = "stage_lost" // defender held
	EventNodeCaptured    EventKind = "node_captured"
	EventOffensiveBroken EventKind = "offensive_broken"
)

// Event is one entry in the war log.
type Event struct {
	Seq      int64     `json:"seq"`
	At       int64     `json:"at"`
	Kind     EventKind `json:"kind"`
	Campaign string    `json:"campaign,omitempty"`
	FrontID  string    `json:"front_id,omitempty"`
	NodeID   string    `json:"node_id,omitempty"`
	Side     Side      `json:"side,omitempty"`
	Stage    int       `json:"stage,omitempty"`
	MatchID  string    `json:"match_id,omitempty"`
	Note     string    `json:"note,omitempty"`
}

// Log is an append-only event log on disk.
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
	seq  int64
}

// OpenLog opens (or creates) the log at path and returns it along with
// everything already in it.
func OpenLog(path string) (*Log, []Event, error) {
	var events []Event
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var e Event
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				f.Close()
				return nil, nil, fmt.Errorf("%s: %w", path, err)
			}
			events = append(events, e)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	l := &Log{path: path, f: f}
	if n := len(events); n > 0 {
		l.seq = events[n-1].Seq
	}
	return l, events, nil
}

// Append writes an event and returns it with its sequence number filled in.
func (l *Log) Append(e Event) (Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq = l.seq
	if e.At == 0 {
		e.At = time.Now().Unix()
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	if _, err := l.f.Write(append(raw, '\n')); err != nil {
		return e, err
	}
	return e, l.f.Sync()
}

// Close closes the log file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
