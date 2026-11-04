package gc

import (
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/war"
)

// WarView is the war as the game's war screen draws it
// (game/tc2/loose/resource/fui/screens/war.rml reads exactly these fields).
// The game copies it into the UI state as it is, so a field added here is a
// field the screen can use without touching C++.
//
// The screen is built around operations: an operation is an open front, an
// offensive by one side on one sector, fought as a fixed series of stages.
// Winning a stage moves the operation on, losing one pushes it back, winning
// the last one takes the sector and the front line moves. The map itself
// (territory, borders, the front line) is derived on the client from the
// sectors and their owners.
type WarView struct {
	Winner         string  `json:"winner"`
	EndedAt        int64   `json:"ended_at"`
	NextCampaignAt int64   `json:"next_campaign_at"`
	Name           string  `json:"name"`
	Campaign       string  `json:"campaign"`
	Day            int     `json:"day"`
	Days           int     `json:"days"`
	Red            float64 `json:"red"`
	RedFronts      int     `json:"red_fronts"`
	BluFronts      int     `json:"blu_fronts"`
	Battles        int     `json:"battles"`
	Soldiers       int     `json:"soldiers"`
	// The pool: servers connected to the GC, and how many of them are free
	// for the next battle. The war is never wider than the pool.
	Servers     int       `json:"servers"`
	ServersFree int       `json:"servers_free"`
	Fronts      []WarNode `json:"fronts"`
	Links       []WarLink `json:"links"`
	Ops         []WarOp   `json:"ops"`
	// Top is the sector of the war's order: the operation headquarters wants
	// finished (see warorder.go); Hot the one with the most people in it.
	Top     string `json:"top"`
	Hot     string `json:"hot"`
	OrderXP int    `json:"order_xp"`
	// Live is every war battle being fought now.
	Live   []WarBattle `json:"live"`
	Events []WarEvent  `json:"events"`
}

// WarNode is one sector. Owner is "red" or "blu"; Control is RED's share of
// it, 0..1, which only moves while an operation is on it.
type WarNode struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Icon    string  `json:"icon"`
	HQ      bool    `json:"hq"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Owner   string  `json:"owner"`
	Control float64 `json:"control"`
	// Frontline: a neighbour is the other side's, so an operation can open here.
	Frontline bool `json:"frontline"`
	// Set while an operation is on this sector.
	Front     string `json:"front"`
	Attacker  string `json:"attacker"`
	From      string `json:"from"` // the attacker's sector the push comes from
	Stage     int    `json:"stage"`
	Stages    int    `json:"stages"`
	StageKind string `json:"stage_kind"`
	Mode      string `json:"mode"`
	Map       string `json:"map"`
	Image     string `json:"image"`
	Battles   int    `json:"battles"`
	Soldiers  int    `json:"soldiers"`
	Bonus     string `json:"bonus"`
	// Banner is the clan that fought hardest for this sector (clans with the
	// banner perk only); empty tag when none.
	Banner WarBanner `json:"banner"`
}

// WarBanner is a clan's banner on a sector.
type WarBanner struct {
	Tag    string `json:"tag"`
	Name   string `json:"name"`
	Emblem string `json:"emblem"`
	Color  string `json:"color"`
}

// WarLink is a road between two sectors. Side is the owner of both ends, or
// "front" when the road crosses the front line.
type WarLink struct {
	A    string `json:"a"`
	B    string `json:"b"`
	Side string `json:"side"`
}

// WarOp is one operation, for the list beside the map.
type WarOp struct {
	Front    string   `json:"front"`
	Node     string   `json:"node"`
	Name     string   `json:"name"`
	Attacker string   `json:"attacker"`
	Defender string   `json:"defender"`
	From     string   `json:"from"`
	FromName string   `json:"from_name"`
	Stage    int      `json:"stage"` // 1-based, the one being fought
	Stages   int      `json:"stages"`
	Plan     []string `json:"plan"` // every stage's kind, in order
	Kind     string   `json:"kind"` // this stage's
	Next     string   `json:"next"` // the stage after it, "" on the last
	Prev     string   `json:"prev"` // the stage a loss falls back to, "" on the first
	Mode     string   `json:"mode"`
	Map      string   `json:"map"`
	Image    string   `json:"image"`
	Battles  int      `json:"battles"`
	Soldiers int      `json:"soldiers"`
	Want     int      `json:"want"` // players online who asked for this front
	HQ       bool     `json:"hq"`   // taking it ends the war
	// Momentum: lost battles the offensive can still take, of MomentumMax.
	Momentum    int      `json:"momentum"`
	MomentumMax int      `json:"momentum_max"`
	Conditions  []string `json:"conditions"` // what its next battle is fought under
	Order       bool     `json:"order"`
}

// WarBattle is one war battle being fought right now.
type WarBattle struct {
	Front      string   `json:"front"`
	Node       string   `json:"node"`
	Name       string   `json:"name"`
	Map        string   `json:"map"`
	Image      string   `json:"image"`
	Mode       string   `json:"mode"`
	Stage      int      `json:"stage"`
	StageKind  string   `json:"stage_kind"`
	Attacker   string   `json:"attacker"`
	Players    int      `json:"players"`
	Max        int      `json:"max"`
	Seconds    int64    `json:"seconds"` // since it went live
	Conditions []string `json:"conditions"`
	Order      bool     `json:"order"`
}

// WarEvent is a line in the front-line report. The game words it from Kind
// (fui_war_ev_<kind>), so it reads in the player's language.
type WarEvent struct {
	Kind      string `json:"kind"`
	Side      string `json:"side"`
	Node      string `json:"node"`
	Name      string `json:"name"`
	Stage     int    `json:"stage"`
	StageKind string `json:"stage_kind"`
	Ago       int64  `json:"ago"` // seconds
}

const (
	warMapW = 1140.0
	warMapH = 640.0
	// How many report lines the screen gets.
	warEvents = 14
)

// War returns the latest war view, or nil when the GC runs no war.
func (g *GC) War() *WarView { return g.warView.Load() }

func (g *GC) publishWar() {
	if g.war == nil {
		return
	}
	snap := g.war.Snapshot()
	t := snap.Theater
	now := g.now()
	v := &WarView{Name: t.Name, Campaign: snap.Campaign, Fronts: []WarNode{}, Links: []WarLink{}, Ops: []WarOp{}, Events: []WarEvent{},
		Servers: g.poolSize(), ServersFree: g.freeServers(""), Live: []WarBattle{}, OrderXP: orderBonusXP}
	order := g.orderFront()
	v.Winner, v.EndedAt = strings.ToLower(string(snap.Winner)), snap.EndedAt
	if snap.EndedAt > 0 {
		v.NextCampaignAt = snap.EndedAt + 86400
	}

	// Campaign ids end in the unix time they started.
	if i := strings.LastIndexByte(snap.Campaign, '-'); i >= 0 {
		if start, err := strconv.ParseInt(snap.Campaign[i+1:], 10, 64); err == nil {
			v.Day = int(now.Sub(time.Unix(start, 0)).Hours()/24) + 1
		}
	}

	fronts := map[string]war.Front{}
	for _, f := range snap.Fronts {
		fronts[f.NodeID] = f
	}
	battles := map[string]int{}
	soldiers := map[string]int{}
	maps := map[string]string{}
	for _, l := range g.lobbies {
		if l.War == nil {
			continue
		}
		battles[l.War.NodeID]++
		maps[l.War.NodeID] = l.Map
		if l.State == lobbyRun {
			v.Battles++
			v.Soldiers += len(l.Members)
			soldiers[l.War.NodeID] += len(l.Members)
		}
	}
	want := map[string]int{}
	for _, p := range g.online {
		if p.Online && p.WarFront != "" {
			want[p.WarFront]++
		}
	}

	pos := map[string][2]float64{}
	names := map[string]string{}
	for i, n := range t.Nodes {
		x, y := n.X, n.Y
		if x == 0 && y == 0 {
			a := 2 * math.Pi * float64(i) / float64(len(t.Nodes))
			x = warMapW/2 + math.Cos(a)*warMapW*0.36
			y = warMapH/2 + math.Sin(a)*warMapH*0.34
		}
		pos[n.ID] = [2]float64{x, y}
		names[n.ID] = n.Name
	}

	banners := map[string]WarBanner{}
	if g.clans != nil {
		for sector, c := range g.clans.Banners() {
			banners[sector] = WarBanner{Tag: c.Tag, Name: c.Name, Emblem: c.Emblem, Color: c.Color}
		}
	}

	red := 0.0
	for _, n := range t.Nodes {
		owner := snap.Owner[n.ID]
		node := WarNode{
			ID: n.ID, Name: n.Name, Icon: n.Icon, X: pos[n.ID][0], Y: pos[n.ID][1],
			Owner: side(owner), Battles: battles[n.ID], Soldiers: soldiers[n.ID],
			HQ:     t.HQ[owner] == n.ID || t.HQ[owner.Other()] == n.ID,
			Banner: banners[n.ID],
		}
		for _, adj := range t.Neighbours(n.ID) {
			node.Frontline = node.Frontline || snap.Owner[adj] != owner
		}
		if node.Icon == "" && node.HQ {
			node.Icon = "hq"
		}
		if owner == war.SideRED {
			node.Control = 1
			v.RedFronts++
		} else {
			v.BluFronts++
		}

		stageIndex := 0
		if f, ok := fronts[n.ID]; ok && len(n.Plan) > 0 {
			stageIndex = f.StageIndex
			if stageIndex >= len(n.Plan) {
				stageIndex = len(n.Plan) - 1
			}
			// The attacker holds half the sector by the last stage; taking
			// that one flips it.
			p := 0.5 * float64(stageIndex) / float64(len(n.Plan))
			if owner == war.SideRED {
				node.Control = 1 - p
			} else {
				node.Control = p
			}
			node.Front = f.ID
			node.Attacker = side(f.Attacker)
			node.Stage = stageIndex + 1
			node.Stages = len(n.Plan)
			node.From = stagingFor(t, snap.Owner, n.ID, f.Attacker, pos)
		}
		red += node.Control

		if len(n.Plan) > 0 {
			stage := n.Plan[stageIndex]
			node.StageKind = stage.Kind
			node.Map = maps[n.ID]
			if node.Map == "" {
				node.Map = stage.Map
			}
			fields := t.FieldsFor(stage)
			if node.Map == "" && len(fields) > 0 {
				// Each sector its own picture: a stable pick from the pool.
				h := fnv.New32a()
				h.Write([]byte(n.ID))
				node.Map = fields[int(h.Sum32()%uint32(len(fields)))].Map
			}
			for _, b := range fields {
				if b.Map == node.Map || node.Mode == "" {
					node.Mode = b.Mode
				}
			}
		}
		if node.Map != "" {
			node.Image = "vgui/maps/menu_thumb_" + node.Map
		}
		if t.HQ[owner] == n.ID {
			node.Bonus = "hq"
		}
		v.Fronts = append(v.Fronts, node)

		if node.Front != "" {
			plan := make([]string, len(n.Plan))
			for i, s := range n.Plan {
				plan[i] = s.Kind
			}
			at := func(i int) string {
				if i < 0 || i >= len(plan) {
					return ""
				}
				return plan[i]
			}
			v.Ops = append(v.Ops, WarOp{
				Front: node.Front, Node: n.ID, Name: n.Name,
				Attacker: node.Attacker, Defender: node.Owner,
				From: node.From, FromName: names[node.From],
				Stage: node.Stage, Stages: node.Stages, Plan: plan,
				Kind: at(node.Stage - 1), Next: at(node.Stage), Prev: at(node.Stage - 2),
				Mode: node.Mode, Map: node.Map, Image: node.Image,
				Battles: node.Battles, Soldiers: node.Soldiers, Want: want[node.Front],
				HQ: node.HQ, Momentum: fronts[n.ID].Momentum, MomentumMax: war.MaxMomentum,
				Conditions: nonNil(t.ConditionsFor(n, fronts[n.ID])), Order: node.Front == order,
			})
		}
	}
	if len(t.Nodes) > 0 {
		v.Red = red / float64(len(t.Nodes))
	}

	// The order first, then where people already are, then the operation
	// closest to its goal.
	sort.SliceStable(v.Ops, func(i, j int) bool {
		a, b := v.Ops[i], v.Ops[j]
		if a.Order != b.Order {
			return a.Order
		}
		if a.Soldiers+a.Want != b.Soldiers+b.Want {
			return a.Soldiers+a.Want > b.Soldiers+b.Want
		}
		if a.Stage != b.Stage {
			return a.Stage > b.Stage
		}
		return a.Front < b.Front
	})
	best := 0
	for _, o := range v.Ops {
		if o.Order {
			v.Top = o.Node
		}
		if o.Soldiers+o.Want > best {
			best, v.Hot = o.Soldiers+o.Want, o.Node
		}
	}
	if v.Top == "" && len(v.Ops) > 0 {
		v.Top = v.Ops[0].Node
	}

	// The battles under way.
	for _, l := range g.lobbies {
		if l.War == nil || l.State != lobbyRun {
			continue
		}
		b := WarBattle{
			Front: l.War.FrontID, Node: l.War.NodeID, Name: names[l.War.NodeID], Map: l.Map,
			Image: "vgui/maps/menu_thumb_" + l.Map, Stage: l.War.StageIndex + 1, StageKind: l.War.StageKind,
			Attacker: strings.ToLower(l.War.Attacker), Players: len(l.Members), Max: l.MaxPlayers,
			Seconds: int64(now.Sub(l.StartedAt).Seconds()), Conditions: nonNil(l.War.Conditions), Order: l.War.Order,
		}
		if n, ok := t.Node(l.War.NodeID); ok && l.War.StageIndex < len(n.Plan) {
			for _, f := range t.FieldsFor(n.Plan[l.War.StageIndex]) {
				if f.Map == l.Map {
					b.Mode = f.Mode
				}
			}
		}
		v.Live = append(v.Live, b)
	}
	sort.Slice(v.Live, func(i, j int) bool {
		if v.Live[i].Seconds != v.Live[j].Seconds {
			return v.Live[i].Seconds > v.Live[j].Seconds
		}
		return v.Live[i].Map < v.Live[j].Map
	})

	for _, e := range t.Edges {
		s := "front"
		if oa, ob := snap.Owner[e[0]], snap.Owner[e[1]]; oa == ob {
			s = side(oa)
		}
		v.Links = append(v.Links, WarLink{A: e[0], B: e[1], Side: s})
	}

	for _, ev := range snap.Recent {
		if len(v.Events) >= warEvents {
			break
		}
		we := WarEvent{Kind: string(ev.Kind), Side: side(ev.Side), Node: ev.NodeID, Name: names[ev.NodeID], Ago: now.Unix() - ev.At}
		if we.Ago < 0 {
			we.Ago = 0
		}
		if n, ok := t.Node(ev.NodeID); ok {
			// stage_won: the attacker finished stage ev.Stage (1-based) and
			// moves to the next; stage_lost: pushed back to ev.Stage+1.
			idx := -1
			switch ev.Kind {
			case war.EventStageWon:
				idx = ev.Stage - 1
			case war.EventStageLost:
				idx = ev.Stage
			}
			if idx >= 0 && idx < len(n.Plan) {
				we.Stage = idx + 1
				we.StageKind = n.Plan[idx].Kind
			}
		}
		if ev.Kind == war.EventFrontClosed {
			continue // always follows node_captured, which says it better
		}
		v.Events = append(v.Events, we)
	}
	g.warView.Store(v)
}

func side(s war.Side) string { return strings.ToLower(string(s)) }

// stagingFor is the attacker's sector an offensive on node pushes from: a
// neighbour the attacker holds, the nearest one if there are several.
func stagingFor(t *war.Theater, owner map[string]war.Side, node string, attacker war.Side, pos map[string][2]float64) string {
	best, bestD := "", math.MaxFloat64
	p := pos[node]
	for _, adj := range t.Neighbours(node) {
		if owner[adj] != attacker {
			continue
		}
		q := pos[adj]
		if d := math.Hypot(q[0]-p[0], q[1]-p[1]); d < bestD {
			best, bestD = adj, d
		}
	}
	return best
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
