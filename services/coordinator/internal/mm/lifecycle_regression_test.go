package mm

import (
	"context"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

func TestEnqueueAfterMatchOverGetsFreshTicket(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(2, 2, 4, 0), 1)

	const leader = "7656119801000000"
	first := party(t, m, leader, 1)
	party(t, m, "7656119802000000", 1)
	settle(m)

	if first.state != tsAssigned || first.matchID == "" {
		t.Fatalf("first ticket = state %q match %q, want an assignment", first.state, first.matchID)
	}
	oldTicketID := first.ID
	oldMatchID := first.matchID

	if err := m.ReportResult(context.Background(), wire.MatchResult{
		MatchID: oldMatchID,
		Winner:  wire.TeamRed,
	}); err != nil {
		t.Fatalf("report result: %v", err)
	}

	again := party(t, m, leader, 1)
	if again.ID == oldTicketID {
		t.Fatalf("requeue replayed stale ticket %q from completed match %q", again.ID, oldMatchID)
	}
	if again.state != tsSearching || again.matchID != "" || again.assignment != nil {
		t.Fatalf("fresh ticket = state %q match %q assignment %#v, want searching", again.state, again.matchID, again.assignment)
	}
}

func TestEndMatchRequeuesUnannouncedSeat(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(2, 2, 4, 0), 1)

	a := party(t, m, "7656119803000000", 1)
	party(t, m, "7656119804000000", 1)
	settle(m)
	if a.matchID == "" {
		t.Fatal("initial match was not formed")
	}

	pending := party(t, m, "7656119805000000", 1)
	m.mu.Lock()
	mt := m.matches[a.matchID]
	if mt == nil || mt.state != msLive {
		m.mu.Unlock()
		t.Fatalf("match %q is not live", a.matchID)
	}
	m.addToMatchLocked(mt, pending, wire.TeamRed)
	m.mu.Unlock()

	if pending.state != tsMatched || pending.assignment != nil {
		t.Fatalf("pending ticket = state %q assignment %#v, want unannounced tsMatched", pending.state, pending.assignment)
	}

	if err := m.ReportResult(context.Background(), wire.MatchResult{
		MatchID: mt.ID,
		Winner:  wire.TeamBlu,
	}); err != nil {
		t.Fatalf("report result: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if pending.state != tsSearching || pending.matchID != "" || pending.assignment != nil {
		t.Fatalf("pending ticket after end = state %q match %q assignment %#v, want searching", pending.state, pending.matchID, pending.assignment)
	}
	for _, p := range mt.Players {
		for _, mine := range pending.Players {
			if p.SteamID == mine.SteamID {
				t.Fatalf("unannounced player %s remained in finished match roster", p.SteamID)
			}
		}
	}
	for _, id := range mt.tickets {
		if id == pending.ID {
			t.Fatalf("unannounced ticket %q remained attached to finished match", id)
		}
	}
}

func TestEndedMatchTTLStartsWhenMatchEnds(t *testing.T) {
	cfg := testConfig(2, 2, 2, 0)
	cfg.Timing.AssignmentTTLSecs = 10
	m, _, clock := newTestMM(t, cfg, 1)

	a := party(t, m, "7656119806000000", 1)
	party(t, m, "7656119807000000", 1)
	settle(m)
	matchID := a.matchID
	if matchID == "" {
		t.Fatal("match was not formed")
	}

	// Let the live match itself last longer than AssignmentTTL. That must not
	// consume the post-match retention window.
	*clock = clock.Add(20 * time.Second)
	if err := m.ReportResult(context.Background(), wire.MatchResult{
		MatchID: matchID,
		Winner:  wire.TeamRed,
	}); err != nil {
		t.Fatalf("report result: %v", err)
	}

	m.expire()
	m.mu.Lock()
	_, stillThere := m.matches[matchID]
	m.mu.Unlock()
	if !stillThere {
		t.Fatal("finished match expired immediately because its live time was counted against AssignmentTTL")
	}

	*clock = clock.Add(11 * time.Second)
	m.expire()
	m.mu.Lock()
	_, stillThere = m.matches[matchID]
	m.mu.Unlock()
	if stillThere {
		t.Fatal("finished match was retained past AssignmentTTL after it ended")
	}
}
