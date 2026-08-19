package mm

import (
	"testing"
	"time"

	"github.com/greyline-frontress/coordinator/internal/war"
)

func partyPlayer(steamID uint64, side war.Side, partyID string, queuedAt time.Time) *Player {
	return &Player{
		SteamID:  steamID,
		Side:     side,
		State:    StateQueued,
		PartyID:  partyID,
		QueuedAt: queuedAt,
	}
}

// TestWithPartiesClustersTheQueue is the scenario the essay this rework was
// scoped against calls out by name: quickplay has no way to say "these two
// want to land together". Player 3 and player 6 share a party but queued
// three slots apart, which under plain FIFO puts them in different formed
// battles once a front is forming four at a time. withParties must pull them
// adjacent without moving anyone who queued earlier than either of them.
func TestWithPartiesClustersTheQueue(t *testing.T) {
	base := time.Now()
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Second) }

	queued := []*Player{
		partyPlayer(1, war.SideRed, "", at(0)),
		partyPlayer(2, war.SideRed, "", at(1)),
		partyPlayer(3, war.SideBlu, "duo", at(2)),
		partyPlayer(4, war.SideBlu, "", at(3)),
		partyPlayer(5, war.SideRed, "", at(4)),
		partyPlayer(6, war.SideBlu, "duo", at(5)),
		partyPlayer(7, war.SideRed, "", at(6)),
		partyPlayer(8, war.SideBlu, "", at(7)),
	}

	out := withParties(queued)

	got := make([]uint64, len(out))
	for i, p := range out {
		got[i] = p.SteamID
	}
	want := []uint64{1, 2, 3, 6, 4, 5, 7, 8}
	if len(got) != len(want) {
		t.Fatalf("withParties dropped or added players: got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("withParties order = %v, want %v (player 6 must land right after their party-mate 3, "+
				"without passing players 1 and 2 who queued before either of them)", got, want)
		}
	}

	// The first four — the slice a real formOne call would pick for a 2v2 —
	// now contains both party members instead of splitting them across two
	// separate battles.
	firstFour := map[uint64]bool{}
	for _, p := range out[:4] {
		firstFour[p.SteamID] = true
	}
	if !firstFour[3] || !firstFour[6] {
		t.Fatalf("party members 3 and 6 were split across the batch boundary: first four = %v", got[:4])
	}
}

// TestWithPartiesNeverReordersASoloQueue confirms the fast path: a queue with
// nobody partied up is returned exactly as FIFO already had it.
func TestWithPartiesNeverReordersASoloQueue(t *testing.T) {
	base := time.Now()
	queued := []*Player{
		partyPlayer(1, war.SideRed, "", base),
		partyPlayer(2, war.SideBlu, "", base.Add(time.Second)),
		partyPlayer(3, war.SideRed, "", base.Add(2*time.Second)),
	}
	out := withParties(queued)
	for i, p := range out {
		if p.SteamID != queued[i].SteamID {
			t.Fatalf("withParties reordered a queue with no parties: %v", out)
		}
	}
}

// TestAssignSlotsKeepsAPartyOnOneSideWhenThereIsRoom is the other half of the
// party feature: even once co-picked into the same battle, assignSlots must
// not let ordinary red/blu balancing split them onto opposing teams — that
// would defeat the entire point of queueing together.
func TestAssignSlotsKeepsAPartyOnOneSideWhenThereIsRoom(t *testing.T) {
	plan := war.BattlePlan{Attacker: war.SideRed, Defender: war.SideBlu}
	// Both party members prefer opposite sides (as if one hasn't picked yet);
	// the second should follow the first onto whichever side it landed on
	// rather than being balanced onto the other.
	picked := []*Player{
		{SteamID: 1, Side: war.SideRed, PartyID: "duo"},
		{SteamID: 2, Side: war.SideBlu, PartyID: "duo"},
		{SteamID: 3, Side: war.SideRed},
		{SteamID: 4, Side: war.SideBlu},
	}
	slots := assignSlots(picked, 2, plan)

	sideOf := map[uint64]war.Side{}
	for _, s := range slots {
		sideOf[s.SteamID] = s.Side
	}
	if sideOf[1] != sideOf[2] {
		t.Fatalf("party members 1 and 2 were split: %s vs %s", sideOf[1], sideOf[2])
	}
}

// TestAssignSlotsRespectsCapacityOverParty confirms a party cannot overflow a
// side just because they queued together — teamSize is still the hard limit.
func TestAssignSlotsRespectsCapacityOverParty(t *testing.T) {
	plan := war.BattlePlan{}
	picked := []*Player{
		{SteamID: 1, Side: war.SideRed, PartyID: "trio"},
		{SteamID: 2, Side: war.SideRed, PartyID: "trio"},
		{SteamID: 3, Side: war.SideRed, PartyID: "trio"},
		{SteamID: 4, Side: war.SideBlu},
	}
	slots := assignSlots(picked, 2, plan)
	red, blu := 0, 0
	for _, s := range slots {
		switch s.Side {
		case war.SideRed:
			red++
		case war.SideBlu:
			blu++
		}
	}
	if red != 2 || blu != 2 {
		t.Fatalf("a 3-person party on a team size of 2 must overflow to the other side: got RED %d BLU %d", red, blu)
	}
}
