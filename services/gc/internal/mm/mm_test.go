package mm

import (
	"testing"
	"time"
)

func entry(id uint64, at time.Time, ratings ...int) Entry {
	e := Entry{PartyID: id, QueuedAt: at}
	for i, r := range ratings {
		e.Players = append(e.Players, Player{SteamID: id*10 + uint64(i), Rating: r})
	}
	return e
}

func TestPartiesAreNeverSplit(t *testing.T) {
	now := time.Now()
	q := []Entry{entry(1, now, 1500, 1500, 1500), entry(2, now, 1500), entry(3, now, 1500, 1500)}
	f, ok := Form(q, Sizes{Min: 2, Ideal: 6, Max: 6}, 0, now)
	if !ok || f.Seats() != 6 || Count(f.Red) != 3 || Count(f.Blu) != 3 {
		t.Fatalf("got %+v ok=%v", f, ok)
	}
}

func TestUnevenQueueWaits(t *testing.T) {
	now := time.Now()
	q := []Entry{entry(1, now, 1500, 1500), entry(2, now, 1500)}
	if f, ok := Form(q, Sizes{Min: 2, Ideal: 2, Max: 4}, 0, now); ok {
		t.Fatalf("formed %+v from 2+1", f)
	}
}

func TestPatienceSettlesForMinimum(t *testing.T) {
	now := time.Now()
	q := []Entry{entry(1, now.Add(-30*time.Second), 1500), entry(2, now, 1500)}
	sizes := Sizes{Min: 2, Ideal: 12, Max: 24}
	if _, ok := Form(q, sizes, time.Minute, now); ok {
		t.Fatal("did not wait for the ideal size")
	}
	if _, ok := Form(q, sizes, 20*time.Second, now); !ok {
		t.Fatal("did not settle after patience ran out")
	}
}

func TestSplitBalancesRating(t *testing.T) {
	es := []Entry{entry(1, time.Time{}, 2000), entry(2, time.Time{}, 2000), entry(3, time.Time{}, 1000), entry(4, time.Time{}, 1000)}
	red, blu, ok := SplitTeams(es)
	if !ok {
		t.Fatal("no split")
	}
	if red[0].rating()+red[1].rating() != blu[0].rating()+blu[1].rating() {
		t.Fatalf("unbalanced: %+v vs %+v", red, blu)
	}
}

func TestRoomForRespectsCap(t *testing.T) {
	if team, ok := RoomFor(3, 2, 1, 3); !ok || team != Blu {
		t.Fatalf("got %v %v", team, ok)
	}
	if _, ok := RoomFor(3, 3, 1, 3); ok {
		t.Fatal("seated past the cap")
	}
	if _, ok := RoomFor(1, 2, 3, 3); ok {
		t.Fatal("seated a party that does not fit either side")
	}
}

func TestChooseMapPrefersCommonVoteThenOldest(t *testing.T) {
	es := []Entry{{Maps: []string{"a", "b"}}, {Maps: []string{"b", "c"}}}
	if m := ChooseMap([]string{"a", "b", "c"}, es, func(string) time.Time { return time.Time{} }); m != "b" {
		t.Fatalf("got %q", m)
	}
	played := map[string]time.Time{"a": time.Now()}
	if m := ChooseMap([]string{"a", "z"}, nil, func(s string) time.Time { return played[s] }); m != "z" {
		t.Fatalf("got %q", m)
	}
}
