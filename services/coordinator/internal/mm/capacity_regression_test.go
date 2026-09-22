package mm

import (
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

func TestBackfillUsesPerMatchCapacity(t *testing.T) {
	cfg := testConfig(2, 2, 12, 0)
	m, _, _ := newTestMM(t, cfg, 1)

	party(t, m, "7656119808100000", 1)
	party(t, m, "7656119808200000", 1)
	settle(m)
	matchID := onlyMatchID(t, m)

	m.mu.Lock()
	m.matches[matchID].MaxPlayers = 4
	m.mu.Unlock()

	lateA := party(t, m, "7656119808300000", 1)
	settle(m)
	lateB := party(t, m, "7656119808400000", 1)
	settle(m)
	for _, tk := range []*Ticket{lateA, lateB} {
		st, _ := m.Status(tk.ID)
		if st.State != wire.QueueStateAssigned || st.Assignment == nil || st.Assignment.MatchID != matchID {
			t.Fatalf("late ticket %s was not admitted to the open match: %#v", tk.ID, st)
		}
	}

	blocked := party(t, m, "7656119808500000", 1)
	settle(m)
	st, _ := m.Status(blocked.ID)
	if st.State != wire.QueueStateSearching {
		t.Fatalf("ticket beyond per-match capacity = %q, want searching", st.State)
	}

	red, blu := teamCounts(t, m, matchID)
	if red+blu != 4 {
		t.Fatalf("match has %d players, want its per-match capacity of 4", red+blu)
	}
}

func TestStandbyUsesPerMatchCapacity(t *testing.T) {
	cfg := testConfig(2, 2, 12, 0)
	m, _, _ := newTestMM(t, cfg, 1)

	party(t, m, "7656119808600000", 1)
	party(t, m, "7656119808700000", 1)
	settle(m)
	matchID := onlyMatchID(t, m)

	m.mu.Lock()
	m.matches[matchID].MaxPlayers = 2
	m.mu.Unlock()

	_, err := m.Enqueue(&Ticket{
		MatchGroup:     cfg.MatchGroups[0].MatchGroup,
		Leader:         "7656119808800000",
		Players:        []wire.AssignedPlayer{{SteamID: "7656119808800000"}},
		StandbyMatchID: matchID,
	})
	if err == nil {
		t.Fatal("standby exceeded the match's own capacity")
	}
}
