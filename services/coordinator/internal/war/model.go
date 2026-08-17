// Package war is the strategic layer of GREYLINE FRONTRESS.
//
// The war is not a scoreboard drawn on top of ordinary matches. It is the thing
// that decides what the next match is: which two territories are fighting, what
// stage that offensive has reached, and therefore which mode and map the next
// battle uses. A finished battle feeds exactly one event back in, and that event
// is what moves the front, flips a node, or ends a campaign.
//
// Everything mutable lives in State and is only ever changed by applying an
// Event (see reduce.go). The engine decides — which front to open, which map to
// play — and bakes every decision into the event it appends, so replaying the
// log rebuilds the same world without re-deciding anything.
package war

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Side is a belligerent. The war has exactly two for the MVP; Machines get a
// third value later, which is why this is not a bool.
type Side uint8

const (
	SideNone Side = 0
	SideRed  Side = 1
	SideBlu  Side = 2
)

func (s Side) String() string {
	switch s {
	case SideRed:
		return "RED"
	case SideBlu:
		return "BLU"
	default:
		return "NONE"
	}
}

// Opposite returns the other belligerent. SideNone has no opposite.
func (s Side) Opposite() Side {
	switch s {
	case SideRed:
		return SideBlu
	case SideBlu:
		return SideRed
	default:
		return SideNone
	}
}

func (s Side) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *Side) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("war: side must be a string: %w", err)
	}
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "RED":
		*s = SideRed
	case "BLU", "BLUE":
		*s = SideBlu
	case "", "NONE", "NEUTRAL":
		*s = SideNone
	default:
		return fmt.Errorf("war: unknown side %q", raw)
	}
	return nil
}

// ParseSide reads a side from client or operator input.
func ParseSide(raw string) (Side, error) {
	var s Side
	if err := json.Unmarshal([]byte(`"`+strings.ToUpper(strings.TrimSpace(raw))+`"`), &s); err != nil {
		return SideNone, err
	}
	return s, nil
}

// Outcome is a finished battle's verdict as far as the war cares. Anything the
// game server could not decide arrives as OutcomeUnknown and never reaches the
// war state.
type Outcome uint8

const (
	OutcomeUnknown Outcome = iota
	OutcomeRedWin
	OutcomeBluWin
	OutcomeStalemate
	OutcomeAbandoned
)

func (o Outcome) String() string {
	switch o {
	case OutcomeRedWin:
		return "RED_WIN"
	case OutcomeBluWin:
		return "BLU_WIN"
	case OutcomeStalemate:
		return "STALEMATE"
	case OutcomeAbandoned:
		return "ABANDONED"
	default:
		return "UNKNOWN"
	}
}

func (o Outcome) MarshalJSON() ([]byte, error) { return json.Marshal(o.String()) }

func (o *Outcome) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("war: outcome must be a string: %w", err)
	}
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "RED_WIN", "RED":
		*o = OutcomeRedWin
	case "BLU_WIN", "BLU", "BLUE_WIN":
		*o = OutcomeBluWin
	case "STALEMATE", "DRAW", "TIE":
		*o = OutcomeStalemate
	case "ABANDONED":
		*o = OutcomeAbandoned
	case "", "UNKNOWN":
		*o = OutcomeUnknown
	default:
		return fmt.Errorf("war: unknown outcome %q", raw)
	}
	return nil
}

// ParseOutcome reads an outcome reported by a game server.
func ParseOutcome(raw string) (Outcome, error) {
	var o Outcome
	if err := json.Unmarshal([]byte(`"`+raw+`"`), &o); err != nil {
		return OutcomeUnknown, err
	}
	return o, nil
}

// Winner maps an outcome onto the side that won it, if any.
func (o Outcome) Winner() Side {
	switch o {
	case OutcomeRedWin:
		return SideRed
	case OutcomeBluWin:
		return SideBlu
	default:
		return SideNone
	}
}

// StageKind is one step of an offensive. It is the strategic meaning of a
// battle, not its technical game mode: a BREAKTHROUGH may be 5CP at twelve
// players and a KOTH point at six.
type StageKind string

const (
	StageSkirmish     StageKind = "skirmish"
	StageBreakthrough StageKind = "breakthrough"
	StageAdvance      StageKind = "advance"
	StageAssault      StageKind = "assault"
)

// Label is the stage name players see.
func (k StageKind) Label() string { return strings.ToUpper(string(k)) }

// Valid reports whether the stage is one the engine knows how to plan.
func (k StageKind) Valid() bool {
	switch k {
	case StageSkirmish, StageBreakthrough, StageAdvance, StageAssault:
		return true
	}
	return false
}

// FrontStatus is where an offensive stands.
type FrontStatus string

const (
	// FrontActive accepts deployments.
	FrontActive FrontStatus = "ACTIVE"
	// FrontWon means the attacker cleared the last stage; the node flips and the
	// front closes in the same batch of events.
	FrontWon FrontStatus = "WON"
	// FrontCollapsed means the defender broke the offensive. A counter-offensive
	// usually opens in its place.
	FrontCollapsed FrontStatus = "COLLAPSED"
	// FrontClosed was retired by the coordinator, not decided by play.
	FrontClosed FrontStatus = "CLOSED"
)

// CampaignStatus is the phase of the current war.
type CampaignStatus string

const (
	CampaignActive       CampaignStatus = "ACTIVE"
	CampaignIntermission CampaignStatus = "INTERMISSION"
)

// Front is one active conflict: a named offensive by one side against one
// adjacent enemy node, fought over several stages.
//
// It holds deliberately little. Everything a client wants to say about it —
// "RED offensive, stage 2 of 3" — comes from these fields, and everything else
// is derivable from the event log.
type Front struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	CampaignID string      `json:"campaign_id"`
	Attacker   Side        `json:"attacker"`
	Defender   Side        `json:"defender"`
	TargetNode string      `json:"target_node"`
	SourceNode string      `json:"source_node"`
	Status     FrontStatus `json:"status"`

	// Plan is the ordered stage list this offensive must clear. Its length is
	// the front's depth; a mobilized defender is attacked over a shorter plan
	// only when the coordinator says so at open time, never retroactively.
	Plan  []StageKind `json:"plan"`
	Stage int         `json:"stage"`
	// CollapseAtStage is the stage at which a defensive win breaks the offensive
	// outright instead of pushing it back one step. Normally 0; a defender under
	// DEFENSIVE MOBILIZATION gets 1, so its defence needs one stage less.
	CollapseAtStage int `json:"collapse_at_stage"`

	OpenedAt     time.Time `json:"opened_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Battles      int       `json:"battles"`
	AttackerWins int       `json:"attacker_wins"`
	DefenderWins int       `json:"defender_wins"`

	// RecentMaps is what this front has just been fought on, newest first, so
	// the next battle can avoid repeating it. A small community plays the same
	// front all evening, and a war that keeps serving the same map is the
	// fastest way to end the evening — the map is the only thing a player sees
	// change between one battle and the next.
	//
	// It is rebuilt from the event log like everything else, so a restart does
	// not reset the rotation.
	RecentMaps []string `json:"recent_maps,omitempty"`
}

// FrontMapMemory is how many battles back a front remembers. Two is enough to
// break the A-B-A-B alternation a single slot produces while still letting a
// three-map pool come round again quickly.
const FrontMapMemory = 2

// RememberMap pushes a map onto the front's recent list, newest first.
func (f *Front) RememberMap(name string) {
	if name == "" {
		return
	}
	out := make([]string, 0, FrontMapMemory)
	out = append(out, name)
	for _, m := range f.RecentMaps {
		if m == name || len(out) >= FrontMapMemory {
			continue
		}
		out = append(out, m)
	}
	f.RecentMaps = out
}

// PlayedRecently reports whether this front has just been fought on this map.
func (f *Front) PlayedRecently(name string) bool {
	for _, m := range f.RecentMaps {
		if m == name {
			return true
		}
	}
	return false
}

// StageKindAt returns the stage kind for an index, clamped into the plan.
func (f *Front) StageKindAt(i int) StageKind {
	if len(f.Plan) == 0 {
		return StageBreakthrough
	}
	if i < 0 {
		i = 0
	}
	if i >= len(f.Plan) {
		i = len(f.Plan) - 1
	}
	return f.Plan[i]
}

// CurrentStage is the stage the next battle on this front fights.
func (f *Front) CurrentStage() StageKind { return f.StageKindAt(f.Stage) }

// Depth is how many wins the attacker still needs.
func (f *Front) Depth() int { return len(f.Plan) - f.Stage }

// Involves reports whether a side is fighting on this front.
func (f *Front) Involves(s Side) bool { return s == f.Attacker || s == f.Defender }

func (f *Front) clone() *Front {
	c := *f
	c.Plan = append([]StageKind(nil), f.Plan...)
	return &c
}

// NodeState is a territory's mutable half.
type NodeState struct {
	ID         string    `json:"id"`
	Owner      Side      `json:"owner"`
	CapturedAt time.Time `json:"captured_at,omitzero"`
	// Captures counts how many times this node changed hands in the campaign,
	// which is what makes a node "the one everybody keeps fighting over".
	Captures int `json:"captures"`
}

// Campaign is one whole war, from opening positions to an enemy HQ falling.
type Campaign struct {
	ID        string         `json:"id"`
	Number    int            `json:"number"`
	Name      string         `json:"name"`
	Theater   string         `json:"theater"`
	Status    CampaignStatus `json:"status"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   time.Time      `json:"ended_at,omitzero"`
	Winner    Side           `json:"winner"`
	Battles   int            `json:"battles"`
	Captures  int            `json:"captures"`
}

// Mobilization is the soft comeback state for a side that is losing ground.
// It never fakes a result; it only changes which fronts open and how deep an
// offensive against that side has to go.
type Mobilization struct {
	Side   Side      `json:"side"`
	Since  time.Time `json:"since"`
	Reason string    `json:"reason"`
}

// BattlePlan is the coordinator's answer to "what is the next match here".
type BattlePlan struct {
	FrontID    string    `json:"front_id"`
	FrontName  string    `json:"front_name"`
	CampaignID string    `json:"campaign_id"`
	Attacker   Side      `json:"attacker"`
	Defender   Side      `json:"defender"`
	TargetNode string    `json:"target_node"`
	Stage      int       `json:"stage"`
	StageCount int       `json:"stage_count"`
	StageKind  StageKind `json:"stage_kind"`
	Map        string    `json:"map"`
	Mode       string    `json:"mode"`
	MinPlayers int       `json:"min_players"`
	MaxPlayers int       `json:"max_players"`
	// AttackerTeam is the in-game team the attacking side must play as. Payload
	// and attack/defend maps hardcode BLU as the attacker, so when RED is on the
	// offensive its mercenaries play the BLU team for that battle. Empty means
	// the map is symmetric and each side plays its own colour.
	AttackerTeam string `json:"attacker_team,omitempty"`
	Headline     string `json:"headline"`
}

// GameTeam maps a war side onto the in-game team for this battle.
func (p BattlePlan) GameTeam(s Side) Side {
	if p.AttackerTeam == "" {
		return s
	}
	attackerTeam := SideBlu
	if strings.EqualFold(p.AttackerTeam, "red") {
		attackerTeam = SideRed
	}
	if s == p.Attacker {
		return attackerTeam
	}
	return attackerTeam.Opposite()
}

// WarSide is the inverse of GameTeam: which belligerent was playing as the
// given in-game team. A game server only knows RED scored three and BLU scored
// one; on a directional map that has to be read back into the war's own sides
// before it can move a front.
func (p BattlePlan) WarSide(gameTeam Side) Side {
	if p.AttackerTeam == "" {
		return gameTeam
	}
	attackerTeam := SideBlu
	if strings.EqualFold(p.AttackerTeam, "red") {
		attackerTeam = SideRed
	}
	if gameTeam == attackerTeam {
		return p.Attacker
	}
	return p.Defender
}

// TranslateResult converts a scoreline reported in in-game teams into the war's
// own sides. It is a no-op on symmetric maps and a swap on the ones where the
// attacker has to play BLU.
func (p BattlePlan) TranslateResult(outcome Outcome, gameRed, gameBlu uint32) (Outcome, uint32, uint32) {
	redSide := p.WarSide(SideRed)
	scores := map[Side]uint32{redSide: gameRed, redSide.Opposite(): gameBlu}

	switch outcome {
	case OutcomeRedWin:
		outcome = winFor(p.WarSide(SideRed))
	case OutcomeBluWin:
		outcome = winFor(p.WarSide(SideBlu))
	}
	return outcome, scores[SideRed], scores[SideBlu]
}

func winFor(s Side) Outcome {
	if s == SideBlu {
		return OutcomeBluWin
	}
	return OutcomeRedWin
}

// BattleResult is one finished battle, as reported by the game server that ran
// it. Exactly one of these produces exactly one war event.
type BattleResult struct {
	BattleID   string        `json:"battle_id"`
	FrontID    string        `json:"front_id"`
	Outcome    Outcome       `json:"outcome"`
	RedScore   uint32        `json:"red_score"`
	BluScore   uint32        `json:"blu_score"`
	Map        string        `json:"map"`
	Mode       string        `json:"mode"`
	Players    int           `json:"players"`
	Duration   time.Duration `json:"-"`
	ServerID   string        `json:"server_id"`
	ReportedAt time.Time     `json:"reported_at"`
}

// Update is what a reported battle did to the war — the post-match screen and
// the only thing the coordinator has to hand back to players.
type Update struct {
	FrontID      string      `json:"front_id"`
	FrontName    string      `json:"front_name"`
	Winner       Side        `json:"winner"`
	Attacker     Side        `json:"attacker"`
	Stage        int         `json:"stage"`
	StageCount   int         `json:"stage_count"`
	StageKind    StageKind   `json:"stage_kind"`
	FrontStatus  FrontStatus `json:"front_status"`
	NodeCaptured bool        `json:"node_captured"`
	NodeID       string      `json:"node_id,omitempty"`
	NewOwner     Side        `json:"new_owner,omitempty"`
	Collapsed    bool        `json:"offensive_collapsed"`
	CounterFront string      `json:"counter_front,omitempty"`
	CampaignOver bool        `json:"campaign_over"`
	CampaignWon  Side        `json:"campaign_winner,omitempty"`
	Headline     string      `json:"headline"`
	Revision     uint64      `json:"revision"`
	// Events are the war events this battle produced, newest last. Clients that
	// keep a timeline can append these without refetching the world.
	Events []Event `json:"events,omitempty"`
}

// Snapshot is an immutable view of the whole war, for the map screen.
type Snapshot struct {
	Revision     uint64         `json:"revision"`
	Theater      TheaterInfo    `json:"theater"`
	Campaign     Campaign       `json:"campaign"`
	Nodes        []SnapshotNode `json:"nodes"`
	Fronts       []Front        `json:"fronts"`
	Mobilization *Mobilization  `json:"mobilization,omitempty"`
	NodeCount    map[string]int `json:"node_count"`
	GeneratedAt  time.Time      `json:"generated_at"`
}

// SnapshotNode joins a node's static definition with its current owner, so a
// client can draw the map from one document.
type SnapshotNode struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Owner      Side      `json:"owner"`
	HQ         Side      `json:"hq,omitempty"`
	Adjacent   []string  `json:"adjacent"`
	X          float64   `json:"x"`
	Y          float64   `json:"y"`
	Contested  bool      `json:"contested"`
	FrontIDs   []string  `json:"front_ids,omitempty"`
	Captures   int       `json:"captures"`
	CapturedAt time.Time `json:"captured_at,omitzero"`
}

// TheaterInfo identifies the map the campaign is fought on.
type TheaterInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
