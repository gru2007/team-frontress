package war

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Theater is the static definition of a war: the node graph, the battlefields
// each node can be fought over, and the opening positions a campaign starts
// from. Nothing in here changes while the coordinator runs — the whole mutable
// world is State.
//
// A strategic node is not a Source map. FOUNDRY 17 is a district; cp_process,
// pl_badwater and cp_dustbowl are all places inside it. That indirection is
// what lets seven nodes carry a war without seven bespoke maps.
type Theater struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Nodes    []*NodeDef       `json:"nodes"`
	Profiles []*BattleProfile `json:"battle_profiles"`
	// Openings are the starting owner layouts campaigns rotate through, so
	// campaign 2 is not a replay of campaign 1. At least one is required.
	Openings []Opening `json:"openings"`

	nodeByID    map[string]*NodeDef
	profileByID map[string]*BattleProfile
}

// NodeDef is a territory's static half.
type NodeDef struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Adjacent []string `json:"adjacent"`
	// HQ marks this node as a side's headquarters. Taking it ends the campaign.
	HQ Side `json:"hq,omitempty"`
	// Profile names the battle profile used when this node is the target of an
	// offensive.
	Profile string `json:"profile"`
	// X and Y are layout hints for whoever draws the map. The engine ignores
	// them; they live here so the client needs exactly one document.
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// BattleProfile is what fighting over a node actually looks like: the stage
// plan an offensive must clear, and the battlefields available at each stage.
type BattleProfile struct {
	ID string `json:"id"`
	// Stages is the default capture plan, in order.
	Stages []StageKind `json:"stages"`
	// Battlefields lists the maps usable at each stage kind. A stage with no
	// entry that fits the current population falls back to skirmish.
	Battlefields map[StageKind][]MapEntry `json:"battlefields"`
}

// MapEntry is one battlefield with the population envelope it plays well at.
type MapEntry struct {
	Map          string `json:"map"`
	Mode         string `json:"mode"`
	MinPlayers   int    `json:"min_players"`
	IdealPlayers int    `json:"ideal_players"`
	MaxPlayers   int    `json:"max_players"`
	// AttackerTeam is the in-game team that attacks on this map. Payload and
	// attack/defend maps are built for BLU attacking, so a RED offensive on one
	// of them plays as the BLU team for that battle and the coordinator does the
	// translation. Empty means the map is symmetric.
	AttackerTeam string `json:"attacker_team,omitempty"`
}

// Opening is one starting layout for a campaign.
type Opening struct {
	Name   string          `json:"name"`
	Owners map[string]Side `json:"owners"`
}

// LoadTheater reads and validates a theater file. A missing or invalid theater
// is fatal: an operator who mistypes a path should find out at startup, not
// when the first player presses DEPLOY.
func LoadTheater(path string) (*Theater, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("war: read theater %s: %w", path, err)
	}
	var t Theater
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("war: parse theater %s: %w", path, err)
	}
	if err := t.compile(); err != nil {
		return nil, fmt.Errorf("war: theater %s: %w", path, err)
	}
	return &t, nil
}

// compile indexes and validates the theater.
func (t *Theater) compile() error {
	if t.ID == "" {
		return fmt.Errorf("theater has no id")
	}
	if len(t.Nodes) < 2 {
		return fmt.Errorf("theater declares %d nodes; a war needs at least two", len(t.Nodes))
	}
	t.nodeByID = make(map[string]*NodeDef, len(t.Nodes))
	for _, n := range t.Nodes {
		if n.ID == "" {
			return fmt.Errorf("a node has no id")
		}
		if _, dup := t.nodeByID[n.ID]; dup {
			return fmt.Errorf("duplicate node %q", n.ID)
		}
		if n.Name == "" {
			n.Name = n.ID
		}
		t.nodeByID[n.ID] = n
	}

	t.profileByID = make(map[string]*BattleProfile, len(t.Profiles))
	for _, p := range t.Profiles {
		if p.ID == "" {
			return fmt.Errorf("a battle profile has no id")
		}
		if _, dup := t.profileByID[p.ID]; dup {
			return fmt.Errorf("duplicate battle profile %q", p.ID)
		}
		if len(p.Stages) == 0 {
			return fmt.Errorf("battle profile %q has no stages", p.ID)
		}
		for _, s := range p.Stages {
			if !s.Valid() {
				return fmt.Errorf("battle profile %q has unknown stage %q", p.ID, s)
			}
		}
		if len(p.Battlefields) == 0 {
			return fmt.Errorf("battle profile %q has no battlefields", p.ID)
		}
		for kind, maps := range p.Battlefields {
			if !kind.Valid() {
				return fmt.Errorf("battle profile %q has battlefields for unknown stage %q", p.ID, kind)
			}
			for _, m := range maps {
				if m.Map == "" {
					return fmt.Errorf("battle profile %q stage %q has a battlefield with no map", p.ID, kind)
				}
				if m.MinPlayers <= 0 || m.MaxPlayers < m.MinPlayers {
					return fmt.Errorf("battle profile %q map %q has an invalid player envelope (%d-%d)",
						p.ID, m.Map, m.MinPlayers, m.MaxPlayers)
				}
			}
		}
		// Every profile must be able to produce something at a tiny population,
		// otherwise a front can go dark the moment six people are online.
		if len(p.Battlefields[StageSkirmish]) == 0 {
			return fmt.Errorf("battle profile %q has no skirmish battlefields; "+
				"a front with a small queue would have nothing to play", p.ID)
		}
		t.profileByID[p.ID] = p
	}

	// Adjacency is undirected: declaring it once is enough, and a one-sided
	// link is almost always a typo rather than a one-way front.
	for _, n := range t.Nodes {
		for _, adj := range n.Adjacent {
			other, ok := t.nodeByID[adj]
			if !ok {
				return fmt.Errorf("node %q links to unknown node %q", n.ID, adj)
			}
			if adj == n.ID {
				return fmt.Errorf("node %q links to itself", n.ID)
			}
			if !contains(other.Adjacent, n.ID) {
				other.Adjacent = append(other.Adjacent, n.ID)
			}
		}
		if n.Profile == "" {
			return fmt.Errorf("node %q has no battle profile", n.ID)
		}
		if _, ok := t.profileByID[n.Profile]; !ok {
			return fmt.Errorf("node %q uses unknown battle profile %q", n.ID, n.Profile)
		}
		sort.Strings(n.Adjacent)
	}

	hq := map[Side]string{}
	for _, n := range t.Nodes {
		if n.HQ == SideNone {
			continue
		}
		if prev, ok := hq[n.HQ]; ok {
			return fmt.Errorf("%s has two headquarters (%s and %s)", n.HQ, prev, n.ID)
		}
		hq[n.HQ] = n.ID
	}
	if hq[SideRed] == "" || hq[SideBlu] == "" {
		return fmt.Errorf("both sides need a headquarters; a campaign has no win condition without one")
	}

	if len(t.Openings) == 0 {
		return fmt.Errorf("theater declares no openings")
	}
	for i, op := range t.Openings {
		if len(op.Owners) == 0 {
			return fmt.Errorf("opening %d (%s) assigns no owners", i, op.Name)
		}
		for id := range op.Owners {
			if _, ok := t.nodeByID[id]; !ok {
				return fmt.Errorf("opening %d (%s) assigns unknown node %q", i, op.Name, id)
			}
		}
		for side, node := range hq {
			if op.Owners[node] != side {
				return fmt.Errorf("opening %d (%s) does not start %s in control of its headquarters %s",
					i, op.Name, side, node)
			}
		}
	}
	return nil
}

// Node looks up a node definition.
func (t *Theater) Node(id string) (*NodeDef, bool) {
	n, ok := t.nodeByID[id]
	return n, ok
}

// Profile returns the battle profile for a node.
func (t *Theater) Profile(nodeID string) (*BattleProfile, bool) {
	n, ok := t.nodeByID[nodeID]
	if !ok {
		return nil, false
	}
	p, ok := t.profileByID[n.Profile]
	return p, ok
}

// HQ returns a side's headquarters node.
func (t *Theater) HQ(s Side) string {
	for _, n := range t.Nodes {
		if n.HQ == s {
			return n.ID
		}
	}
	return ""
}

// Info is the theater's identity, for snapshots.
func (t *Theater) Info() TheaterInfo { return TheaterInfo{ID: t.ID, Name: t.Name} }

// OpeningFor picks the opening a campaign number starts from, rotating through
// the declared list so consecutive campaigns are not identical.
func (t *Theater) OpeningFor(number int) Opening {
	if number < 1 {
		number = 1
	}
	return t.Openings[(number-1)%len(t.Openings)]
}

// distances returns hop counts from a node to every other node, ignoring
// ownership. Used to tell "toward the enemy HQ" from "sideways".
func (t *Theater) distances(from string) map[string]int {
	dist := map[string]int{from: 0}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		n, ok := t.nodeByID[cur]
		if !ok {
			continue
		}
		for _, adj := range n.Adjacent {
			if _, seen := dist[adj]; seen {
				continue
			}
			dist[adj] = dist[cur] + 1
			queue = append(queue, adj)
		}
	}
	return dist
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
