package mm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// boot gets a server for a formed match, prepares it, and publishes the
// assignment to the match's tickets.
//
// It runs off the matchmaker's goroutine because acquiring a server can mean an
// HTTP reservation and an RCON round-trip. Failure re-queues the parties rather
// than dropping them: they were promised a match, not a specific server.
func (m *Matchmaker) boot(ctx context.Context, mt *Match) {
	group, _ := m.cfg.Group(mt.MatchGroup)

	bootCtx, cancel := context.WithTimeout(ctx, m.cfg.Pool.BootDeadline())
	defer cancel()

	srv, err := m.pool.Acquire(bootCtx, pool.Request{
		MatchID: mt.ID,
		Players: len(mt.Players),
		Minutes: int(m.cfg.Pool.MaxMatch().Minutes()),
	})
	if err != nil {
		m.failMatch(mt, fmt.Errorf("no server: %w", err), errors.Is(err, pool.ErrNoServer))
		return
	}

	spec := Spec{
		MatchID:      mt.ID,
		Map:          mt.Map,
		Password:     mt.Password,
		ServerConfig: group.ServerConfig,
		Players:      len(mt.Players),
		MaxPlayers:   group.MaxPlayers,
	}
	if err := m.setup.Setup(bootCtx, srv, spec); err != nil {
		if relErr := m.pool.Release(context.WithoutCancel(ctx), srv); relErr != nil {
			m.log.Warn("could not release server after failed setup", "server", srv.Connect, "err", relErr)
		}
		m.failMatch(mt, fmt.Errorf("server %s did not come up: %w", srv.Connect, err), true)
		return
	}

	now := m.now()
	m.mu.Lock()
	mt.Server = srv
	mt.state = msLive
	mt.startedAt = now
	mt.lastNonEmpty = now
	for _, id := range mt.tickets {
		t, ok := m.tickets[id]
		if !ok {
			continue
		}
		t.state = tsAssigned
		t.assignment = m.assignmentLocked(mt, teamOf(mt, t), false)
	}
	m.mu.Unlock()

	m.log.Info("match live", "match", mt.ID, "server", srv.Connect, "map", mt.Map, "players", len(mt.Players))
}

// teamOf returns the team a ticket's players were put on.
func teamOf(mt *Match, t *Ticket) wire.Team {
	if len(t.Players) == 0 {
		return wire.TeamUnassigned
	}
	want := t.Players[0].SteamID
	for _, p := range mt.Players {
		if p.SteamID == want {
			return p.Team
		}
	}
	return wire.TeamUnassigned
}

// failMatch abandons a match that never started. requeue puts the parties back
// at the front of the queue, keeping their original wait time so they are not
// punished for the coordinator's problem.
func (m *Matchmaker) failMatch(mt *Match, cause error, requeue bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mt.state = msOver
	for _, id := range mt.tickets {
		t, ok := m.tickets[id]
		if !ok {
			continue
		}
		if requeue {
			t.state = tsSearching
			t.matchID = ""
			t.lastPoll = m.now()
		} else {
			t.state = tsFailed
			t.failure = cause.Error()
		}
	}
	m.log.Warn("match aborted before it started", "match", mt.ID, "requeued", requeue, "err", cause)
}

// superviseMatches ends matches that are empty, over time, or finished.
func (m *Matchmaker) superviseMatches(ctx context.Context) {
	type check struct {
		mt  *Match
		srv *pool.Server
	}
	var toPoll []check
	var toEnd []*Match

	now := m.now()
	m.mu.Lock()
	for _, mt := range m.matches {
		switch mt.state {
		case msBooting:
			if now.Sub(mt.createdAt) > m.cfg.Pool.BootDeadline()+10*time.Second {
				// boot() owns this match and should have finished by now.
				// Something wedged; let it go rather than leak the parties.
				toEnd = append(toEnd, mt)
			}
		case msLive:
			if now.Sub(mt.startedAt) > m.cfg.Pool.MaxMatch() {
				toEnd = append(toEnd, mt)
				continue
			}
			if now.Sub(mt.lastNonEmpty) > m.cfg.Pool.IdleEnd() {
				toEnd = append(toEnd, mt)
				continue
			}
			if now.Sub(mt.lastPolled) > 30*time.Second {
				mt.lastPolled = now
				toPoll = append(toPoll, check{mt, mt.Server})
			}
		}
	}
	m.mu.Unlock()

	for _, mt := range toEnd {
		m.endMatch(ctx, mt, nil)
	}
	for _, c := range toPoll {
		go func(mt *Match, srv *pool.Server) {
			n, ok := m.setup.PlayerCount(ctx, srv)
			if !ok {
				return
			}
			m.mu.Lock()
			mt.players = n
			if n > 0 {
				mt.lastNonEmpty = m.now()
			}
			m.mu.Unlock()
		}(c.mt, c.srv)
	}
}

// ObserveServer records what a game server reported about itself. It is the
// heartbeat path: a server that says it has players keeps its match alive
// without the coordinator having to RCON it.
func (m *Matchmaker) ObserveServer(matchID string, players int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.matches[matchID]
	if !ok || mt.state != msLive {
		return
	}
	mt.players = players
	if players > 0 {
		mt.lastNonEmpty = m.now()
	}
}

// ReportResult records a finished match: the war hears about it, and the server
// goes back to the pool.
func (m *Matchmaker) ReportResult(ctx context.Context, res wire.MatchResult) error {
	m.mu.Lock()
	mt, ok := m.matches[res.MatchID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no match %q", res.MatchID)
	}
	if mt.state == msOver {
		m.mu.Unlock()
		return nil // a duplicate report is not an error
	}
	m.mu.Unlock()

	m.endMatch(ctx, mt, &res)
	return nil
}

// endMatch is the single place a match stops existing. res may be nil, which
// means the match ended without a reported result (empty server, time limit);
// the war is left untouched in that case, because nobody won.
func (m *Matchmaker) endMatch(ctx context.Context, mt *Match, res *wire.MatchResult) {
	m.mu.Lock()
	if mt.state == msOver {
		m.mu.Unlock()
		return
	}
	mt.state = msOver
	srv := mt.Server
	frontID := mt.FrontID
	briefing := mt.War
	for _, id := range mt.tickets {
		if t, ok := m.tickets[id]; ok && t.state != tsFailed {
			// Keep the assignment fetchable: a client that reconnects during
			// the match-over screen still wants to know where it was.
			t.state = tsAssigned
		}
	}
	m.mu.Unlock()

	if res != nil && !res.Aborted && frontID != "" && m.war != nil && briefing != nil {
		attackerWon := res.Winner == briefing.AttackerTeam && briefing.AttackerTeam != wire.TeamUnassigned
		if err := m.war.ApplyResult(frontID, mt.ID, attackerWon); err != nil {
			m.log.Error("war could not record the result", "match", mt.ID, "front", frontID, "err", err)
		} else {
			m.log.Info("war advanced", "match", mt.ID, "front", frontID, "attacker_won", attackerWon)
		}
	}

	if srv != nil {
		releaseCtx := context.WithoutCancel(ctx)
		if err := m.setup.Teardown(releaseCtx, srv); err != nil {
			m.log.Debug("teardown failed", "server", srv.Connect, "err", err)
		}
		if err := m.pool.Release(releaseCtx, srv); err != nil {
			m.log.Warn("could not release server", "server", srv.Connect, "err", err)
		}
	}
	m.log.Info("match over", "match", mt.ID, "reported", res != nil)
}

// reconcileWar keeps the number of open fronts in step with how many people are
// playing. It is a no-op when the war is off.
func (m *Matchmaker) reconcileWar() {
	if m.war == nil {
		return
	}
	if _, err := m.war.Reconcile(m.Population()); err != nil {
		m.log.Error("war could not reconcile fronts", "err", err)
	}
}
