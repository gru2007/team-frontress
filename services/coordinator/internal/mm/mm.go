// Package mm is the matchmaker: it holds the queue, forms matches, gets a
// server for each one and hands the connect details back to the clients.
//
// The design constraint that shapes everything here is a small population. The
// matchmaker forms the largest match it can, waits a configurable while for a
// better one, and then settles rather than leaving six people in a queue that
// wants twenty-four.
package mm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/war"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// ticketState is the matchmaker's own view, which is finer than the client's.
type ticketState string

const (
	tsSearching ticketState = "searching" // in queue
	tsMatched   ticketState = "matched"   // in a match that is still booting
	tsAssigned  ticketState = "assigned"  // has connect details
	tsCancelled ticketState = "cancelled"
	tsExpired   ticketState = "expired"
	tsFailed    ticketState = "failed"
)

// Ticket is one party's place in one queue.
type Ticket struct {
	ID         string
	MatchGroup wire.MatchGroup
	Leader     wire.SteamID
	Players    []wire.AssignedPlayer
	Maps       []string
	LateJoinOK bool
	Region     string

	state      ticketState
	queuedAt   time.Time
	lastPoll   time.Time
	matchID    string
	assignment *wire.Assignment
	failure    string
}

// Size is how many seats this ticket needs.
func (t *Ticket) Size() int { return len(t.Players) }

// matchState is where a formed match is in its life.
type matchState string

const (
	// msWaitingServer keeps a formed match intact while all providers are
	// temporarily empty. Re-forming the same match every tick both spams
	// logs and churns maps/IDs for no player-visible benefit.
	msWaitingServer matchState = "waiting_server"
	msBooting       matchState = "booting"
	msLive          matchState = "live"
	msOver          matchState = "over"
)

// Match is a formed match and the server running it.
type Match struct {
	ID         string
	MatchGroup wire.MatchGroup
	Map        string
	Server     *pool.Server
	Players    []wire.AssignedPlayer
	Password   string
	War        *wire.WarBriefing
	FrontID    string

	state        matchState
	tickets      []string
	createdAt    time.Time
	startedAt    time.Time
	lastNonEmpty time.Time
	lastPolled   time.Time
	players      int
}

// Spec is everything a game server needs to run a match. It is what ServerSetup
// receives, so the RCON details stay in one place.
type Spec struct {
	MatchID      string
	Map          string
	Password     string
	ServerConfig string
	Players      int
	MaxPlayers   int
}

// ServerSetup prepares a server for a match and watches it afterwards. The
// real implementation drives RCON; tests supply their own.
type ServerSetup interface {
	// Setup makes the server ready and returns when the map is changing.
	Setup(ctx context.Context, s *pool.Server, spec Spec) error
	// PlayerCount returns how many humans are on the server. ok is false when
	// the server could not be asked, which is not the same as "empty".
	PlayerCount(ctx context.Context, s *pool.Server) (n int, ok bool)
	// Teardown returns the server to a resting state after a match. Best
	// effort: an ephemeral server is about to be destroyed anyway.
	Teardown(ctx context.Context, s *pool.Server) error
}

// Matchmaker owns the queue and every formed match.
type Matchmaker struct {
	cfg   config.Config
	pool  *pool.Pool
	setup ServerSetup
	war   *war.Engine
	log   *slog.Logger
	now   func() time.Time
	newID func() string

	mu      sync.Mutex
	players PlayerStore
	tickets map[string]*Ticket
	matches map[string]*Match
	// recent remembers when each map was last played, so the picker can
	// prefer one the players have not just finished.
	recent map[string]time.Time
	// byLeader lets a client re-queue without stacking tickets: one party, one
	// ticket per match group.
	byLeader map[leaderKey]string
}

type leaderKey struct {
	leader wire.SteamID
	group  wire.MatchGroup
}

// New builds a matchmaker. warEngine may be nil, in which case match groups
// choose their own maps.
func New(cfg config.Config, p *pool.Pool, setup ServerSetup, warEngine *war.Engine, log *slog.Logger) *Matchmaker {
	if log == nil {
		log = slog.Default()
	}
	return &Matchmaker{
		cfg:      cfg,
		pool:     p,
		setup:    setup,
		war:      warEngine,
		log:      log,
		now:      time.Now,
		newID:    randomID,
		tickets:  map[string]*Ticket{},
		matches:  map[string]*Match{},
		byLeader: map[leaderKey]string{},
	}
}

// Enqueue puts a party in queue, replacing that party's previous ticket for
// the same match group.
func (m *Matchmaker) Enqueue(t *Ticket) (*Ticket, error) {
	group, ok := m.cfg.Group(t.MatchGroup)
	m.log.Info("enqueue request",
		"group", t.MatchGroup,
		"leader", t.Leader,
		"players", t.Size(),
	)

	if !ok {
		m.log.Warn("queue refused: group is not configured",
			"group", t.MatchGroup,
		)
		return nil, fmt.Errorf(
			"match group %d is not available here",
			t.MatchGroup,
		)
	}

	if !group.Enabled {
		m.log.Warn("queue refused: group is disabled",
			"group", t.MatchGroup,
			"name", group.Name,
		)
		return nil, fmt.Errorf(
			"match group %d is not available here",
			t.MatchGroup,
		)
	}
	if !ok || !group.Enabled {
		return nil, fmt.Errorf("match group %d is not available here", t.MatchGroup)
	}
	if t.Size() == 0 {
		return nil, fmt.Errorf("a queue ticket needs at least one player")
	}

	// A party bigger than half a match cannot be placed without splitting it,
	// and splitting a party is the one thing matchmaking must not do. That
	// check lives with the rest of the group's entry rules.
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := checkRestrictions(group, t, m.players, m.cfg.Auth.Verified(), m.now()); err != nil {
		m.log.Info("queue refused", "group", group.Name, "leader", t.Leader, "reason", err)
		return nil, err
	}

	key := leaderKey{t.Leader, t.MatchGroup}
	if oldID, ok := m.byLeader[key]; ok {
		if old, ok := m.tickets[oldID]; ok {
			switch old.state {
			case tsMatched, tsAssigned:
				// Already in a match. Hand back what they have rather than
				// queueing them for a second one.
				return old, nil
			}
			old.state = tsCancelled
		}
	}

	t.ID = m.newID()
	t.state = tsSearching
	t.queuedAt = m.now()
	t.lastPoll = t.queuedAt
	m.tickets[t.ID] = t
	m.byLeader[key] = t.ID
	m.log.Info("queued", "ticket", t.ID, "group", group.Name, "players", t.Size(), "leader", t.Leader)
	return t, nil
}

// Cancel takes a ticket out of queue. A ticket already in a match is not
// cancellable: the match exists and abandoning it is the game's business.
func (m *Matchmaker) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok {
		return fmt.Errorf("no ticket %q", id)
	}
	if t.state == tsMatched || t.state == tsAssigned {
		return fmt.Errorf("ticket %s is already in a match", id)
	}
	t.state = tsCancelled
	return nil
}

// Status returns the client-facing view of a ticket and marks it as polled.
func (m *Matchmaker) Status(id string) (wire.QueueStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok {
		return wire.QueueStatus{}, fmt.Errorf("no ticket %q", id)
	}
	now := m.now()
	t.lastPoll = now

	st := wire.QueueStatus{
		TicketID:    t.ID,
		QueuedSecs:  int(now.Sub(t.queuedAt).Seconds()),
		PollAfterMS: m.cfg.Timing.PollAfterMS,
		Assignment:  t.assignment,
		Error:       t.failure,
	}
	switch t.state {
	case tsAssigned:
		st.State = wire.QueueStateAssigned
	case tsCancelled:
		st.State = wire.QueueStateCancelled
	case tsExpired:
		st.State = wire.QueueStateExpired
	case tsFailed:
		st.State = wire.QueueStateFailed
	default:
		st.State = wire.QueueStateSearching
	}

	group, _ := m.cfg.Group(t.MatchGroup)
	queued := m.queuedPlayersLocked(t.MatchGroup)
	st.InQueue = queued
	if need := group.MinPlayers - queued; need > 0 {
		st.NeedPlayers = need
	}
	return st, nil
}

func (m *Matchmaker) queuedPlayersLocked(g wire.MatchGroup) int {
	n := 0
	for _, t := range m.tickets {
		if t.MatchGroup == g && t.state == tsSearching {
			n += t.Size()
		}
	}
	return n
}

// QueuedPlayers reports the queue depth per match group.
func (m *Matchmaker) QueuedPlayers() map[wire.MatchGroup]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[wire.MatchGroup]int{}
	for _, t := range m.tickets {
		if t.state == tsSearching {
			out[t.MatchGroup] += t.Size()
		}
	}
	return out
}

// Population is everyone the coordinator can currently see: queued and in a
// live match. The war's width is decided from it.
func (m *Matchmaker) Population() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.tickets {
		if t.state == tsSearching {
			n += t.Size()
		}
	}
	for _, mt := range m.matches {
		if mt.state != msOver {
			n += len(mt.Players)
		}
	}
	return n
}

// OpenMatches counts, per match group, the live matches that would take
// another player right now.
func (m *Matchmaker) OpenMatches() map[wire.MatchGroup]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[wire.MatchGroup]int{}
	for _, g := range m.cfg.MatchGroups {
		if !g.Enabled || !g.BBackfills() {
			continue
		}
		out[g.MatchGroup] = len(m.openMatchesLocked(g))
	}
	return out
}

// FreeServers is how many servers could host a match right now. Providers that
// cannot answer without a network call report zero, so it is a floor.
func (m *Matchmaker) FreeServers() int { return m.pool.Free() }

// LiveMatches counts matches that are booting or running.
func (m *Matchmaker) LiveMatches() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, mt := range m.matches {
		if mt.state != msOver {
			n++
		}
	}
	return n
}

// Run drives the matchmaker until ctx is done.
func (m *Matchmaker) Run(ctx context.Context) {
	tick := time.NewTicker(m.cfg.Timing.Tick())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.Tick(ctx)
		}
	}
}

// Tick is one pass: expire stale tickets, form what can be formed, and check
// on the matches already running. It is exported so tests can step time.
func (m *Matchmaker) Tick(ctx context.Context) {
	m.expire()
	m.reconcileWar()
	for _, g := range m.cfg.MatchGroups {
		if !g.Enabled {
			continue
		}

		// Fill the games that are already running before starting another
		// one. With a small population that is the difference between one
		// full server and three quarter-empty ones.
		m.backfill(ctx, g)

		for {
			mt := m.form(g)
			if mt == nil {
				break
			}
			go m.boot(ctx, mt)
		}
	}
	m.superviseMatches(ctx)
}

// expire drops tickets whose client stopped polling, and forgets matches that
// ended long enough ago that nobody will ask about them.
func (m *Matchmaker) expire() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	ttl := m.cfg.Timing.TicketTTL()
	assignTTL := m.cfg.Timing.AssignmentTTL()

	for id, t := range m.tickets {
		switch t.state {
		case tsSearching:
			if now.Sub(t.lastPoll) > ttl {
				t.state = tsExpired
				m.log.Info("ticket expired", "ticket", id, "leader", t.Leader)
			}
		case tsCancelled, tsExpired, tsFailed, tsAssigned:
			if now.Sub(t.lastPoll) > assignTTL {
				delete(m.tickets, id)
				m.forgetLeaderLocked(t)
			}
		}
	}
	for id, mt := range m.matches {
		if mt.state == msOver && now.Sub(mt.createdAt) > assignTTL {
			delete(m.matches, id)
		}
	}
}

func (m *Matchmaker) forgetLeaderLocked(t *Ticket) {
	key := leaderKey{t.Leader, t.MatchGroup}
	if m.byLeader[key] == t.ID {
		delete(m.byLeader, key)
	}
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("mm: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// sortedSearchingLocked returns the searching tickets for a group, oldest first.
func (m *Matchmaker) sortedSearchingLocked(g wire.MatchGroup) []*Ticket {
	var out []*Ticket
	for _, t := range m.tickets {
		if t.MatchGroup == g && t.state == tsSearching {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].queuedAt.Equal(out[j].queuedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].queuedAt.Before(out[j].queuedAt)
	})
	return out
}
