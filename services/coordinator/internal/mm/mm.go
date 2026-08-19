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
	"math"
	"sort"
	"sync"
	"time"

	"github.com/greyline-frontress/coordinator/internal/hostelect"
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

	// WidenAfter is how long a queued player waits before bestFrontFor starts
	// favouring a front that already has people queued over one that merely
	// fits their preference. Zero disables widening.
	WidenAfter time.Duration
	// WidenStepBonus is how much score one WidenAfter-sized step of waiting
	// adds to a front that already has somebody queued on it.
	WidenStepBonus float64
	// WidenMaxSteps caps how far widening can push the score, so an
	// extraordinarily long wait cannot swamp every other factor.
	WidenMaxSteps int

	// HostElection and HostRequirements score a formed roster to pick a P2P
	// host when the dedicated pool has nothing free. See package hostelect.
	HostElection     hostelect.Weights
	HostRequirements hostelect.Requirements
	// HostAcceptDeadline is how long an elected host has to call
	// client/host-register before the coordinator gives up on the offer and
	// puts the roster back in the queue.
	HostAcceptDeadline time.Duration
	// HostFailureCooldown is how long a player whose hosting attempt failed —
	// an offer they never answered, or a server of theirs that never came up
	// — is kept out of elections. Without it the same unreachable client wins
	// the election again on the very next tick, and the front spends forever
	// forming and aborting the same battle while everybody else sits at
	// "already in a battle" and cannot even re-queue.
	HostFailureCooldown time.Duration
	// VerifiedIdentities is true when this coordinator proves SteamIDs against
	// Steam rather than trusting what a client claims. It travels with every
	// assignment, because a game server cannot otherwise know whether the
	// roster it was handed names accounts it will actually see.
	VerifiedIdentities bool
	// ResultQuorum is the fraction of non-host roster members who must
	// corroborate a P2P host's reported result before it counts towards the
	// war. A dedicated server's result is never held for a vote.
	ResultQuorum float64
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
		WidenAfter:         30 * time.Second,
		WidenStepBonus:     0.4,
		WidenMaxSteps:      6,
		HostElection: hostelect.Weights{
			Upload: 0.30, Latency: 0.35, CPU: 0.15, Stability: 0.20,
			DedicatedBonus: 0.50, PublicIPBonus: 0.10, AbandonPenalty: 0.35,
		},
		HostRequirements: hostelect.Requirements{
			MinUploadKbps: 1500, MinCPUScore: 40, MinMemoryMB: 2048,
			MaxAcceptableRTT: 180, RequireCanHost: true,
		},
		HostAcceptDeadline:  20 * time.Second,
		HostFailureCooldown: 2 * time.Minute,
		ResultQuorum:        0.5,
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

	// hostElector scores a formed roster to pick a P2P host. Nil (in a test
	// that builds a Config with a zeroed Elector-worthy Weights value is still
	// fine — Elect just always disqualifies on RequireCanHost) never crashes;
	// electHost guards it anyway for clarity.
	hostElector *hostelect.Elector
	// hostHistory is what the coordinator has learned about each player's
	// hosting record. In-memory only, which is right for the MVP: it exists to
	// make repeated elections in one session smarter, not to be a persistent
	// reputation system.
	hostHistory map[uint64]*hostelect.History
	// hostCooldown keeps a player who just failed to host out of the next few
	// elections. Same lifetime and same reasoning as hostHistory: in memory,
	// for this session, to stop one unreachable client from monopolising a
	// front.
	hostCooldown map[uint64]time.Time

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
		cfg:  cfg,
		log:  log,
		war:  engine,
		pool: servers,
		now:  time.Now,
		// No latency oracle yet: the coordinator does not collect per-client
		// RTT samples in this MVP, so every candidate scores an equal (zero)
		// latency term. Wiring hostelect.PopOracle up to client heartbeats is
		// future work, not a correctness gap — election still works on upload,
		// CPU and hosting history.
		hostElector:  hostelect.New(cfg.HostElection, cfg.HostRequirements, nil),
		hostHistory:  make(map[uint64]*hostelect.History),
		hostCooldown: make(map[uint64]time.Time),
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
// caps is what the client reports about its own ability to host a battle,
// only ever consulted when the dedicated pool has nothing free.
func (m *Matchmaker) Hello(steamID uint64, name string, side war.Side, region string, caps hostelect.Capabilities) *Player {
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
	// Allegiance is a between-battles decision, on this path as much as on
	// SetSide's. A client that reconnects mid-battle — a crash, a settings
	// save, a second hello — must not be able to change the side its own
	// roster slot was written against, or the battle is counted for a side
	// the player was not fighting for.
	inBattle := p.State == StateAssigned || p.State == StatePlaying
	if side != war.SideNone && !inBattle {
		p.Side = side
	} else if p.Side == war.SideNone {
		p.Side = m.lightestSide()
	}
	if region != "" {
		p.Region = region
	}
	p.HostCaps = caps
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
// DEPLOY button: the coordinator decides where they are needed. partyID, when
// non-empty, is shared by everyone who wants to land in the same battle
// together — it is opaque to the coordinator, which only ever compares it for
// equality against other queued players' own partyID.
func (m *Matchmaker) Deploy(p *Player, frontID string, acceptContract bool, partyID string) (QueueStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()

	if p.State == StateAssigned || p.State == StatePlaying {
		// A battle nobody has been sent to yet is not somewhere to be stuck.
		// Between forming and going ready a client can be holding a slot in a
		// battle that is waiting on a host who is not answering, and the only
		// thing it can do about it is press DEPLOY — which used to come back
		// "already in a battle" for as long as the front kept re-forming
		// around it. Pressing DEPLOY there means "get me out of this and find
		// me another one", so that is what it does.
		match := m.matches[p.MatchID]
		switch {
		case match == nil || match.over():
			p.MatchID = ""
		case match.live():
			return QueueStatus{}, fmt.Errorf("mm: you are already in a battle — leave it first")
		default:
			m.dropFromPendingMatchLocked(p, match)
		}
	}
	if p.Side == war.SideNone {
		p.Side = m.lightestSide()
	}
	p.PartyID = partyID

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

	// widenSteps grows with how long this player has already been waiting.
	// Quickplay-style server search scores every option once, locally, and
	// never revisits a bad early guess; this is the one place that is
	// deliberately not true here — the longer somebody has waited, the more a
	// front that already has people queued pulls ahead of one that merely
	// matches their preference, because getting them into a battle starts to
	// outweigh which battle it is. Only meaningful for a player who is already
	// queued (rehomeOrphanedQueue); a fresh DEPLOY has nothing to widen from
	// yet.
	widenSteps := 0.0
	if p.State == StateQueued && m.cfg.WidenAfter > 0 {
		if waited := m.now().Sub(p.QueuedAt); waited > 0 {
			widenSteps = float64(waited / m.cfg.WidenAfter)
			if m.cfg.WidenMaxSteps > 0 && widenSteps > float64(m.cfg.WidenMaxSteps) {
				widenSteps = float64(m.cfg.WidenMaxSteps)
			}
		}
	}

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
			s += widenSteps * m.cfg.WidenStepBonus
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
		// A battle that is already being played and has room takes people
		// before a new one is formed. Two 1v1s on the same front while four
		// people are online is the wrong answer; so is making somebody who
		// deployed thirty seconds late wait out a battle they could be in.
		m.topUpLocked(f)
		for m.formOne(f) {
		}
	}
}

// topUpLocked puts queued players into battles this front already has, whether
// those battles are being played or still standing up.
//
// A formed roster is not a sealed one, and the moment a battle was created is
// not a reason to make the next person wait for a whole second one. The MVP's
// population is small enough that the difference between "you are in the fight
// that is happening" and "you are the first half of a fight that will form in
// four minutes" decides whether an evening of testing works at all — and a
// battlefield that holds twelve has no reason to run 2v2 while four more
// people wait beside it.
//
// Standing-up battles matter as much as running ones here: a map takes half a
// minute to load, and anybody who deployed during that half minute used to sit
// in the queue watching a battle they could have been in, until there were
// enough of them to start a second one next to it.
//
// Latecomers only ever fill towards balance, and only take the other side
// under a contract they already agreed to, so growing a battle can never make
// it more lopsided than it already was. Called with the lock held.
func (m *Matchmaker) topUpLocked(f war.Front) {
	for _, match := range m.matches {
		if match.FrontID != f.ID || match.over() {
			continue
		}
		// Sending somebody to a running battle with no address is sending them
		// nowhere, silently — the one failure this whole design exists to not
		// reproduce. It cannot normally happen past ready; refuse it anyway.
		// A battle that has not got there yet has no address by definition,
		// and its roster is what it will boot with.
		if match.live() && match.Connect == "" {
			continue
		}
		room := m.capacityOf(match) - len(match.Slots)
		if room <= 0 {
			continue
		}
		var added []pool.RosterEntry
		var seated []seat
		for _, p := range m.queuedOn(f.ID) {
			if room == 0 {
				break
			}
			slot, ok := m.seatLocked(match, p)
			if !ok {
				continue
			}
			room--
			seated = append(seated, seat{player: p, slot: slot})
			added = append(added, pool.RosterEntry{
				SteamID: slot.SteamID, Name: slot.Name,
				Side: slot.Side.String(), Team: slot.Team.String(),
				Contract: slot.Contract,
			})
		}
		if len(added) == 0 {
			continue
		}
		// The server is told before the players are, and that order matters:
		// it decides their team from the roster and refuses anybody not on it,
		// so a player who set off first could arrive at a server that has
		// never heard of them. A battle that has not been handed to a server
		// yet needs no telling — the assignment is built from the roster at
		// the moment it goes out, so it will carry them already.
		if match.ServerID != "" {
			if err := m.pool.Send(match.ServerID, pool.Command{
				Type: pool.CommandRoster, MatchID: match.ID, Roster: added,
			}); err != nil {
				m.log.Warn("could not send a roster update to a battle in progress",
					"match", match.ID, "server", match.ServerID, "err", err)
			}
		}
		for _, s := range seated {
			m.notifySeatedLocked(match, s.player, s.slot)
		}
		m.log.Info("players joined an existing battle", "match", match.ID,
			"front", f.ID, "state", match.State, "joined", len(added), "roster", len(match.Slots))
	}
}

// seatLocked puts one queued player into a battle that already exists, or
// reports that there is no place for them in it. Called with the lock held.
func (m *Matchmaker) seatLocked(match *Match, p *Player) (*Slot, bool) {
	if match.slot(p.SteamID) != nil {
		return nil, false
	}
	red, blu := match.sideCounts()

	// Fill the thinner side. A player only crosses to the other one under a
	// contract, which is a thing they opted into — never a surprise.
	side := p.Side
	switch {
	case red < blu:
		side = war.SideRed
	case blu < red:
		side = war.SideBlu
	}
	contract := side != p.Side
	if contract && !p.AcceptContract {
		return nil, false
	}

	slot := &Slot{
		SteamID: p.SteamID, Name: p.Name,
		Side: side, Team: match.Plan.GameTeam(side), Contract: contract,
	}
	match.Slots = append(match.Slots, slot)

	p.State = StateAssigned
	p.MatchID = match.ID
	p.FrontID = match.FrontID
	if contract {
		p.Contracts++
		m.stats.Contracts++
	}

	return slot, true
}

// seat is one player and the place they were just given, held between seating
// them and telling them — see topUpLocked for why those are not the same
// moment.
type seat struct {
	player *Player
	slot   *Slot
}

// notifySeatedLocked tells a player they are in a battle, and where it is if
// there is anywhere to go yet. Called with the lock held.
func (m *Matchmaker) notifySeatedLocked(match *Match, p *Player, slot *Slot) {
	red, blu := match.sideCounts()
	info := m.matchInfo(match, slot)
	info.RedPlayers, info.BluPlayers = red, blu
	info.DeadlineS = int(m.cfg.JoinDeadline.Seconds())
	if match.live() {
		p.push(Event{Type: EventMatchReady, Match: &info})
		return
	}
	// Nowhere to send them yet. They get the connect details with everybody
	// else's when the server reports ready — sendToBattle walks the roster
	// this player is now on.
	info.Connect, info.Password = "", ""
	p.push(Event{Type: EventMatchState, Match: &info,
		Message: "you are in the next battle at " + match.Plan.FrontName + " — it is starting up"})
}

func (m *Matchmaker) formOne(f war.Front) bool {
	if m.liveOnFrontLocked(f.ID) >= m.cfg.MaxBattlesPerFront {
		return false
	}
	queued := withParties(m.queuedOn(f.ID))
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

	match := &Match{
		ID:         matchID,
		FrontID:    f.ID,
		Plan:       plan,
		Slots:      slots,
		Password:   pool.NewPassword(),
		State:      MatchForming,
		CreatedAt:  m.now(),
		StateSince: m.now(),
	}

	server, err := m.pool.Reserve(matchID, pickRegion(picked), m.now())
	if err != nil {
		// Nothing dedicated is free. Before giving up, see if anybody just
		// picked is willing and able to run the battle on their own machine —
		// this is the fallback money-constrained testing needs, not the
		// default: Reserve already prefers a dedicated server whenever one is
		// idle, including a previously-elected P2P host sitting idle from an
		// earlier battle.
		host, ok := m.electHostLocked(picked)
		if !ok {
			m.stats.NoServer++
			// Nobody is dropped from the queue: the battle is formed the
			// moment a machine frees up, or somebody is elected to host it
			// themselves. Telling players why they are waiting is better than
			// a silent queue.
			m.noticeQueued(f.ID, "waiting for a free battle server")
			return false
		}
		match.HostSteamID = host
		match.setState(MatchAwaitingHost, m.now())
		m.matches[match.ID] = match
		m.claimSlotsLocked(match)
		m.stats.Formed++

		if hp := m.players[host]; hp != nil {
			hp.push(Event{Type: EventHostOffer, Host: &HostOffer{
				MatchID: match.ID, Map: plan.Map, Mode: plan.Mode,
				MaxPlayers: len(slots), FrontName: plan.FrontName,
				AcceptDeadlineS: int(m.cfg.HostAcceptDeadline.Seconds()),
			}})
		}
		m.log.Info("host offered", "match", match.ID, "front", f.ID,
			"steam_id", host, "map", plan.Map)
		m.pushMatchState(match, "a machine in the roster was asked to host "+plan.Map)
		return true
	}

	match.ServerID = server.ID
	match.Connect = server.ConnectAddress
	m.matches[match.ID] = match
	m.claimSlotsLocked(match)
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

// dropFromPendingMatchLocked takes a player out of a battle that has not sent
// anybody anywhere yet. If they were the one elected to host it, the battle
// has lost its server before it ever had one, so it is aborted and the rest of
// the roster goes back to the queue — which is exactly what would have
// happened when the offer timed out, only now.
func (m *Matchmaker) dropFromPendingMatchLocked(p *Player, match *Match) {
	p.MatchID = ""
	if match.HostSteamID == p.SteamID {
		m.abort(match, "the elected host withdrew before the battle started")
		return
	}
	slots := match.Slots[:0]
	for _, s := range match.Slots {
		if s.SteamID != p.SteamID {
			slots = append(slots, s)
		}
	}
	match.Slots = slots
	// A roster that has dropped below a battle's floor is not a battle. Let it
	// go now rather than let it sit until its own deadline runs out.
	if len(match.Slots) < 2*m.cfg.MinTeamSize {
		m.abort(match, "the roster fell apart before the battle started")
	}
}

// claimSlotsLocked marks every player in a formed match as assigned and out
// of the queue. Called with the lock held, for both the dedicated-server and
// the elected-host paths through formOne.
func (m *Matchmaker) claimSlotsLocked(match *Match) {
	for _, s := range match.Slots {
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
}

// electHostLocked scores a just-formed roster and returns a SteamID willing
// and able to host the battle, if anyone qualifies. Called with the lock
// held: every input is already in memory, so this never blocks.
func (m *Matchmaker) electHostLocked(picked []*Player) (uint64, bool) {
	if m.hostElector == nil || len(picked) == 0 {
		return 0, false
	}
	now := m.now()
	candidates := make([]hostelect.Candidate, 0, len(picked))
	for _, p := range picked {
		c := hostelect.Candidate{
			SteamID: p.SteamID,
			Caps:    p.HostCaps,
			Hist:    m.hostHistoryLocked(p.SteamID),
			// Everyone in picked is a currently-queued, currently-polling
			// client — that is exactly what queuedOn already filtered for.
			Online: true,
		}
		if until, ok := m.hostCooldown[p.SteamID]; ok && now.Before(until) {
			c.ExcludeReason = "a recent hosting attempt by this machine failed"
		}
		candidates = append(candidates, c)
	}
	best, _, ok := m.hostElector.Elect(candidates)
	if !ok {
		return 0, false
	}
	return best.SteamID, true
}

func (m *Matchmaker) hostHistoryLocked(steamID uint64) hostelect.History {
	if h, ok := m.hostHistory[steamID]; ok {
		return *h
	}
	return hostelect.History{}
}

// recordHostOutcomeLocked is the only place a player's hosting record
// changes. It is in-memory only, which is right for the MVP: it exists to
// make repeated elections within one session smarter, not to be a persistent
// reputation system.
func (m *Matchmaker) recordHostOutcomeLocked(steamID uint64, hostedOK, hostedFailed, abandoned bool) {
	if steamID == 0 {
		return
	}
	h, exists := m.hostHistory[steamID]
	if !exists {
		h = &hostelect.History{}
		m.hostHistory[steamID] = h
	}
	if hostedOK {
		h.HostedOK++
	}
	if hostedFailed {
		h.HostedFailed++
	}
	if abandoned {
		h.Abandons++
	}
}

// ConfirmHost is called when a client accepts a host_offer: it registers the
// client's own machine into the server pool for exactly this battle and hands
// it the assignment, the same way a dedicated agent would receive it.
func (m *Matchmaker) ConfirmHost(p *Player, matchID string, reg pool.Registration) (serverID, serverToken string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	match, ok := m.matches[matchID]
	if !ok {
		return "", "", fmt.Errorf("mm: unknown battle %q", matchID)
	}
	if match.State != MatchAwaitingHost || match.HostSteamID != p.SteamID {
		return "", "", fmt.Errorf("mm: you were not offered host for battle %q", matchID)
	}

	id, token, err := m.pool.RegisterElectedHost(reg, matchID, m.now())
	if err != nil {
		return "", "", err
	}
	match.ServerID = id
	if err := m.pool.Send(id, pool.Command{
		Type:       pool.CommandAssign,
		MatchID:    match.ID,
		Assignment: m.assignmentFor(match),
	}); err != nil {
		m.abort(match, "the elected host's registration could not be handed the battle")
		return "", "", fmt.Errorf("mm: could not hand the battle to your machine: %w", err)
	}
	match.setState(MatchBooting, m.now())
	m.log.Info("elected host confirmed", "match", match.ID, "steam_id", p.SteamID, "server", id)
	return id, token, nil
}

// assignSlots splits the picked players into two sides. Allegiance is honoured
// where it can be; the rest are given contracts for the other side, which is
// what a war of mercenaries can do and a war of armies cannot. A party — a
// non-empty PartyID shared by two or more picked players — is kept on the same
// side whenever there is room, ahead of everything except a player's own
// already-placed partymates: landing together is the entire reason to queue as
// a party, and quickplay-style matchmaking has no way to express that at all.
func assignSlots(picked []*Player, teamSize int, plan war.BattlePlan) []*Slot {
	slots := make([]*Slot, 0, len(picked))
	counts := map[war.Side]int{}
	partySide := map[string]war.Side{}
	var leftovers []*Player

	place := func(p *Player, side war.Side) {
		counts[side]++
		if p.PartyID != "" {
			partySide[p.PartyID] = side
		}
		slots = append(slots, &Slot{
			SteamID:  p.SteamID,
			Name:     p.Name,
			Side:     side,
			Team:     plan.GameTeam(side),
			Contract: side != p.Side,
		})
	}

	for _, p := range picked {
		if p.PartyID != "" {
			if side, ok := partySide[p.PartyID]; ok && counts[side] < teamSize {
				place(p, side)
				continue
			}
		}
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
		if p.PartyID != "" {
			if side, ok := partySide[p.PartyID]; ok && counts[side] < teamSize {
				place(p, side)
				continue
			}
		}
		if counts[war.SideRed] <= counts[war.SideBlu] && counts[war.SideRed] < teamSize {
			place(p, war.SideRed)
		} else {
			place(p, war.SideBlu)
		}
	}
	return slots
}

// withParties reorders a queue so a party lands together in one clump instead
// of being split apart by plain arrival order. A party's clump takes the
// position of its earliest member's arrival, so a party never skips ahead of
// players who queued before any of them did — it only stops being split up.
func withParties(queued []*Player) []*Player {
	earliest := map[string]time.Time{}
	for _, p := range queued {
		if p.PartyID == "" {
			continue
		}
		if t, ok := earliest[p.PartyID]; !ok || p.QueuedAt.Before(t) {
			earliest[p.PartyID] = p.QueuedAt
		}
	}
	if len(earliest) == 0 {
		return queued // nobody partied up; the FIFO order already stands.
	}
	out := make([]*Player, len(queued))
	copy(out, queued)
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := out[i].QueuedAt, out[j].QueuedAt
		if out[i].PartyID != "" {
			ti = earliest[out[i].PartyID]
		}
		if out[j].PartyID != "" {
			tj = earliest[out[j].PartyID]
		}
		return ti.Before(tj)
	})
	return out
}

// capacityOf is how many players a battle can hold: what the battlefield
// itself allows, never less than the roster already on it.
func (m *Matchmaker) capacityOf(match *Match) int {
	capacity := match.Plan.MaxPlayers
	if capacity <= 0 {
		capacity = 2 * m.cfg.TeamSizes[len(m.cfg.TeamSizes)-1]
	}
	if capacity < len(match.Slots) {
		capacity = len(match.Slots)
	}
	return capacity
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
		MatchID:  match.ID,
		Map:      plan.Map,
		Mode:     plan.Mode,
		Password: match.Password,
		// The battlefield's capacity, not the size of the roster that opened
		// it: a server told to hold exactly the four people who formed the
		// battle has no room for the fifth who deploys a minute later, and
		// keeping that room is the whole point of letting a battle grow.
		MaxPlayers: m.capacityOf(match),
		// Wait for the roster that opened the battle, not for the battlefield
		// to fill: capacity is room to grow into, MinPlayers is who this
		// battle was formed out of and is therefore actually coming.
		VerifiedIdentities: m.cfg.VerifiedIdentities,
		MinPlayers:         len(match.Slots),
		MusterTimeoutS:     int(m.cfg.JoinDeadline.Seconds()),
		Roster:             roster,
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
// reason is what the server said about a "failed" state and is ignored for
// every other one. It is the only account anybody gets of why a battle a
// player was standing on ended, so it is carried through rather than replaced
// with a sentence the coordinator made up.
func (m *Matchmaker) ServerState(serverID, matchID, state string, players int, reason string) error {
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
			// Re-read the address from the pool rather than trusting whatever
			// formOne captured at reservation time: a dedicated server always
			// had one, but a P2P host only learns its Steam FakeIP after its
			// own engine allocates it, well after the assignment was sent —
			// see servers/address. Sending a match_ready with nothing to
			// connect to is exactly the silent "connection never happens" bug
			// this design exists to not reproduce, so it is refused outright
			// rather than sent empty.
			if srv, ok := m.pool.Get(serverID); ok && srv.ConnectAddress != "" {
				match.Connect = srv.ConnectAddress
			}
			if match.Connect == "" {
				return fmt.Errorf("mm: battle %q has no connect address to send anyone to yet", matchID)
			}
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
		why := "the battle server could not host this battle"
		if reason != "" {
			why = "the battle server gave up: " + reason
		}
		m.abort(match, why)
	default:
		return fmt.Errorf("mm: unknown server state %q", state)
	}
	return nil
}

// ServerResult records a finished battle: the one place a game result becomes
// a war event — unless the server reporting it is untrusted, in which case it
// is held for the roster to corroborate. See ConfirmResult.
//
// Dedicated servers are the coordinator's own infrastructure and their word is
// taken as it stands. A P2P host is an ordinary player: nothing stops an
// elected host from reporting a win for themselves, so their report only
// counts once enough of the rest of the roster says the same thing.
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

	// Keep the literal scoreboard report as well as the translated war result.
	// Non-host players corroborate what they actually saw in the game; the
	// confirmation endpoint performs this same translation before comparing it
	// with PendingResult.
	rawOutcome, rawRedScore, rawBluScore := res.Outcome, res.RedScore, res.BluScore

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

	srv, _ := m.pool.Get(serverID)
	if srv == nil || !srv.Trusted {
		match.PendingResult = &res
		notice := &ResultPendingNotice{
			MatchID: match.ID, Outcome: rawOutcome,
			RedScore: rawRedScore, BluScore: rawBluScore,
		}
		for _, slot := range match.Slots {
			if slot.SteamID == match.HostSteamID {
				continue // the host is never allowed to corroborate itself
			}
			if p := m.players[slot.SteamID]; p != nil {
				p.push(Event{Type: EventResultPending, Pending: notice})
			}
		}
		m.log.Info("battle result reported by an untrusted host; waiting for the roster to corroborate it",
			"match", match.ID, "host_steam_id", match.HostSteamID, "outcome", res.Outcome)
		return nil, nil
	}
	return m.finalizeResultLocked(match, res), nil
}

// ConfirmResult is a roster member corroborating a P2P host's reported
// result. Once enough of the non-host roster agrees — Config.ResultQuorum,
// rounded up, of however many of them there are — the result is recorded the
// same way a trusted dedicated server's report is. A vote that disagrees with
// the pending result is logged and otherwise ignored: the MVP has no dispute
// process yet, only visibility for an operator to notice a pattern.
func (m *Matchmaker) ConfirmResult(p *Player, matchID string, outcome war.Outcome, redScore, bluScore uint32) (*war.Update, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	match, ok := m.matches[matchID]
	if !ok {
		return nil, fmt.Errorf("mm: unknown battle %q", matchID)
	}
	if match.over() {
		return nil, nil
	}
	if match.PendingResult == nil {
		return nil, fmt.Errorf("mm: battle %q has no result to corroborate yet", matchID)
	}
	if match.HostSteamID != 0 && p.SteamID == match.HostSteamID {
		return nil, fmt.Errorf("mm: the host's own report needs corroboration, not another vote from the host")
	}
	if match.slot(p.SteamID) == nil {
		return nil, fmt.Errorf("mm: you were not on this battle's roster")
	}

	outcome, redScore, bluScore = match.Plan.TranslateResult(outcome, redScore, bluScore)
	if outcome != match.PendingResult.Outcome || redScore != match.PendingResult.RedScore || bluScore != match.PendingResult.BluScore {
		m.log.Warn("a roster member's result confirmation disagrees with the host's report",
			"match", match.ID, "steam_id", p.SteamID)
		return nil, nil
	}

	if match.confirms == nil {
		match.confirms = map[uint64]bool{}
	}
	match.confirms[p.SteamID] = true

	nonHost := 0
	for _, s := range match.Slots {
		if s.SteamID != match.HostSteamID {
			nonHost++
		}
	}
	needed := int(math.Ceil(m.cfg.ResultQuorum * float64(nonHost)))
	if needed < 1 {
		needed = 1
	}
	if needed > nonHost {
		needed = nonHost
	}
	if len(match.confirms) < needed {
		return nil, nil
	}
	return m.finalizeResultLocked(match, *match.PendingResult), nil
}

// finalizeResultLocked is the one place a game result actually becomes a war
// event, reached either straight from a trusted server's report or once a P2P
// host's report has been corroborated. Called with the lock held.
func (m *Matchmaker) finalizeResultLocked(match *Match, res war.BattleResult) *war.Update {
	match.Result = &res
	match.PendingResult = nil
	match.RedScore, match.BluScore = res.RedScore, res.BluScore
	match.setState(MatchFinished, m.now())
	m.stats.Finished++
	if match.HostSteamID != 0 {
		m.recordHostOutcomeLocked(match.HostSteamID, true, false, false)
	}

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
	if match.ServerID != "" {
		m.pool.Release(match.ServerID, true)
	}
	m.broadcastWorldLocked(update)
	return update
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
	info.IsP2P = match.HostSteamID != 0
	if s != nil {
		info.Side, info.Team, info.Contract = s.Side, s.Team, s.Contract
		info.IsHost = match.HostSteamID != 0 && match.HostSteamID == s.SteamID
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
	// A P2P host's hosting record is the one input to future elections a
	// player cannot forge — see hostelect. Booting is "never got up",
	// anything past that is "went away mid-battle": both matter, but
	// differently, so hostelect scores them differently too.
	//
	// A failed host also goes on cooldown. History alone is not enough to
	// break the loop: it is one term among several, so a client that is the
	// only capable machine on the front still wins the very next election, and
	// the front does nothing but form and abort the same battle from then on.
	hostFailed := false
	if match.HostSteamID != 0 {
		switch match.State {
		case MatchReady, MatchLive:
			m.recordHostOutcomeLocked(match.HostSteamID, false, false, true)
			hostFailed = true
		case MatchAwaitingHost, MatchBooting:
			m.recordHostOutcomeLocked(match.HostSteamID, false, true, false)
			hostFailed = true
		}
		if hostFailed && m.cfg.HostFailureCooldown > 0 {
			m.hostCooldown[match.HostSteamID] = m.now().Add(m.cfg.HostFailureCooldown)
		}
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

		// The host of a battle that never got up does not go back in the
		// queue. Their client just demonstrated that it is not answering —
		// still loading, minimised, gone — and re-queueing it only feeds it
		// straight back into the next formation on this front, where it holds
		// a slot the people who *are* answering then wait behind. Idle is the
		// honest state: pressing DEPLOY again is one click, and by then their
		// game is actually there to press it.
		if hostFailed && s.SteamID == match.HostSteamID {
			p.State = StateIdle
			p.FrontID = ""
			status := QueueStatus{Queued: false, Side: p.Side,
				Message: "your machine did not get the battle up — DEPLOY again when the game is ready"}
			p.push(Event{Type: EventQueue, Queue: &status})
			continue
		}

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
// reason is what the client said it was leaving for, and may be empty. It is
// logged and nothing else: a player who could not reach a battle and one who
// walked out of it look identical from here otherwise, and the first of those
// is a fault worth seeing in the log.
func (m *Matchmaker) Leave(p *Player, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if reason != "" {
		m.log.Info("player left", "steam_id", p.SteamID, "state", p.State,
			"match", p.MatchID, "reason", reason)
	}
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
		case MatchAwaitingHost:
			if now.Sub(match.StateSince) > m.cfg.HostAcceptDeadline {
				m.abort(match, "the elected host did not confirm in time")
			}
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
		// Players is who is actually on the server, as the server itself
		// last reported — not the size of the roster. They are not the same
		// number and the difference is the whole story on the war map: a
		// battle showing "2 players" that only one person has managed to
		// reach is a battle somebody is waiting alone in, and saying so is
		// what lets them tell that apart from a battle in progress.
		players := 0
		if match.ServerID != "" {
			if srv, ok := m.pool.Get(match.ServerID); ok {
				players = srv.Players
			}
		}
		out = append(out, LiveBattle{
			MatchID: match.ID, FrontID: match.FrontID, FrontName: match.Plan.FrontName,
			NodeID: match.Plan.TargetNode, Map: match.Plan.Map, Mode: match.Plan.Mode,
			StageKind: match.Plan.StageKind, Stage: match.Plan.Stage + 1,
			State: match.State, Players: players,
			RosterSize: len(match.Slots), Capacity: m.capacityOf(match),
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
