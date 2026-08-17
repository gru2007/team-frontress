package war

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	mrand "math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNoSuchFront is returned for a front that is not open.
	ErrNoSuchFront = errors.New("war: no such front")
	// ErrFrontResolved means the front has already been won or broken; a late
	// result from a match that outlived it must not move anything.
	ErrFrontResolved = errors.New("war: front is already resolved")
	// ErrNotCountable is an outcome the war cannot act on — an abandoned or
	// undecided battle. It is not an error in the operational sense; the caller
	// simply tells the players the battle did not advance the front.
	ErrNotCountable = errors.New("war: outcome does not advance a front")
	// ErrCampaignOver means the campaign has ended and is in its armistice.
	ErrCampaignOver = errors.New("war: campaign is between wars")
)

// Rules are the knobs the operator can turn without touching the theater.
type Rules struct {
	// PlayersPerFront is how much population one extra active front needs. The
	// point of the whole rule is that a small community is never spread thin:
	// with fifteen people online there is exactly one place to fight.
	PlayersPerFront int
	// MaxFronts caps parallel fronts no matter how big the population gets.
	MaxFronts int
	// MobilizationDeficit is the territory gap at which the losing side is put
	// under DEFENSIVE MOBILIZATION.
	MobilizationDeficit int
	// Intermission is the armistice between a campaign ending and the next one
	// starting.
	Intermission time.Duration
	// CampaignName is the name campaigns are numbered under.
	CampaignName string
	// AttackerTeam is the in-game team the attacking side plays as, in every
	// battle, whatever the map.
	//
	// This exists because payload and attack/defend maps are built for BLU to
	// attack and nothing can change that: the cart, the spawn doors and the
	// respawn rooms are the map's own geometry. So a RED offensive on one of
	// them is fought in BLU uniforms no matter what the coordinator does.
	//
	// Given that, the honest choice is to make it a rule rather than an
	// exception: the attacking side always wears BLU, so a player's uniform
	// tells them which half of the battle they are in, and it never flips for a
	// reason they cannot see. Set it empty to let each battlefield decide, which
	// puts the surprise back.
	AttackerTeam string
}

// DefaultRules match the MVP: one front until sixteen people are online, a soft
// comeback once a side is two territories down, a short armistice between wars.
func DefaultRules() Rules {
	return Rules{
		PlayersPerFront:     16,
		MaxFronts:           4,
		MobilizationDeficit: 2,
		Intermission:        15 * time.Minute,
		CampaignName:        "THE SECOND GRAVEL WAR",
		AttackerTeam:        "blu",
	}
}

func (r *Rules) fill() {
	if r.PlayersPerFront <= 0 {
		r.PlayersPerFront = 16
	}
	if r.MaxFronts <= 0 {
		r.MaxFronts = 4
	}
	if r.MobilizationDeficit <= 0 {
		r.MobilizationDeficit = 2
	}
	if r.CampaignName == "" {
		r.CampaignName = "THE SECOND GRAVEL WAR"
	}
}

// attackerTeamFor decides which in-game team the attacking side plays as. A
// battlefield that states one wins — it is the map's own constraint — and
// otherwise the rule applies to every battle alike.
func (r Rules) attackerTeamFor(m MapEntry) string {
	if m.AttackerTeam != "" {
		return m.AttackerTeam
	}
	return r.AttackerTeam
}

// Engine owns the war. Every mutation goes through emit, so the log and the
// state can never disagree.
type Engine struct {
	mu      sync.Mutex
	theater *Theater
	log     *EventLog
	st      *State
	rules   Rules
	rng     *mrand.Rand
	now     func() time.Time

	// desired is the front count the last Reconcile asked for. Cascades that
	// replace a front (a capture, a collapse) consult it so a battle finishing
	// never widens the war behind the population's back.
	desired int
}

// NewEngine replays the log and, if the world is empty, opens the first
// campaign.
func NewEngine(t *Theater, log *EventLog, rules Rules) (*Engine, error) {
	rules.fill()
	e := &Engine{
		theater: t,
		log:     log,
		st:      newState(),
		rules:   rules,
		rng:     mrand.New(mrand.NewSource(time.Now().UnixNano())),
		now:     time.Now,
		desired: 1,
	}
	for _, ev := range log.Events() {
		if err := apply(e.st, ev); err != nil {
			return nil, fmt.Errorf("war: replay failed at seq %d: %w", ev.Seq, err)
		}
	}
	if e.st.Campaign.ID == "" {
		if err := e.startCampaign(1); err != nil {
			return nil, err
		}
	}
	if e.st.Campaign.Theater != t.ID {
		return nil, fmt.Errorf("war: log is for theater %q but %q was loaded; "+
			"point -war-log at that campaign's log or start a new one",
			e.st.Campaign.Theater, t.ID)
	}
	return e, nil
}

// Snapshot returns the whole world for the map screen.
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.st.snapshot(e.theater, e.now())
}

// Revision is the world's version, for cheap change detection.
func (e *Engine) Revision() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.st.Revision
}

// ActiveFronts returns the fronts players may deploy to.
func (e *Engine) ActiveFronts() []Front {
	e.mu.Lock()
	defer e.mu.Unlock()
	fronts := e.st.ActiveFronts()
	out := make([]Front, 0, len(fronts))
	for _, f := range fronts {
		out = append(out, *f.clone())
	}
	return out
}

// Front looks up one front.
func (e *Engine) Front(id string) (Front, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f, ok := e.st.Fronts[id]
	if !ok {
		return Front{}, false
	}
	return *f.clone(), true
}

// NodeName resolves a node's display name. Cheap enough to call per player,
// unlike building a whole snapshot for one string.
func (e *Engine) NodeName(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nodeName(id)
}

// CampaignName is the current war's name.
func (e *Engine) CampaignName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.st.Campaign.Name
}

// Timeline returns war events after seq, oldest first — the world recap.
func (e *Engine) Timeline(since uint64, limit int) []Event { return e.log.Since(since, limit) }

// Recap returns the newest n events, oldest first.
func (e *Engine) Recap(n int) []Event { return e.log.Tail(n) }

// Close flushes the log.
func (e *Engine) Close() error { return e.log.Close() }

// ---------------------------------------------------------------------------
// Planning the next battle
// ---------------------------------------------------------------------------

// PlanBattle answers "what does the next match on this front look like at this
// population". The strategic event stays the same — BATTLE FOR FOUNDRY, stage
// two — while the shape of the battle adapts to how many people are actually
// online.
func (e *Engine) PlanBattle(frontID string, players int) (BattlePlan, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	f, ok := e.st.Fronts[frontID]
	if !ok {
		return BattlePlan{}, fmt.Errorf("%w: %s", ErrNoSuchFront, frontID)
	}
	if f.Status != FrontActive {
		return BattlePlan{}, fmt.Errorf("%w: %s", ErrFrontResolved, frontID)
	}
	prof, ok := e.theater.Profile(f.TargetNode)
	if !ok {
		return BattlePlan{}, fmt.Errorf("war: node %q has no battle profile", f.TargetNode)
	}

	kind := f.CurrentStage()
	entry, ok := e.pickMap(prof, kind, players)
	if !ok {
		// Nothing at this stage fits the queue. A skirmish is the honest answer:
		// the offensive is still at the same stage, it is simply being fought by
		// six people instead of twenty-four.
		entry, ok = e.pickMap(prof, StageSkirmish, players)
	}
	if !ok {
		entry, ok = e.smallestMap(prof)
	}
	if !ok {
		return BattlePlan{}, fmt.Errorf("war: battle profile %q has no usable battlefield", prof.ID)
	}

	node, _ := e.theater.Node(f.TargetNode)
	return BattlePlan{
		FrontID:      f.ID,
		FrontName:    f.Name,
		CampaignID:   f.CampaignID,
		Attacker:     f.Attacker,
		Defender:     f.Defender,
		TargetNode:   f.TargetNode,
		Stage:        f.Stage,
		StageCount:   len(f.Plan),
		StageKind:    kind,
		Map:          entry.Map,
		Mode:         entry.Mode,
		MinPlayers:   entry.MinPlayers,
		MaxPlayers:   entry.MaxPlayers,
		AttackerTeam: e.rules.attackerTeamFor(entry),
		Headline: fmt.Sprintf("%s — %s %d/%d AT %s",
			strings.ToUpper(kind.Label()), f.Attacker, f.Stage+1, len(f.Plan), nodeName(node, f.TargetNode)),
	}, nil
}

// pickMap chooses the battlefield whose ideal population is closest to what is
// queued, keeping some rotation between equally good ones.
func (e *Engine) pickMap(prof *BattleProfile, kind StageKind, players int) (MapEntry, bool) {
	var fits []MapEntry
	for _, m := range prof.Battlefields[kind] {
		if players >= m.MinPlayers && players <= m.MaxPlayers {
			fits = append(fits, m)
		}
	}
	if len(fits) == 0 {
		return MapEntry{}, false
	}
	sort.Slice(fits, func(i, j int) bool {
		di, dj := absInt(idealOf(fits[i])-players), absInt(idealOf(fits[j])-players)
		if di != dj {
			return di < dj
		}
		return fits[i].Map < fits[j].Map
	})
	best := absInt(idealOf(fits[0]) - players)
	n := 0
	for n < len(fits) && absInt(idealOf(fits[n])-players) == best {
		n++
	}
	return fits[e.rng.Intn(n)], true
}

// smallestMap is the last resort for a queue below every declared envelope.
func (e *Engine) smallestMap(prof *BattleProfile) (MapEntry, bool) {
	var best MapEntry
	found := false
	for _, kind := range []StageKind{StageSkirmish, StageBreakthrough, StageAdvance, StageAssault} {
		for _, m := range prof.Battlefields[kind] {
			if !found || m.MinPlayers < best.MinPlayers {
				best, found = m, true
			}
		}
	}
	return best, found
}

func idealOf(m MapEntry) int {
	if m.IdealPlayers > 0 {
		return m.IdealPlayers
	}
	return (m.MinPlayers + m.MaxPlayers) / 2
}

// ---------------------------------------------------------------------------
// Recording a battle
// ---------------------------------------------------------------------------

// RecordBattle applies one finished match to the war and reports what changed.
// It is the only entry point that moves a front, and it is called exactly once
// per counted match.
func (e *Engine) RecordBattle(res BattleResult) (Update, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if res.Outcome == OutcomeUnknown || res.Outcome == OutcomeAbandoned {
		return Update{}, fmt.Errorf("%w: %s", ErrNotCountable, res.Outcome)
	}
	f, ok := e.st.Fronts[res.FrontID]
	if !ok {
		return Update{}, fmt.Errorf("%w: %s", ErrNoSuchFront, res.FrontID)
	}
	if f.Status != FrontActive {
		return Update{}, fmt.Errorf("%w: %s", ErrFrontResolved, res.FrontID)
	}
	if e.st.Campaign.Status != CampaignActive {
		return Update{}, ErrCampaignOver
	}

	from := e.log.Seq()
	winner := res.Outcome.Winner()
	stage, kind := f.Stage, f.CurrentStage()
	target := f.TargetNode
	targetName := e.nodeName(target)
	attacker, defender := f.Attacker, f.Defender

	// The headline describes where the front ends up, so it has to be written
	// against the projected state rather than the one still in memory. Advance
	// is the same rule the reducer applies a moment later.
	nextStage, nextStatus := Advance(f, winner)
	headline := battleHeadline(f, winner, kind, f.StageKindAt(nextStage), nextStatus, targetName)

	if err := e.emit(Event{
		Kind:     EventBattleRecorded,
		Headline: headline,
		Battle: &BattleRecordedData{
			BattleID:  res.BattleID,
			FrontID:   f.ID,
			ServerID:  res.ServerID,
			Outcome:   res.Outcome,
			Winner:    winner,
			RedScore:  res.RedScore,
			BluScore:  res.BluScore,
			Map:       res.Map,
			Mode:      res.Mode,
			Stage:     stage,
			StageKind: kind,
			Players:   res.Players,
			DurationS: int64(res.Duration.Seconds()),
		},
	}); err != nil {
		return Update{}, err
	}

	up := Update{
		FrontID:     f.ID,
		FrontName:   f.Name,
		Winner:      winner,
		Attacker:    attacker,
		Stage:       f.Stage,
		StageCount:  len(f.Plan),
		StageKind:   f.CurrentStage(),
		FrontStatus: f.Status,
		Headline:    headline,
	}

	switch f.Status {
	case FrontWon:
		owner := e.st.Owner(target)
		if err := e.emit(Event{
			Kind:     EventNodeCaptured,
			Headline: fmt.Sprintf("%s CAPTURED %s", attacker, targetName),
			NodeCaptured: &NodeCapturedData{
				NodeID: target, Name: targetName, From: owner, To: attacker, FrontID: f.ID,
			},
		}); err != nil {
			return up, err
		}
		if err := e.closeFront(f.ID, FrontWon, "objective captured"); err != nil {
			return up, err
		}
		up.NodeCaptured = true
		up.NodeID = target
		up.NewOwner = attacker
		up.Headline = fmt.Sprintf("%s CAPTURED %s", attacker, targetName)

		if hq := e.theater.HQ(defender); hq == target {
			if err := e.endCampaign(attacker, fmt.Sprintf("%s headquarters overrun", defender)); err != nil {
				return up, err
			}
			up.CampaignOver = true
			up.CampaignWon = attacker
			up.Headline = fmt.Sprintf("%s WINS %s", attacker, e.st.Campaign.Name)
		} else {
			e.evaluateMobilization()
			// The front physically moves: the offensive that just took a node
			// rolls into the next one rather than the map going quiet.
			e.openFronts(1, "offensive continues past "+targetName)
		}

	case FrontCollapsed:
		if err := e.closeFront(f.ID, FrontCollapsed, "offensive collapsed"); err != nil {
			return up, err
		}
		up.Collapsed = true
		up.Headline = fmt.Sprintf("%s OFFENSIVE AT %s COLLAPSED", attacker, targetName)
		e.evaluateMobilization()
		// The defender that broke the attack gets to push back at where it came
		// from. One series of matches becomes a story rather than a reset.
		if e.st.Owner(f.SourceNode) == attacker {
			if id, ok := e.openCounterOffensive(defender, attacker, f.SourceNode, target); ok {
				up.CounterFront = id
			}
		} else {
			e.openFronts(1, "front reopened after a collapsed offensive")
		}

	default:
		e.evaluateMobilization()
	}

	up.Revision = e.st.Revision
	up.Events = e.log.Since(from, 32)
	return up, nil
}

// battleHeadline states what the battle did to the offensive, in the words the
// world recap and the post-match screen both use.
func battleHeadline(f *Front, winner Side, cleared, next StageKind, status FrontStatus, targetName string) string {
	switch {
	case winner == SideNone:
		return fmt.Sprintf("STALEMATE AT %s", targetName)
	case winner == f.Attacker && status == FrontWon:
		return fmt.Sprintf("%s CLEARED %s AT %s", winner, cleared.Label(), targetName)
	case winner == f.Attacker:
		return fmt.Sprintf("%s %s COMPLETE AT %s — NEXT: %s",
			winner, cleared.Label(), targetName, next.Label())
	case status == FrontCollapsed:
		return fmt.Sprintf("%s BROKE THE %s OFFENSIVE AT %s", winner, f.Attacker, targetName)
	default:
		return fmt.Sprintf("%s HELD AT %s — %s PUSHED BACK TO %s",
			winner, targetName, f.Attacker, next.Label())
	}
}

// ---------------------------------------------------------------------------
// Reconciliation: how wide the war is allowed to be
// ---------------------------------------------------------------------------

// Reconcile matches the number of open fronts to the population and starts the
// next campaign once the armistice is over. busy reports whether a front
// currently has battles running on it, so a quiet evening never closes a front
// people are fighting on.
//
// This is the mechanic that keeps a fifteen-player community feeling like a war
// instead of a thinly spread server browser.
func (e *Engine) Reconcile(population int, busy func(frontID string) bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.st.Campaign.Status == CampaignIntermission {
		if e.now().Sub(e.st.Campaign.EndedAt) < e.rules.Intermission {
			return nil
		}
		return e.startCampaign(e.st.Campaign.Number + 1)
	}

	want := e.frontsFor(population)
	e.desired = want
	active := e.st.ActiveFronts()

	if len(active) < want {
		e.openFronts(want-len(active), fmt.Sprintf("population %d supports %d fronts", population, want))
		return nil
	}
	// Stand down the quietest surplus fronts, newest first, and only ones that
	// nobody is fighting on and nobody has bled for yet.
	for i := len(active) - 1; i >= 0 && len(e.st.ActiveFronts()) > want; i-- {
		f := active[i]
		if f.Battles > 0 || (busy != nil && busy(f.ID)) {
			continue
		}
		if err := e.closeFront(f.ID, FrontClosed, "population no longer supports this many fronts"); err != nil {
			return err
		}
	}
	return nil
}

// frontsFor is the width of the war at a given population: one front until
// PlayersPerFront people are online, then one more per step, capped.
func (e *Engine) frontsFor(population int) int {
	if population < 0 {
		population = 0
	}
	n := 1 + population/e.rules.PlayersPerFront
	if n > e.rules.MaxFronts {
		n = e.rules.MaxFronts
	}
	return n
}

// ---------------------------------------------------------------------------
// Front selection
// ---------------------------------------------------------------------------

type candidate struct {
	attacker, defender Side
	target, source     string
	score              float64
	reason             string
}

// openFronts opens up to n new fronts from the best available candidates.
func (e *Engine) openFronts(n int, reason string) {
	if e.st.Campaign.Status != CampaignActive {
		return
	}
	for i := 0; i < n; i++ {
		cands := e.candidates()
		if len(cands) == 0 {
			return
		}
		if _, err := e.openFront(cands[0], reason); err != nil {
			return
		}
	}
}

// openCounterOffensive opens the specific front where a broken offensive came
// from, rather than whatever scored highest.
func (e *Engine) openCounterOffensive(attacker, defender Side, target, source string) (string, bool) {
	if e.st.Campaign.Status != CampaignActive {
		return "", false
	}
	if e.st.Owner(target) != defender || e.st.Owner(source) != attacker {
		return "", false
	}
	c := candidate{attacker: attacker, defender: defender, target: target, source: source}
	id, err := e.openFront(c, fmt.Sprintf("%s counter-offensive", attacker))
	if err != nil {
		return "", false
	}
	return id, true
}

// candidates scores every place the front could legitimately be, best first.
//
// The ranking is deliberately readable rather than clever: push toward the
// enemy headquarters, keep the momentum of a side that just took ground, and
// give a side that is losing the map somewhere to push back.
func (e *Engine) candidates() []candidate {
	occupied := map[string]bool{}
	for _, f := range e.st.ActiveFronts() {
		occupied[f.TargetNode] = true
		occupied[f.SourceNode] = true
	}

	distToHQ := map[Side]map[string]int{
		SideRed: e.theater.distances(e.theater.HQ(SideRed)),
		SideBlu: e.theater.distances(e.theater.HQ(SideBlu)),
	}
	lastCapture := e.lastCapturedNode()

	var out []candidate
	add := func(c candidate) {
		if occupied[c.target] || occupied[c.source] {
			return
		}
		c.score = 1
		// Toward the defender's headquarters: a war that wanders sideways never
		// ends, and a campaign that never ends has no history.
		if d, ok := distToHQ[c.defender][c.target]; ok {
			c.score += 1.5 / float64(1+d)
		}
		// Momentum: the side that just took the ground it attacks from keeps
		// rolling, which is what turns a capture into an offensive.
		if c.source == lastCapture && e.st.Owner(c.source) == c.attacker {
			c.score += 1.0
			c.reason = "momentum"
		}
		// Soft comeback: the losing side gets offensives of its own, and battles
		// where it defends are preferred over unrelated ones.
		if e.st.Mob != nil {
			switch e.st.Mob.Side {
			case c.attacker:
				c.score += 1.5
				c.reason = "defensive mobilization: counter-attack"
			case c.defender:
				c.score += 0.5
				c.reason = "defensive mobilization: defence"
			}
		}
		c.score += e.rng.Float64() * 0.25
		out = append(out, c)
	}

	for _, def := range e.theater.Nodes {
		owner := e.st.Owner(def.ID)
		if owner == SideNone {
			// A neutral node is the contested objective both sides want. The
			// front over it is still RED against BLU: whoever attacks, the other
			// side is defending its claim, and the winner takes the ground.
			byOwner := map[Side][]string{}
			for _, adj := range def.Adjacent {
				if o := e.st.Owner(adj); o != SideNone {
					byOwner[o] = append(byOwner[o], adj)
				}
			}
			for _, side := range []Side{SideRed, SideBlu} {
				if len(byOwner[side]) == 0 || len(byOwner[side.Opposite()]) == 0 {
					continue
				}
				add(candidate{
					attacker: side, defender: side.Opposite(),
					target: def.ID, source: byOwner[side][0],
				})
			}
			continue
		}
		for _, adj := range def.Adjacent {
			if target := e.st.Owner(adj); target != SideNone && target != owner {
				add(candidate{attacker: owner, defender: target, target: adj, source: def.ID})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if math.Abs(out[i].score-out[j].score) > 1e-9 {
			return out[i].score > out[j].score
		}
		if out[i].target != out[j].target {
			return out[i].target < out[j].target
		}
		return out[i].source < out[j].source
	})
	return out
}

func (e *Engine) lastCapturedNode() string {
	var newest string
	var at time.Time
	for id, n := range e.st.Nodes {
		if n.CapturedAt.After(at) {
			newest, at = id, n.CapturedAt
		}
	}
	return newest
}

// openFront emits the decision to fight over a node, with the stage plan and
// collapse threshold already resolved.
func (e *Engine) openFront(c candidate, reason string) (string, error) {
	prof, ok := e.theater.Profile(c.target)
	if !ok {
		return "", fmt.Errorf("war: node %q has no battle profile", c.target)
	}
	plan := append([]StageKind(nil), prof.Stages...)
	collapse := 0
	if e.st.Mob != nil && e.st.Mob.Side == c.defender {
		// The mobilized side's defence needs one stage less: breaking the
		// attack one step earlier than usual.
		collapse = 1
		if collapse >= len(plan) {
			collapse = len(plan) - 1
		}
	}
	if c.reason != "" {
		reason = c.reason + "; " + reason
	}
	name := "BATTLE FOR " + strings.ToUpper(e.nodeName(c.target))
	id := newFrontID(c.target)
	return id, e.emit(Event{
		Kind:     EventFrontOpened,
		Headline: fmt.Sprintf("%s OPENED AN OFFENSIVE AT %s", c.attacker, e.nodeName(c.target)),
		FrontOpened: &FrontOpenedData{
			FrontID:         id,
			Name:            name,
			CampaignID:      e.st.Campaign.ID,
			Attacker:        c.attacker,
			Defender:        c.defender,
			TargetNode:      c.target,
			SourceNode:      c.source,
			Plan:            plan,
			CollapseAtStage: collapse,
			Reason:          reason,
		},
	})
}

func (e *Engine) closeFront(id string, status FrontStatus, reason string) error {
	return e.emit(Event{
		Kind:        EventFrontClosed,
		Headline:    fmt.Sprintf("FRONT %s CLOSED: %s", id, reason),
		FrontClosed: &FrontClosedData{FrontID: id, Status: status, Reason: reason},
	})
}

// ---------------------------------------------------------------------------
// Campaigns and mobilization
// ---------------------------------------------------------------------------

func (e *Engine) startCampaign(number int) error {
	opening := e.theater.OpeningFor(number)
	owners := make(map[string]Side, len(e.theater.Nodes))
	for _, n := range e.theater.Nodes {
		owners[n.ID] = opening.Owners[n.ID] // absent means neutral, which is legal
	}
	name := fmt.Sprintf("%s — CAMPAIGN %02d", e.rules.CampaignName, number)
	if err := e.emit(Event{
		Kind:     EventCampaignStarted,
		Headline: name + " BEGINS",
		CampaignStarted: &CampaignStartedData{
			CampaignID: fmt.Sprintf("c%02d-%s", number, randHex(3)),
			Number:     number,
			Name:       name,
			Theater:    e.theater.ID,
			Opening:    opening.Name,
			Owners:     owners,
		},
	}); err != nil {
		return err
	}
	e.openFronts(e.desired, "campaign opening")
	return nil
}

func (e *Engine) endCampaign(winner Side, reason string) error {
	c := e.st.Campaign
	for _, f := range e.st.ActiveFronts() {
		if err := e.closeFront(f.ID, FrontClosed, "campaign ended"); err != nil {
			return err
		}
	}
	return e.emit(Event{
		Kind:     EventCampaignEnded,
		Headline: fmt.Sprintf("%s WINS %s", winner, c.Name),
		CampaignEnded: &CampaignEndedData{
			CampaignID: c.ID,
			Winner:     winner,
			Battles:    c.Battles,
			Captures:   c.Captures,
			DurationS:  int64(e.now().Sub(c.StartedAt).Seconds()),
			Reason:     reason,
		},
	})
}

// evaluateMobilization turns the comeback state on when a side is far enough
// behind on territory, and off again once it has clawed back. It never touches
// a result: it only changes which fronts open and how deep an offensive against
// that side has to go.
func (e *Engine) evaluateMobilization() {
	counts := e.st.NodeCount()
	red, blu := counts[SideRed], counts[SideBlu]
	gap := red - blu
	losing := SideBlu
	if gap < 0 {
		gap, losing = -gap, SideRed
	}

	switch {
	case gap >= e.rules.MobilizationDeficit && (e.st.Mob == nil || e.st.Mob.Side != losing):
		_ = e.emit(Event{
			Kind:     EventMobilization,
			Headline: fmt.Sprintf("%s DEFENSIVE MOBILIZATION", losing),
			Mobilization: &MobilizationData{
				Side: losing, Active: true,
				Reason: fmt.Sprintf("%d territories behind", gap),
			},
		})
	case gap <= e.rules.MobilizationDeficit-2 && e.st.Mob != nil:
		_ = e.emit(Event{
			Kind:     EventMobilization,
			Headline: fmt.Sprintf("%s MOBILIZATION ENDED", e.st.Mob.Side),
			Mobilization: &MobilizationData{
				Side: e.st.Mob.Side, Active: false, Reason: "territory restored",
			},
		})
	}
}

// MobilizedSide reports which side, if any, is under defensive mobilization.
// The coordinator uses it to bias mercenary contracts towards the side that
// needs bodies.
func (e *Engine) MobilizedSide() Side {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.st.Mob == nil {
		return SideNone
	}
	return e.st.Mob.Side
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

// emit stamps, persists and applies one event. Persisting first is the whole
// contract: state the log does not contain would vanish on restart, and a war
// that forgets a capture is worse than one that stalls.
func (e *Engine) emit(ev Event) error {
	ev.At = e.now()
	if err := e.log.append(&ev); err != nil {
		return err
	}
	if err := apply(e.st, ev); err != nil {
		return fmt.Errorf("war: event %d (%s) could not be applied: %w", ev.Seq, ev.Kind, err)
	}
	return nil
}

func (e *Engine) nodeName(id string) string {
	n, _ := e.theater.Node(id)
	return nodeName(n, id)
}

func nodeName(n *NodeDef, id string) string {
	if n == nil {
		return id
	}
	return n.Name
}

func newFrontID(target string) string { return target + "-" + randHex(3) }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("war: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
