package mm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

func teamCounts(t *testing.T, m *Matchmaker, matchID string) (red, blu int) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.matches[matchID]
	if !ok {
		t.Fatalf("no match %s", matchID)
	}
	for _, p := range mt.Players {
		switch p.Team {
		case wire.TeamRed:
			red++
		case wire.TeamBlu:
			blu++
		}
	}
	return red, blu
}

func onlyMatchID(t *testing.T, m *Matchmaker) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.matches) != 1 {
		t.Fatalf("matches = %d, want exactly 1", len(m.matches))
	}
	for id := range m.matches {
		return id
	}
	return ""
}

// The point of the whole feature: a match that started small keeps filling
// instead of a second, emptier one starting next to it.
func TestLateArrivalsJoinTheRunningMatch(t *testing.T) {
	cfg := testConfig(4, 4, 12, 0)
	cfg.MatchGroups[0].Mode = config.ModeFrontline
	m, _, _ := newTestMM(t, cfg, 2) // two servers free, so a second match *could* start

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)

	matchID := onlyMatchID(t, m)

	late := party(t, m, "7656119822222222", 2)
	settle(m)

	if got := onlyMatchID(t, m); got != matchID {
		t.Fatal("a second match was started while the first had room")
	}
	st, _ := m.Status(late.ID)
	if st.State != wire.QueueStateAssigned {
		t.Fatalf("late party state = %q, want assigned", st.State)
	}
	if !st.Assignment.LateJoin {
		t.Error("the assignment does not say it was a late join")
	}
	if st.Assignment.MatchID != matchID {
		t.Fatalf("late party sent to %s, want the running match %s", st.Assignment.MatchID, matchID)
	}
	if len(st.Assignment.Roster) != 6 {
		t.Fatalf("roster has %d, want all 6 players", len(st.Assignment.Roster))
	}
}

func TestBackfillKeepsTheTeamsEven(t *testing.T) {
	cfg := testConfig(4, 4, 8, 0)
	m, _, _ := newTestMM(t, cfg, 1)

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)
	matchID := onlyMatchID(t, m)

	// Four singles arrive one at a time, which is the worst case for balance.
	for i := 0; i < 4; i++ {
		party(t, m, string(rune('a'+i))+"765611980000000", 1)
		settle(m)
	}

	red, blu := teamCounts(t, m, matchID)
	if red+blu != 8 {
		t.Fatalf("match holds %d players, want 8", red+blu)
	}
	if red != 4 || blu != 4 {
		t.Fatalf("teams are %d v %d, want 4 v 4", red, blu)
	}
}

func TestBackfillNeverOverfillsATeam(t *testing.T) {
	// Team cap is 3. A party of three fits only on an empty side.
	cfg := testConfig(4, 4, 6, 0)
	m, _, _ := newTestMM(t, cfg, 1)

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)
	matchID := onlyMatchID(t, m)

	// Two seats left, one per team. A party of two cannot take them both.
	trio := party(t, m, "7656119822222222", 2)
	settle(m)

	red, blu := teamCounts(t, m, matchID)
	if red > 3 || blu > 3 {
		t.Fatalf("teams are %d v %d, over the cap of 3", red, blu)
	}
	if st, _ := m.Status(trio.ID); st.State == wire.QueueStateAssigned && st.Assignment.MatchID == matchID {
		if red+blu != 6 {
			t.Fatalf("the party was seated but the match holds %d", red+blu)
		}
	}
}

func TestAFullMatchStopsTakingPlayers(t *testing.T) {
	cfg := testConfig(4, 4, 4, 0)
	m, _, _ := newTestMM(t, cfg, 1) // exactly one server

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)
	matchID := onlyMatchID(t, m)

	late := party(t, m, "7656119822222222", 2)
	settle(m)

	if red, blu := teamCounts(t, m, matchID); red+blu != 4 {
		t.Fatalf("the full match grew to %d players", red+blu)
	}
	st, _ := m.Status(late.ID)
	if st.State != wire.QueueStateSearching {
		t.Fatalf("late party state = %q, want searching: there was nowhere to put them", st.State)
	}
}

// Ranked matches are formed once and left alone.
func TestRankedDoesNotBackfill(t *testing.T) {
	cfg := testConfig(4, 4, 12, 0)
	cfg.MatchGroups[0].Mode = config.ModeRanked
	m, _, _ := newTestMM(t, cfg, 2)

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)
	matchID := onlyMatchID(t, m)

	late := party(t, m, "7656119822222222", 2)
	settle(m)

	if red, blu := teamCounts(t, m, matchID); red+blu != 4 {
		t.Fatalf("a ranked match grew to %d players", red+blu)
	}
	if st, _ := m.Status(late.ID); st.State == wire.QueueStateAssigned &&
		st.Assignment.MatchID == matchID {
		t.Fatal("the late party was backfilled into a ranked match")
	}
}

func TestBackfillWindowCloses(t *testing.T) {
	cfg := testConfig(4, 4, 12, 0)
	cfg.MatchGroups[0].BackfillSecs = 60
	m, _, clock := newTestMM(t, cfg, 2)

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)
	matchID := onlyMatchID(t, m)

	*clock = clock.Add(61 * time.Second)
	late := party(t, m, "7656119822222222", 2)
	settle(m)

	if st, _ := m.Status(late.ID); st.State == wire.QueueStateAssigned &&
		st.Assignment.MatchID == matchID {
		t.Fatal("a party was backfilled after the window closed")
	}
}

// A server about to be dropped for being empty must not be dropped when the
// coordinator has just sent people to it.
func TestBackfillKeepsTheMatchAlive(t *testing.T) {
	cfg := testConfig(4, 4, 12, 0)
	cfg.Pool.IdleEndSecs = 60
	m, setup, clock := newTestMM(t, cfg, 1)
	setup.players = 0 // nobody has loaded in yet

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)

	*clock = clock.Add(59 * time.Second)
	party(t, m, "7656119822222222", 2)
	settle(m)

	*clock = clock.Add(30 * time.Second)
	m.Tick(context.Background())

	if m.LiveMatches() != 1 {
		t.Fatal("the match was ended for being empty right after it was backfilled")
	}
}

func TestOpenMatchesIsReported(t *testing.T) {
	cfg := testConfig(4, 4, 12, 0)
	m, _, _ := newTestMM(t, cfg, 1)

	if got := m.OpenMatches()[wire.MatchGroupCasual12v12]; got != 0 {
		t.Fatalf("open matches = %d before anything ran", got)
	}

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)

	if got := m.OpenMatches()[wire.MatchGroupCasual12v12]; got != 1 {
		t.Fatalf("open matches = %d, want 1", got)
	}
}

// The roster gate means a backfilled player is refused at the door unless the
// server was told about them. This is the test that the door gets opened.
func TestBackfillTellsTheServerBeforeTheClient(t *testing.T) {
	cfg := testConfig(4, 4, 12, 0)
	cfg.MatchGroups[0].Mode = config.ModeFrontline
	m, setup, _ := newTestMM(t, cfg, 1)

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)
	matchID := onlyMatchID(t, m)

	late := party(t, m, "7656119822222222", 2)
	settle(m)

	calls := setup.addedSeats()
	if len(calls) != 1 {
		t.Fatalf("the server was told about seats %d times, want once", len(calls))
	}
	if calls[0].MatchID != matchID {
		t.Fatalf("seats announced for %s, want %s", calls[0].MatchID, matchID)
	}
	if len(calls[0].Roster) != 2 {
		t.Fatalf("announced %d seats, want the 2 that were sold", len(calls[0].Roster))
	}
	for _, p := range calls[0].Roster {
		if p.Team != wire.TeamRed && p.Team != wire.TeamBlu {
			t.Fatalf("seat for %s has no team; the server would not know where to put them", p.SteamID)
		}
	}

	st, _ := m.Status(late.ID)
	if st.State != wire.QueueStateAssigned {
		t.Fatalf("state = %q, want assigned once the server had been told", st.State)
	}
}

// If the server cannot be told, the players must go back in the queue rather
// than be sent at a door that will not open for them.
func TestBackfillGivesTheSeatBackWhenTheServerCannotBeTold(t *testing.T) {
	cfg := testConfig(4, 4, 12, 0)
	cfg.MatchGroups[0].Mode = config.ModeFrontline
	m, setup, _ := newTestMM(t, cfg, 1)

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)
	matchID := onlyMatchID(t, m)

	setup.mu.Lock()
	setup.addFail = errors.New("rcon refused")
	setup.mu.Unlock()

	late := party(t, m, "7656119822222222", 2)
	settle(m)

	st, _ := m.Status(late.ID)
	if st.State != wire.QueueStateSearching {
		t.Fatalf("state = %q, want searching: an un-announced seat is not a seat", st.State)
	}
	if st.Assignment != nil {
		t.Fatal("the client was given connect details for a server that will refuse it")
	}

	red, blu := teamCounts(t, m, matchID)
	if red+blu != 4 {
		t.Fatalf("match holds %d players, want the original 4 back", red+blu)
	}
}

// Standby: a party member asking to be let into the game their party is
// already in. Same seat, same gate, different reason for wanting it.
func TestStandbyPutsYouInYourPartysMatch(t *testing.T) {
	cfg := testConfig(4, 4, 12, 0)
	cfg.MatchGroups[0].Mode = config.ModeFrontline
	m, setup, _ := newTestMM(t, cfg, 1)

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)
	matchID := onlyMatchID(t, m)

	late, err := m.Enqueue(&Ticket{
		MatchGroup:     cfg.MatchGroups[0].MatchGroup,
		Leader:         "7656119822222222",
		Players:        []wire.AssignedPlayer{{SteamID: "7656119822222222"}},
		StandbyMatchID: matchID,
	})
	if err != nil {
		t.Fatalf("standby: %v", err)
	}
	waitAssigned(t, m, late)

	st, _ := m.Status(late.ID)
	if st.State != wire.QueueStateAssigned {
		t.Fatalf("state = %q, want assigned", st.State)
	}
	if st.Assignment.MatchID != matchID {
		t.Fatalf("sent to %s, want the party's match %s", st.Assignment.MatchID, matchID)
	}
	if !st.Assignment.LateJoin {
		t.Error("a standby join is a late join")
	}

	calls := setup.addedSeats()
	if len(calls) != 1 || calls[0].MatchID != matchID {
		t.Fatalf("the server was not told to expect the standby player: %+v", calls)
	}
}

func TestStandbySaysWhyItCannot(t *testing.T) {
	cfg := testConfig(4, 4, 4, 0) // max 4: the match fills exactly
	cfg.MatchGroups[0].Mode = config.ModeFrontline
	m, _, _ := newTestMM(t, cfg, 1)

	party(t, m, "7656119800000000", 2)
	party(t, m, "7656119811111111", 2)
	settle(m)
	matchID := onlyMatchID(t, m)

	_, err := m.Enqueue(&Ticket{
		MatchGroup:     cfg.MatchGroups[0].MatchGroup,
		Leader:         "7656119822222222",
		Players:        []wire.AssignedPlayer{{SteamID: "7656119822222222"}},
		StandbyMatchID: matchID,
	})
	if err == nil {
		t.Fatal("standby into a full match was accepted")
	}

	// A match that does not exist is a refusal, not a crash or a silent queue.
	if _, err := m.Enqueue(&Ticket{
		MatchGroup:     cfg.MatchGroups[0].MatchGroup,
		Leader:         "7656119833333333",
		Players:        []wire.AssignedPlayer{{SteamID: "7656119833333333"}},
		StandbyMatchID: "nosuchmatch",
	}); err == nil {
		t.Fatal("standby into a match that does not exist was accepted")
	}
}
