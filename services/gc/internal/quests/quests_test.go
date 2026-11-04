package quests

import (
	"testing"
	"time"
)

func TestForIsStableAndVaried(t *testing.T) {
	now := time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)
	a := For(76561198000000001, now)
	b := For(76561198000000001, now.Add(5*time.Hour)) // same day
	if len(a) != DailyCount+1 {
		t.Fatalf("%d quests, want %d", len(a), DailyCount+1)
	}
	for i := range a {
		if a[i].Key != b[i].Key {
			t.Fatalf("quests changed within a day: %s vs %s", a[i].Key, b[i].Key)
		}
	}
	seen := map[string]bool{}
	classes := 0
	for _, q := range a[:DailyCount] {
		if seen[q.ID] {
			t.Fatalf("daily %s twice", q.ID)
		}
		seen[q.ID] = true
		if q.Kind == ClassWin {
			classes++
		}
		if !q.Ends.Equal(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("daily ends %v", q.Ends)
		}
	}
	if classes > 1 {
		t.Fatalf("%d class quests in one day", classes)
	}
	if w := a[DailyCount]; !w.Weekly || !w.Ends.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly %+v", w)
	}

	// Different players and days do not all get the same three.
	differ := false
	for d := 0; d < 7 && !differ; d++ {
		c := For(76561198000000002, now.AddDate(0, 0, d))
		differ = c[0].ID != a[0].ID || c[1].ID != a[1].ID || c[2].ID != a[2].ID
	}
	if !differ {
		t.Fatal("every day hands out the same quests")
	}
}

func TestCredit(t *testing.T) {
	st := Stats{Kills: 12, Damage: 3400, Healing: 0, Won: true, War: true, Classes: ClassesPlayed(1<<3 | 1<<5)}
	cases := []struct {
		def  Def
		want int
	}{
		{Def{Kind: Kills}, 12},
		{Def{Kind: Damage}, 3400},
		{Def{Kind: Matches}, 1},
		{Def{Kind: Wins}, 1},
		{Def{Kind: WarWins}, 1},
		{Def{Kind: ClassWin, Class: "soldier"}, 1},
		{Def{Kind: ClassWin, Class: "medic"}, 1},
		{Def{Kind: ClassWin, Class: "spy"}, 0},
	}
	for _, c := range cases {
		if got := c.def.Credit(st); got != c.want {
			t.Errorf("%s/%s: %d, want %d", c.def.Kind, c.def.Class, got, c.want)
		}
	}
	if (Def{Kind: WarWins}).Credit(Stats{Won: true}) != 0 {
		t.Error("a casual win counted as a war win")
	}
}
