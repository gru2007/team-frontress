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

	// The seats are sold, but the door is not open yet: the server admits
	// exactly the people in the lobby it was handed, so it has to be told
	// before the client is. Grouped by match, because one RCON round trip can
	// carry every seat sold in this match on this tick.
	byMatch := map[*Match][]*Ticket{}
	var order []*Match
	for _, p := range placed {
		if _, seen := byMatch[p.mt]; !seen {
			order = append(order, p.mt)
		}
		byMatch[p.mt] = append(byMatch[p.mt], p.tick)
	}

	for _, mt := range order {
		go m.admit(ctx, mt, byMatch[mt], "backfill")
	}
}

// admit tells a running match's server about seats already sold, and only then
// hands the clients their assignment.
//
// The order is the point. Publishing the assignment first would send players at
// a server that has not been told to expect them, and a roster-gated server
// answers that with a refused connect and no explanation. If the server cannot
// be told, the seats are given back: a player left in the queue is a much
// better outcome than one bounced off a server they were sent to.
func (m *Matchmaker) admit(ctx context.Context, mt *Match, tickets []*Ticket, why string) {
	m.mu.Lock()
	srv := mt.Server
	matchID := mt.ID
	live := (mt.state == msLive)
	var roster []wire.AssignedPlayer
	for _, t := range tickets {
		roster = append(roster, t.Players...)
	}
	m.mu.Unlock()

	if !live || srv == nil {
		m.releaseSeats(mt, tickets, "the match is no longer live")
		return
	}

	if err := m.setup.AddPlayers(ctx, srv, matchID, roster); err != nil {
		m.log.Warn("could not seat players in a running match",
			"match", matchID, "why", why, "players", len(roster), "err", err)
		m.releaseSeats(mt, tickets, "the server would not take them")
		return
	}

	m.mu.Lock()
	for _, t := range tickets {
		// A ticket the player cancelled while we were talking to the server
		// has already left; seating it now would strand it.
		if t.state != tsMatched || t.matchID != matchID {
			continue
		}
		t.state = tsAssigned
		t.assignment = m.assignmentLocked(mt, teamOf(mt, t), true)
	}
	// Everyone already in the match sees a roster that just changed. Refresh
	// the assignments we handed out so a client that re-polls is not looking
	// at a stale team list.
	for _, id := range mt.tickets {
		other, ok := m.tickets[id]
		if !ok || other.assignment == nil {
			continue
		}
		other.assignment.Roster = append([]wire.AssignedPlayer(nil), mt.Players...)
	}
	nowIn := len(mt.Players)
	m.mu.Unlock()

	m.log.Info("seated in a running match",
		"match", matchID, "why", why, "players", len(roster), "now", nowIn)
}

// releaseSeats undoes a seating that could not be announced to the server.
func (m *Matchmaker) releaseSeats(mt *Match, tickets []*Ticket, why string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, t := range tickets {
		if t.state != tsMatched || t.matchID != mt.ID {
			continue
		}

		// Take their players back out of the match.
		kept := mt.Players[:0]
		for _, p := range mt.Players {
			drop := false
			for _, mine := range t.Players {
				if mine.SteamID == p.SteamID {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, p)
			}
		}
		mt.Players = kept

		for i, id := range mt.tickets {
			if id == t.ID {
				mt.tickets = append(mt.tickets[:i], mt.tickets[i+1:]...)
				break
			}
		}

		// Back in the queue, keeping the wait they had already served.
		t.state = tsSearching
		t.matchID = ""
		t.assignment = nil
	}

	m.log.Warn("gave back seats in a running match", "match", mt.ID, "why", why, "tickets", len(tickets))
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
// addToMatchLocked seats a queued party in a running match. It stops one step
// short of handing over the connect details: the ticket is tsMatched, not
// tsAssigned, until admit() has told the server to expect them. See admit.
func (m *Matchmaker) addToMatchLocked(mt *Match, t *Ticket, team wire.Team) {
	t.state = tsMatched
	t.matchID = mt.ID
	mt.tickets = append(mt.tickets, t.ID)

	// Index, not range: the ticket has to carry its own team, because admit()
	// builds the announcement to the server from t.Players. Assigning to the
	// loop copy set the team on the entry going into the match and left the
	// ticket's own saying "unassigned", which the server reads as "put them in
	// the player pool" -- a seat on neither side.
	for i := range t.Players {
		t.Players[i].Team = team
		mt.Players = append(mt.Players, t.Players[i])
	}

	// A match that was about to be ended for being empty is not empty any
	// more, and the people we just sent there need time to arrive.
	mt.lastNonEmpty = m.now()
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
