// Package mm is matchmaking over the dedicated server pool.
//
// It is the middle of the loop the whole game is built on:
//
//	war engine  ->  "the next battle at Foundry is an ADVANCE"
//	mm          ->  picks the players, sizes the battle, reserves a server
//	server pool ->  runs it and reports who won
//	war engine  ->  moves the front, and decides the next battle
//
// The coordinator never simulates a game and the game never touches war state.
// One finished match produces exactly one war event, and it goes through here.
package mm

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/greyline-frontress/coordinator/internal/pool"
	"github.com/greyline-frontress/coordinator/internal/war"
)

// Config is the matchmaker's half of the coordinator configuration.
type Config struct {
	// TeamSizes are the per-team sizes the coordinator will form, any order.
	TeamSizes []int
	// MinTeamSize is the smallest battle worth forming at all.
	MinTeamSize int
	// FormWait is how long the queue holds out for a bigger battle before
	// settling for a smaller one.
	FormWait time.Duration
	// MaxBattlesPerFront caps parallel battles on one front.
	MaxBattlesPerFront int
	// BootDeadline is how long a reserved server has to come up.
	BootDeadline time.Duration
	// JoinDeadline is how long players have to reach a ready server.
	JoinDeadline time.Duration
	// MatchTimeout is the longest a battle may run before the coordinator gives
	// up waiting for a result.
	MatchTimeout time.Duration
	// SessionTimeout is how long a client may go without polling before it is
	// treated as gone.
	SessionTimeout time.Duration
	// HeartbeatInterval is what clients are told to poll at.
	HeartbeatInterval time.Duration
}

// DefaultConfig is tuned for the MVP: small battles, one front, a handful of
// people online.
func DefaultConfig() Config {
	return Config{
		TeamSizes:          []int{2, 3, 4, 6},
		MinTeamSize:        2,
		FormWait:           45 * time.Second,
		MaxBattlesPerFront: 2,
		BootDeadline:       90 * time.Second,
		JoinDeadline:       120 * time.Second,
		MatchTimeout:       75 * time.Minute,
		SessionTimeout:     90 * time.Second,
		HeartbeatInterval:  10 * time.Second,
	}
}

// Stats are counters for the admin endpoint.
type Stats struct {
	Deploys   uint64 `json:"deploys"`
	Formed    uint64 `json:"matches_formed"`
	Live      uint64 `json:"matches_live"`
	Finished  uint64 `json:"matches_finished"`
	Aborted   uint64 `json:"matches_aborted"`
	Counted   uint64 `json:"battles_counted"`
	Contracts uint64 `json:"contracts_signed"`
	NoServer  uint64 `json:"formations_without_a_server"`
}

// Matchmaker owns players, queues and matches.
type Matchmaker struct {
	mu   sync.Mutex
	cfg  Config
	log  *slog.Logger
	war  *war.Engine
	pool *pool.Pool
	now  func() time.Time

	players map[uint64]*Player
	byToken map[string]*Player
	matches map[string]*Match

	lastRevision uint64
	stats        Stats
}

// New builds a matchmaker.
func New(cfg Config, log *slog.Logger, engine *war.Engine, servers *pool.Pool) *Matchmaker {
	if cfg.MinTeamSize <= 0 {
		cfg.MinTeamSize = 2
	}
	if len(cfg.TeamSizes) == 0 {
		cfg.TeamSizes = []int{2, 3, 4, 6}
	}
	sort.Ints(cfg.TeamSizes)
	return &Matchmaker{
		cfg:          cfg,
		log:          log,
		war:          engine,
		pool:         servers,
		now:          time.Now,
		players:      make(map[uint64]*Player),
		byToken:      make(map[string]*Player),
		matches:      make(map[string]*Match),
		lastRevision: engine.Revision(),
	}
}

// Config returns the matchmaker's configuration, for the hello reply.
func (m *Matchmaker) Config() Config { return m.cfg }

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// Hello admits a client. An account that was already connected keeps its place:
// reconnecting must not cost a player their queue position or their battle.
func (m *Matchmaker) Hello(steamID uint64, name string, side war.Side, region string) *Player {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()

	p, ok := m.players[steamID]
	if !ok {
		p = newPlayer(steamID, name, now)
		m.players[steamID] = p
		m.byToken[p.token] = p
	}
	if name != "" {
		p.Name = name
	}
	if side != war.SideNone {
		p.Side = side
	} else if p.Side == war.SideNone {
		p.Side = m.lightestSide()
	}
	if region != "" {
		p.Region = region
	}
	p.LastSeen = now
	return p
}

// Authenticate resolves a bearer token to its player.
func (m *Matchmaker) Authenticate(token string) (*Player, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byToken[token]
	if ok {
		p.LastSeen = m.now()
	}
	return p, ok
}

// Poll returns the player's events newer than since, waiting up to wait for
// one to arrive. An empty result is a normal answer, not an error: it means the
// client is up to date and should poll again.
func (m *Matchmaker) Poll(ctx context.Context, p *Player, since uint64, wait time.Duration) []Event {
	m.Touch(p)
	if events := p.drain(since); len(events) > 0 {
		return events
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-p.wake:
	case <-timer.C:
	case <-ctx.Done():
	}
	m.Touch(p)
	return p.drain(since)
}

// Touch records that a client is still there.
func (m *Matchmaker) Touch(p *Player) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p.LastSeen = m.now()
}

// lightestSide is the allegiance the war needs more of. Called with the lock.
func (m *Matchmaker) lightestSide() war.Side {
	// A side under defensive mobilization is the one short of bodies, so new
	// mercenaries default to it before anything else is considered.
	if mob := m.war.MobilizedSide(); mob != war.SideNone {
		return mob
	}
	red, blu := 0, 0
	for _, p := range m.players {
		switch p.Side {
		case war.SideRed:
			red++
		case war.SideBlu:
			blu++
		}
	}
	if blu < red {
		return war.SideBlu
	}
	return war.SideRed
}

// SetSide changes a player's allegiance between battles.
func (m *Matchmaker) SetSide(p *Player, side war.Side) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.State == StateAssigned || p.State == StatePlaying {
		return fmt.Errorf("mm: cannot change sides during a battle")
	}
	p.Side = side
	return nil
}

// ---------------------------------------------------------------------------
// DEPLOY
// ---------------------------------------------------------------------------

// Deploy puts a player in the queue. frontID may be empty, which is the plain
// DEPLOY button: the coordinator decides where they are needed.
func (m *Matchmaker) Deploy(p *Player, frontID string, acceptContract bool) (QueueStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()

	if p.State == StateAssigned || p.State == StatePlaying {
		return QueueStatus{}, fmt.Errorf("mm: already in a battle")
	}
	if p.Side == war.SideNone {
		p.Side = m.lightestSide()
	}

	fronts := m.war.ActiveFronts()
	if len(fronts) == 0 {
		return QueueStatus{}, fmt.Errorf("mm: the war has no active front right now")
	}
	target := ""
	if frontID != "" {
		for _, f := range fronts {
			if f.ID == frontID {
				target = f.ID
				break
			}
		}
		if target == "" {
			return QueueStatus{}, fmt.Errorf("mm: front %q is not accepting deployments", frontID)
		}
	} else {
		target = m.bestFrontFor(p, fronts)
	}

	// Deploying again while already queued for the same front is a retry — a
	// reconnect, a double-click, a client that lost its reply. It must not cost
	// the player their place: the queue time is what decides who waits longest
	// and when the coordinator stops holding out for a bigger battle, so
	// resetting it here would let a repeating client wait forever.
	if p.State != StateQueued || p.FrontID != target {
		p.QueuedAt = now
		m.stats.Deploys++
	}
	p.State = StateQueued
	p.FrontID = target
	p.LastSeen = now
	p.AcceptContract = acceptContract

	status := m.queueStatusLocked(p)
	p.push(Event{Type: EventQueue, Queue: &status})
	m.log.Info("player deployed", "steam_id", p.SteamID, "front", target,
		"side", p.Side, "contract_ok", acceptContract)
	return status, nil
}

// bestFrontFor is the DEPLOY decision: send the player where they will get into
// a battle soonest, preferring a front that is one player short of forming.
func (m *Matchmaker) bestFrontFor(p *Player, fronts []war.Front) string {
	type scored struct {
		id    string
		score float64
	}
	need := 2 * m.cfg.MinTeamSize
	var best []scored
	for _, f := range fronts {
		queued := 0
		for _, other := range m.players {
			if other.State == StateQueued && other.FrontID == f.ID {
				queued++
			}
		}
		s := 0.0
		// Closest to forming a battle wins: joining a queue of three beats
		// starting a queue of one.
		if queued > 0 {
			s += 2.0 - float64(abs(need-queued-1))*0.1
		}
		// A front where the player's own side is defending is where their side
		// most needs them.
		if f.Defender == p.Side {
			s += 0.5
		}
		if m.liveOnFrontLocked(f.ID) >= m.cfg.MaxBattlesPerFront {
			s -= 3.0
		}
		best = append(best, scored{f.ID, s})
	}
	sort.Slice(best, func(i, j int) bool {
		if best[i].score != best[j].score {
			return best[i].score > best[j].score
		}
		return best[i].id < best[j].id
	})
	return best[0].id
}

// Cancel takes a player out of the queue.
func (m *Matchmaker) Cancel(p *Player) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.State != StateQueued {
		return
	}
	p.State = StateIdle
	p.FrontID = ""
	status := QueueStatus{Queued: false, Message: "deployment cancelled"}
	p.push(Event{Type: EventQueue, Queue: &status})
}

// QueueStatusFor is the queue view for one player.
func (m *Matchmaker) QueueStatusFor(p *Player) QueueStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queueStatusLocked(p)
}

func (m *Matchmaker) queueStatusLocked(p *Player) QueueStatus {
	if p.State != StateQueued {
		return QueueStatus{Queued: false, Side: p.Side, Message: "not deployed"}
	}
	inQueue, ahead := 0, 0
	for _, other := range m.players {
		if other.State != StateQueued || other.FrontID != p.FrontID {
			continue
		}
		inQueue++
		if other.QueuedAt.Before(p.QueuedAt) {
			ahead++
		}
	}
	needed := 2*m.cfg.MinTeamSize - inQueue
	if needed < 0 {
		needed = 0
	}
	msg := "searching for a battle"
	if needed > 0 {
		msg = fmt.Sprintf("waiting for %d more mercenaries", needed)
	}
	name := p.FrontID
	if f, ok := m.war.Front(p.FrontID); ok {
		name = f.Name
	}
	return QueueStatus{
		Queued: true, FrontID: p.FrontID, FrontName: name, Side: p.Side,
		PlayersInQueue: inQueue, PlayersNeeded: needed, Position: ahead + 1,
		WaitedS: int(m.now().Sub(p.QueuedAt).Seconds()), Message: msg,
	}
}

// ---------------------------------------------------------------------------
// Forming battles
// ---------------------------------------------------------------------------

// tryForm walks every active front and forms whatever the queue supports.
// Called with the lock held.
func (m *Matchmaker) tryForm() {
	for _, f := range m.war.ActiveFronts() {
		for m.formOne(f) {
		}
	}
}

func (m *Matchmaker) formOne(f war.Front) bool {
	if m.liveOnFrontLocked(f.ID) >= m.cfg.MaxBattlesPerFront {
		return false
	}
	queued := m.queuedOn(f.ID)
	if len(queued) < 2*m.cfg.MinTeamSize {
		return false
	}

	teamSize := 0
	maxSize := m.cfg.TeamSizes[len(m.cfg.TeamSizes)-1]
	for _, sz := range m.cfg.TeamSizes {
		if 2*sz <= len(queued) {
			teamSize = sz
		}
	}
	if teamSize == 0 {
		return false
	}
	// Hold out for a bigger battle only while somebody has not been waiting
	// long. Eight players online should become one 4v4, not two 2v2s.
	if teamSize < maxSize && m.now().Sub(queued[0].QueuedAt) < m.cfg.FormWait {
		return false
	}

	total := 2 * teamSize
	plan, err := m.war.PlanBattle(f.ID, total)
	if err != nil {
		m.log.Warn("war layer could not plan a battle", "front", f.ID, "err", err)
		return false
	}
	if total < plan.MinPlayers {
		// The smallest battlefield this front can offer still needs more people
		// than are queued. Waiting is the honest answer.
		return false
	}
	if plan.MaxPlayers > 0 && total > plan.MaxPlayers {
		total = plan.MaxPlayers - plan.MaxPlayers%2
		teamSize = total / 2
	}

	picked := queued[:total]
	slots := assignSlots(picked, teamSize, plan)
	matchID := newID("b")

	server, err := m.pool.Reserve(matchID, pickRegion(picked), m.now())
	if err != nil {
		m.stats.NoServer++
		// Nobody is dropped from the queue: the battle is formed the moment a
		// machine frees up. Telling players why they are waiting is better than
		// a silent queue.
		m.noticeQueued(f.ID, "waiting for a free battle server")
		return false
	}

	match := &Match{
		ID:         matchID,
		FrontID:    f.ID,
		Plan:       plan,
		Slots:      slots,
		ServerID:   server.ID,
		Connect:    server.ConnectAddress,
		Password:   pool.NewPassword(),
		State:      MatchForming,
		CreatedAt:  m.now(),
		StateSince: m.now(),
	}
	m.matches[match.ID] = match
	for _, s := range slots {
		p := m.players[s.SteamID]
		if p == nil {
			continue
		}
		p.State = StateAssigned
		p.MatchID = match.ID
		if s.Contract {
			p.Contracts++
			m.stats.Contracts++
		}
	}
	m.stats.Formed++

	if err := m.pool.Send(server.ID, pool.Command{
		Type:       pool.CommandAssign,
		MatchID:    match.ID,
		Assignment: m.assignmentFor(match),
	}); err != nil {
		m.log.Error("could not hand the battle to its server", "match", match.ID,
			"server", server.ID, "err", err)
		m.abort(match, "the assigned server did not take the battle")
		return false
	}
	match.setState(MatchBooting, m.now())
	m.log.Info("battle formed", "match", match.ID, "front", f.ID, "server", server.ID,
		"map", plan.Map, "mode", plan.Mode, "size", fmt.Sprintf("%dv%d", teamSize, teamSize),
		"stage", fmt.Sprintf("%d/%d %s", plan.Stage+1, plan.StageCount, plan.StageKind))

	m.pushMatchState(match, "battle server is loading "+plan.Map)
	return true
}

// assignSlots splits the picked players into two sides. Allegiance is honoured
// where it can be; the rest are given contracts for the other side, which is
// what a war of mercenaries can do and a war of armies cannot.
func assignSlots(picked []*Player, teamSize int, plan war.BattlePlan) []*Slot {
	slots := make([]*Slot, 0, len(picked))
	counts := map[war.Side]int{}
	var leftovers []*Player

	place := func(p *Player, side war.Side) {
		counts[side]++
		slots = append(slots, &Slot{
			SteamID:  p.SteamID,
			Name:     p.Name,
			Side:     side,
			Team:     plan.GameTeam(side),
			Contract: side != p.Side,
		})
	}

	for _, p := range picked {
		if p.Side != war.SideNone && counts[p.Side] < teamSize {
			place(p, p.Side)
			continue
		}
		leftovers = append(leftovers, p)
	}
	// Volunteers first: a player who ticked "I'll fight for either side" should
	// be the one who gets moved, not somebody who did not.
	sort.SliceStable(leftovers, func(i, j int) bool {
		return leftovers[i].AcceptContract && !leftovers[j].AcceptContract
	})
	for _, p := range leftovers {
		if counts[war.SideRed] <= counts[war.SideBlu] && counts[war.SideRed] < teamSize {
			place(p, war.SideRed)
		} else {
			place(p, war.SideBlu)
		}
	}
	return slots
}

func (m *Matchmaker) assignmentFor(match *Match) *pool.Assignment {
	plan := match.Plan
	roster := make([]pool.RosterEntry, 0, len(match.Slots))
	for _, s := range match.Slots {
		roster = append(roster, pool.RosterEntry{
			SteamID:  s.SteamID,
			Name:     s.Name,
			Side:     s.Side.String(),
			Team:     s.Team.String(),
			Contract: s.Contract,
		})
	}
	nodeName := m.war.NodeName(plan.TargetNode)
	mob := ""
	if s := m.war.MobilizedSide(); s != war.SideNone {
		mob = s.String()
	}
	return &pool.Assignment{
		MatchID:    match.ID,
		Map:        plan.Map,
		Mode:       plan.Mode,
		Password:   match.Password,
		MaxPlayers: len(match.Slots),
		Roster:     roster,
		Briefing: pool.Briefing{
			FrontID:    plan.FrontID,
			FrontName:  plan.FrontName,
			NodeID:     plan.TargetNode,
			NodeName:   nodeName,
			Campaign:   m.war.CampaignName(),
			Attacker:   plan.Attacker.String(),
			Defender:   plan.Defender.String(),
			Stage:      plan.Stage + 1,
			StageCount: plan.StageCount,
			StageKind:  string(plan.StageKind),
			Reason:     briefingReason(plan, nodeName),
			Mobilized:  mob,
		},
		BootDeadlineS:   int(m.cfg.BootDeadline.Seconds()),
		ResultDeadlineS: int(m.cfg.MatchTimeout.Seconds()),
	}
}

// briefingReason is the one line that tells a player why they are on this map,
// in this mode, right now. Without it the strategic layer is invisible from
// inside the game.
func briefingReason(plan war.BattlePlan, nodeName string) string {
	switch plan.StageKind {
	case war.StageBreakthrough:
		return fmt.Sprintf("%s is breaking the line at %s", plan.Attacker, nodeName)
	case war.StageAdvance:
		return fmt.Sprintf("%s is pushing the offensive deeper into %s", plan.Attacker, nodeName)
	case war.StageAssault:
		return fmt.Sprintf("%s is storming the last positions at %s", plan.Attacker, nodeName)
	case war.StageSkirmish:
		return fmt.Sprintf("a skirmish decides the next move at %s", nodeName)
	default:
		return fmt.Sprintf("%s offensive at %s", plan.Attacker, nodeName)
	}
}

// ---------------------------------------------------------------------------
// Reports from the server pool
// ---------------------------------------------------------------------------

// ServerState records a game server's progress on its battle.
func (m *Matchmaker) ServerState(serverID, matchID, state string, players int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	match, ok := m.matches[matchID]
	if !ok {
		return fmt.Errorf("mm: unknown battle %q", matchID)
	}
	if match.ServerID != serverID {
		return fmt.Errorf("mm: battle %q is not hosted by server %q", matchID, serverID)
	}
	now := m.now()

	switch state {
	case "ready":
		if match.State == MatchBooting {
			match.setState(MatchReady, now)
			m.sendToBattle(match)
		}
	case "live":
		if !match.over() {
			if match.State != MatchLive {
				m.stats.Live++
			}
			match.setState(MatchLive, now)
			for _, s := range match.Slots {
				if p := m.players[s.SteamID]; p != nil {
					p.State = StatePlaying
				}
			}
			m.pushMatchState(match, "battle is live")
		}
	case "failed":
		m.abort(match, "the battle server could not host this battle")
	default:
		return fmt.Errorf("mm: unknown server state %q", state)
	}
	return nil
}

// ServerResult records a finished battle: the one place a game result becomes a
// war event.
func (m *Matchmaker) ServerResult(serverID string, res war.BattleResult) (*war.Update, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	match, ok := m.matches[res.BattleID]
	if !ok {
		return nil, fmt.Errorf("mm: unknown battle %q", res.BattleID)
	}
	if match.ServerID != serverID {
		return nil, fmt.Errorf("mm: battle %q is not hosted by server %q", res.BattleID, serverID)
	}
	if match.over() {
		// A retrying agent must not move the war twice.
		return nil, nil
	}

	// The server reports what its scoreboard said, in in-game teams. On a
	// directional map the attacking side played as BLU, so the scoreline has to
	// be read back into the war's own sides before it can move anything. The
	// plan is the only place that mapping lives, so the translation happens
	// here rather than in every agent.
	res.Outcome, res.RedScore, res.BluScore = match.Plan.TranslateResult(res.Outcome, res.RedScore, res.BluScore)

	res.FrontID = match.FrontID
	res.Map = match.Plan.Map
	res.Mode = match.Plan.Mode
	if res.Players == 0 {
		res.Players = len(match.Slots)
	}
	match.Result = &res
	match.RedScore, match.BluScore = res.RedScore, res.BluScore
	match.setState(MatchFinished, m.now())
	m.stats.Finished++

	var update *war.Update
	counted := false
	if up, err := m.war.RecordBattle(res); err != nil {
		m.log.Warn("battle did not advance the war", "match", match.ID,
			"outcome", res.Outcome, "err", err)
	} else {
		update = &up
		counted = true
		m.stats.Counted++
		m.log.Info("battle recorded", "match", match.ID, "front", match.FrontID,
			"outcome", res.Outcome, "headline", up.Headline)
	}

	for _, s := range match.Slots {
		p := m.players[s.SteamID]
		if p == nil {
			continue
		}
		p.State = StateIdle
		p.MatchID = ""
		p.Battles++
		over := &MatchOver{
			MatchID:  match.ID,
			Outcome:  res.Outcome,
			Won:      res.Outcome.Winner() == s.Side,
			RedScore: res.RedScore,
			BluScore: res.BluScore,
			Counted:  counted,
			Update:   update,
			Message:  resultMessage(counted, update),
		}
		p.push(Event{Type: EventMatchOver, Over: over})
	}
	m.pool.Release(serverID, true)
	m.broadcastWorldLocked(update)
	return update, nil
}

func resultMessage(counted bool, up *war.Update) string {
	if !counted {
		return "battle ended without advancing the front"
	}
	if up != nil {
		return up.Headline
	}
	return "battle recorded"
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// sendToBattle hands every player their connect details. Called with the lock.
func (m *Matchmaker) sendToBattle(match *Match) {
	red, blu := match.sideCounts()
	for _, s := range match.Slots {
		p := m.players[s.SteamID]
		if p == nil {
			continue
		}
		info := m.matchInfo(match, s)
		info.RedPlayers, info.BluPlayers = red, blu
		info.DeadlineS = int(m.cfg.JoinDeadline.Seconds())
		p.push(Event{Type: EventMatchReady, Match: &info})
	}
	m.log.Info("battle ready", "match", match.ID, "server", match.ServerID,
		"connect", match.Connect, "map", match.Plan.Map)
}

func (m *Matchmaker) matchInfo(match *Match, s *Slot) MatchInfo {
	plan := match.Plan
	nodeName := m.war.NodeName(plan.TargetNode)
	info := MatchInfo{
		MatchID: match.ID, State: string(match.State), Server: match.ServerID,
		Connect: match.Connect, Password: match.Password,
		Map: plan.Map, Mode: plan.Mode,
		FrontID: plan.FrontID, FrontName: plan.FrontName,
		NodeID: plan.TargetNode, NodeName: nodeName,
		Attacker: plan.Attacker, Defender: plan.Defender,
		Stage: plan.Stage + 1, StageCount: plan.StageCount, StageKind: plan.StageKind,
		Headline: plan.Headline, Reason: briefingReason(plan, nodeName),
	}
	if s != nil {
		info.Side, info.Team, info.Contract = s.Side, s.Team, s.Contract
	}
	return info
}

// pushMatchState tells the roster where their battle is, without connect
// details — those only go out once the server is actually up.
func (m *Matchmaker) pushMatchState(match *Match, message string) {
	for _, s := range match.Slots {
		p := m.players[s.SteamID]
		if p == nil {
			continue
		}
		info := m.matchInfo(match, s)
		info.Connect, info.Password = "", ""
		p.push(Event{Type: EventMatchState, Match: &info, Message: message})
	}
}

// abort ends a battle without it touching the war and puts its players back in
// the queue, so a failed server costs a wait rather than a battle.
func (m *Matchmaker) abort(match *Match, reason string) {
	if match.over() {
		return
	}
	match.setState(MatchAborted, m.now())
	m.stats.Aborted++
	m.log.Warn("battle aborted", "match", match.ID, "server", match.ServerID, "reason", reason)

	if match.ServerID != "" {
		_ = m.pool.Send(match.ServerID, pool.Command{
			Type: pool.CommandAbort, MatchID: match.ID, Reason: reason,
		})
		m.pool.Release(match.ServerID, false)
	}
	for _, s := range match.Slots {
		p := m.players[s.SteamID]
		if p == nil {
			continue
		}
		p.MatchID = ""
		p.State = StateQueued
		p.QueuedAt = m.now()
		p.FrontID = match.FrontID
		status := m.queueStatusLocked(p)
		status.Message = reason + " — searching again"
		p.push(Event{Type: EventQueue, Queue: &status})
	}
}

// Leave takes a player out of whatever they are in. A player who leaves a live
// battle is not punished yet; the MVP has no reputation system to punish them
// with, and a wrong penalty is worse than none.
func (m *Matchmaker) Leave(p *Player) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.State == StateQueued {
		p.State = StateIdle
		p.FrontID = ""
		return
	}
	if p.MatchID == "" {
		return
	}
	match := m.matches[p.MatchID]
	p.State = StateIdle
	p.MatchID = ""
	if match == nil || match.over() {
		return
	}
	if s := match.slot(p.SteamID); s != nil {
		s.Connected = false
	}
}

// ---------------------------------------------------------------------------
// Tick
// ---------------------------------------------------------------------------

// Run drives the matchmaker until ctx is cancelled.
func (m *Matchmaker) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Tick()
		}
	}
}

// Tick is one scheduling pass: expire what is stale, form what can be formed,
// and keep the width of the war matched to how many people are online.
func (m *Matchmaker) Tick() {
	m.mu.Lock()
	now := m.now()

	// Servers that stopped answering take their battles with them.
	for _, matchID := range m.pool.ExpireSilent(now) {
		if match, ok := m.matches[matchID]; ok {
			m.abort(match, "the battle server stopped responding")
		}
	}

	for id, match := range m.matches {
		switch match.State {
		case MatchBooting:
			if now.Sub(match.StateSince) > m.cfg.BootDeadline {
				m.abort(match, "the battle server did not come up in time")
			}
		case MatchReady:
			if now.Sub(match.StateSince) > m.cfg.JoinDeadline {
				// Nobody told us it went live. The server is up, so let it run:
				// aborting here would throw away a battle that is probably fine.
				match.setState(MatchLive, now)
				m.stats.Live++
			}
		case MatchLive:
			if now.Sub(match.StateSince) > m.cfg.MatchTimeout {
				m.abort(match, "battle ran past its time limit without a result")
			}
		case MatchFinished, MatchAborted:
			if now.Sub(match.StateSince) > 5*time.Minute {
				delete(m.matches, id)
			}
		}
	}

	// Clients that stopped polling are gone; holding their queue slot would let
	// a closed game hold a battle open.
	for id, p := range m.players {
		if now.Sub(p.LastSeen) <= m.cfg.SessionTimeout {
			continue
		}
		if p.State == StateQueued {
			p.State = StateIdle
			p.FrontID = ""
		}
		if p.State == StateIdle {
			delete(m.players, id)
			delete(m.byToken, p.token)
		}
	}

	m.rehomeOrphanedQueue()
	m.tryForm()
	population := m.populationLocked()
	revision := m.war.Revision()
	busy := m.busyFrontsLocked()
	m.mu.Unlock()

	// Reconcile takes the war engine's lock and calls back into busy. Building
	// that set here, before releasing ours, keeps the two locks strictly
	// ordered: mm may wait on war, war never waits on mm.
	if err := m.war.Reconcile(population, func(id string) bool { return busy[id] }); err != nil {
		m.log.Error("war reconcile failed", "err", err)
	}
	if m.war.Revision() != revision {
		m.mu.Lock()
		m.broadcastWorldLocked(nil)
		m.mu.Unlock()
	}
}

// rehomeOrphanedQueue moves players whose front no longer exists.
//
// A front closes the moment somebody else's battle decides it — the node was
// captured, or the offensive collapsed — and anybody queued on it would
// otherwise wait for a battle that can never be formed. The war moved on; so do
// they. Called with the lock held.
func (m *Matchmaker) rehomeOrphanedQueue() {
	var orphans []*Player
	for _, p := range m.players {
		if p.State != StateQueued {
			continue
		}
		if _, ok := m.war.Front(p.FrontID); !ok {
			orphans = append(orphans, p)
		}
	}
	if len(orphans) == 0 {
		return
	}

	fronts := m.war.ActiveFronts()
	if len(fronts) == 0 {
		// Between campaigns, or every front resolved at once. Keep them queued:
		// Reconcile opens the next front within a tick or two, and dropping the
		// queue would make an armistice feel like a disconnection.
		for _, p := range orphans {
			p.push(Event{Type: EventQueue, Queue: &QueueStatus{
				Queued: true, Side: p.Side,
				Message: "the front has been decided — waiting for the next one to open",
			}})
		}
		return
	}
	for _, p := range orphans {
		p.FrontID = m.bestFrontFor(p, fronts)
		status := m.queueStatusLocked(p)
		status.Message = "that front has been decided — you are now deploying to " + status.FrontName
		p.push(Event{Type: EventQueue, Queue: &status})
		m.log.Info("queued player moved to a live front",
			"steam_id", p.SteamID, "front", p.FrontID)
	}
}

// busyFrontsLocked is the set of fronts with battles running or players
// waiting on them. A front in this set is never stood down.
func (m *Matchmaker) busyFrontsLocked() map[string]bool {
	busy := map[string]bool{}
	for _, match := range m.matches {
		if !match.over() {
			busy[match.FrontID] = true
		}
	}
	for _, p := range m.players {
		if p.State == StateQueued && p.FrontID != "" {
			busy[p.FrontID] = true
		}
	}
	return busy
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

// Population is how many people the war can see right now.
func (m *Matchmaker) Population() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.populationLocked()
}

func (m *Matchmaker) populationLocked() int {
	now := m.now()
	n := 0
	for _, p := range m.players {
		if now.Sub(p.LastSeen) <= m.cfg.SessionTimeout {
			n++
		}
	}
	return n
}

// LiveBattles is what the war map shows on top of the territory: the battles
// happening right now, and how many people are in them.
func (m *Matchmaker) LiveBattles() []LiveBattle {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]LiveBattle, 0, len(m.matches))
	for _, match := range m.matches {
		if match.over() {
			continue
		}
		out = append(out, LiveBattle{
			MatchID: match.ID, FrontID: match.FrontID, FrontName: match.Plan.FrontName,
			NodeID: match.Plan.TargetNode, Map: match.Plan.Map, Mode: match.Plan.Mode,
			StageKind: match.Plan.StageKind, Stage: match.Plan.Stage + 1,
			State: match.State, Players: len(match.Slots),
			RedScore: match.RedScore, BluScore: match.BluScore,
			StartedAt: match.CreatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MatchID < out[j].MatchID })
	return out
}

// QueuedOnFront counts who is waiting on each front, for the map screen.
func (m *Matchmaker) QueuedOnFront() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, p := range m.players {
		if p.State == StateQueued {
			out[p.FrontID]++
		}
	}
	return out
}

// Match returns one battle, for the admin endpoint.
func (m *Matchmaker) Match(id string) (*Match, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	match, ok := m.matches[id]
	return match, ok
}

// MatchInfoFor is a player's own view of their current battle, for a client
// that reconnected and lost its event stream.
func (m *Matchmaker) MatchInfoFor(p *Player) (MatchInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.MatchID == "" {
		return MatchInfo{}, false
	}
	match, ok := m.matches[p.MatchID]
	if !ok {
		return MatchInfo{}, false
	}
	info := m.matchInfo(match, match.slot(p.SteamID))
	if !match.live() {
		info.Connect, info.Password = "", ""
	}
	return info, true
}

// Stats returns the counters.
func (m *Matchmaker) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

// Players returns every connected player, for the admin endpoint.
func (m *Matchmaker) Players() []PlayerView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PlayerView, 0, len(m.players))
	for _, p := range m.players {
		out = append(out, p.Public())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SteamID < out[j].SteamID })
	return out
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (m *Matchmaker) queuedOn(frontID string) []*Player {
	var out []*Player
	for _, p := range m.players {
		if p.State == StateQueued && p.FrontID == frontID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].QueuedAt.Equal(out[j].QueuedAt) {
			return out[i].QueuedAt.Before(out[j].QueuedAt)
		}
		return out[i].SteamID < out[j].SteamID
	})
	return out
}

func (m *Matchmaker) liveOnFrontLocked(frontID string) int {
	n := 0
	for _, match := range m.matches {
		if match.FrontID == frontID && !match.over() {
			n++
		}
	}
	return n
}

func (m *Matchmaker) noticeQueued(frontID, message string) {
	for _, p := range m.players {
		if p.State == StateQueued && p.FrontID == frontID {
			status := m.queueStatusLocked(p)
			status.Message = message
			p.push(Event{Type: EventQueue, Queue: &status})
		}
	}
}

// broadcastWorldLocked tells every client the map moved. This is what makes a
// player who is standing on the war screen see somebody else's battle change
// the front.
func (m *Matchmaker) broadcastWorldLocked(up *war.Update) {
	rev := m.war.Revision()
	if rev == m.lastRevision && up == nil {
		return
	}
	m.lastRevision = rev
	notice := &WorldNotice{Revision: rev}
	if up != nil {
		notice.Headline = up.Headline
	}
	for _, p := range m.players {
		p.push(Event{Type: EventWorld, World: notice})
	}
}

func pickRegion(players []*Player) string {
	counts := map[string]int{}
	for _, p := range players {
		if p.Region != "" {
			counts[p.Region]++
		}
	}
	best, bestN := "", 0
	for region, n := range counts {
		if n > bestN || (n == bestN && region < best) {
			best, bestN = region, n
		}
	}
	// A region shared by fewer than half the players is not a preference worth
	// narrowing the pool for.
	if bestN*2 < len(players) {
		return ""
	}
	return best
}

func newID(prefix string) string { return prefix + "-" + randHex(6) }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
