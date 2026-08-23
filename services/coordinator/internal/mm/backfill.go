package mm

import (
	"context"
	"sort"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// backfill tops up matches that are already running.
//
// This is what makes a peak of thirty-two people fill servers instead of
// scattering them: a match that started as a 4v4 because that is who was in
// queue keeps taking players until it is full, and the queue drains into a
// running game rather than waiting to start a second one.
//
// One rule keeps it balanced with no arithmetic anywhere else: a party may only
// join a team that has room under the group's team cap, which is half the
// match. Neither side can grow past half, so the sides can never differ by more
// than the parties waiting to be placed.
func (m *Matchmaker) backfill(ctx context.Context, group config.MatchGroupConfig) {
	if !group.BBackfills() {
		return
	}

	m.mu.Lock()
	queue := m.sortedSearchingLocked(group.MatchGroup)
	if len(queue) == 0 {
		m.mu.Unlock()
		return
	}
	open := m.openMatchesLocked(group)
	if len(open) == 0 {
		m.mu.Unlock()
		return
	}

	type placement struct {
		mt   *Match
		tick *Ticket
		team wire.Team
	}
	var placed []placement

	for _, t := range queue {
		for _, mt := range open {
			team, ok := roomFor(mt, t.Size(), group.TeamCap())
			if !ok {
				continue
			}
			m.addToMatchLocked(mt, t, team)
			placed = append(placed, placement{mt, t, team})
			break
		}
	}
	m.mu.Unlock()

	for _, p := range placed {
		m.log.Info("backfilled",
			"match", p.mt.ID, "ticket", p.tick.ID, "players", p.tick.Size(),
			"team", p.team, "now", len(p.mt.Players), "max", group.MaxPlayers)
	}
}

// openMatchesLocked returns the live matches of a group that will take more
// players, emptiest first so the queue spreads rather than piling onto one.
func (m *Matchmaker) openMatchesLocked(group config.MatchGroupConfig) []*Match {
	now := m.now()
	var out []*Match
	for _, mt := range m.matches {
		if mt.state != msLive || mt.MatchGroup != group.MatchGroup {
			continue
		}
		if len(mt.Players) >= group.MaxPlayers {
			continue
		}
		if group.BackfillSecs > 0 &&
			now.Sub(mt.startedAt) > time.Duration(group.BackfillSecs)*time.Second {
			continue
		}
		out = append(out, mt)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Players) != len(out[j].Players) {
			return len(out[i].Players) < len(out[j].Players)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// roomFor picks the team a party of n can join, or reports that it cannot.
// The smaller side is tried first so a match evens out as it fills.
func roomFor(mt *Match, n, teamCap int) (wire.Team, bool) {
	red, blu := 0, 0
	for _, p := range mt.Players {
		switch p.Team {
		case wire.TeamRed:
			red++
		case wire.TeamBlu:
			blu++
		}
	}

	first, second := wire.TeamRed, wire.TeamBlu
	firstN, secondN := red, blu
	if blu < red {
		first, second = wire.TeamBlu, wire.TeamRed
		firstN, secondN = blu, red
	}

	if firstN+n <= teamCap {
		return first, true
	}
	if secondN+n <= teamCap {
		return second, true
	}
	return wire.TeamUnassigned, false
}

// addToMatchLocked seats a queued party in a running match and hands it the
// connect details straight away. There is no "match found" moment for a
// backfill: the game is already happening.
func (m *Matchmaker) addToMatchLocked(mt *Match, t *Ticket, team wire.Team) {
	t.state = tsAssigned
	t.matchID = mt.ID
	mt.tickets = append(mt.tickets, t.ID)

	for _, p := range t.Players {
		p.Team = team
		mt.Players = append(mt.Players, p)
	}

	// A match that was about to be ended for being empty is not empty any
	// more, and the people we just sent there need time to arrive.
	mt.lastNonEmpty = m.now()

	t.assignment = m.assignmentLocked(mt, team, true)

	// Everyone already in the match sees a roster that just changed. Refresh
	// the assignments we handed out so a client that re-polls is not looking at
	// a stale team list.
	for _, id := range mt.tickets {
		other, ok := m.tickets[id]
		if !ok || other == t || other.assignment == nil {
			continue
		}
		other.assignment.Roster = append([]wire.AssignedPlayer(nil), mt.Players...)
	}
}

// assignmentLocked builds what a client is told about a match.
func (m *Matchmaker) assignmentLocked(mt *Match, team wire.Team, lateJoin bool) *wire.Assignment {
	a := &wire.Assignment{
		MatchID:    mt.ID,
		MatchGroup: mt.MatchGroup,
		Map:        mt.Map,
		Password:   mt.Password,
		Team:       team,
		Roster:     append([]wire.AssignedPlayer(nil), mt.Players...),
		War:        mt.War,
		LateJoin:   lateJoin,
	}
	if mt.Server != nil {
		a.Connect = mt.Server.Connect
		a.STV = mt.Server.STV
	}
	return a
}
