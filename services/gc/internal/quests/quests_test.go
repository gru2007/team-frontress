package quests

import (
	"testing"
	"time"
)

func TestForIsStableAndVaried(t *testing.T) {
	now := time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)
	a := For(76561198000000001, now, nil)
	b := For(76561198000000001, now.Add(5*time.Hour), nil) // same day
	if len(a) != DailyCount+1 {
		t.Fatalf("%d quests, want %d", len(a), DailyCount+1)
	}
	for i := range a {
		if a[i].Key != b[i].Key {
			t.Fatalf("quests changed within a day: %s vs %s", a[i].Key, b[i].Key)
		}
	}
	for d := 0; d < 60; d++ {
		qs := For(uint64(76561198000000001+d), now.AddDate(0, 0, d), nil)
		classes := 0
		for slot, q := range qs[:DailyCount] {
			if q.Tier != slot+1 || q.Slot != slot {
				t.Fatalf("slot %d has tier %d", slot, q.Tier)
			}
			if q.Class != "" {
				classes++
			}
			if q.XP == 0 || q.Reward == "" || q.RewardN == 0 {
				t.Fatalf("%s pays nothing", q.ID)
			}
		}
		if classes > 1 {
			t.Fatalf("%d class quests in one day", classes)
		}
	}
	for _, q := range a[:DailyCount] {
		if !q.Ends.Equal(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("daily ends %v", q.Ends)
		}
	}
	if w := a[DailyCount]; !w.Weekly || w.Reward != RareCrate || !w.Ends.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly %+v", w)
	}

	// Different players and days do not all get the same three.
	differ := false
	for d := 0; d < 7 && !differ; d++ {
		c := For(76561198000000002, now.AddDate(0, 0, d), nil)
		differ = c[0].ID != a[0].ID || c[1].ID != a[1].ID || c[2].ID != a[2].ID
	}
	if !differ {
		t.Fatal("every day hands out the same quests")
	}
}

// A swap changes its own slot to another quest of the same tier, and no
// other slot.
func TestRerollChangesOneSlot(t *testing.T) {
	for d := 0; d < 40; d++ {
		now := time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC).AddDate(0, 0, d)
		id := uint64(76561198000000100 + d)
		before := For(id, now, nil)
		for slot := 0; slot < DailyCount; slot++ {
			marks := map[string]int{RerollKey(Day(now), slot): 1}
			after := For(id, now, marks)
			for i := range before {
				if i == slot {
					if after[i].ID == before[i].ID || after[i].Tier != before[i].Tier {
						t.Fatalf("day %d slot %d: %s -> %s", d, slot, before[i].ID, after[i].ID)
					}
				} else if after[i].Key != before[i].Key {
					t.Fatalf("day %d: swapping slot %d changed slot %d", d, slot, i)
				}
			}
			classes := 0
			for _, q := range after[:DailyCount] {
				if q.Class != "" {
					classes++
				}
			}
			if classes > 1 {
				t.Fatalf("day %d: a swap made two class quests", d)
			}
			if RerollsUsed(marks, Day(now)) != 1 {
				t.Fatal("swaps miscounted")
			}
		}
	}
}

func TestStreak(t *testing.T) {
	now := time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)
	marks := map[string]int{}
	for d := 1; d <= 3; d++ {
		marks[DailyBonusKey(Day(now.AddDate(0, 0, -d)))] = 1
	}
	if s := Streak(marks, now); s != 3 {
		t.Fatalf("streak %d before today, want 3", s)
	}
	marks[DailyBonusKey(Day(now))] = 1
	if s := Streak(marks, now); s != 4 {
		t.Fatalf("streak %d with today, want 4", s)
	}
	if s := Streak(marks, now.AddDate(0, 0, 2)); s != 0 {
		t.Fatalf("a missed day kept the streak: %d", s)
	}
}

func TestCredit(t *testing.T) {
	medals, gold := CountMedals(0, 3, 1, 2, 4)
	st := Stats{Kills: 12, Deaths: 4, Damage: 3400, Won: true, War: true, WithParty: true,
		Medals: medals, GoldMedals: gold, Classes: ClassesPlayed(1<<3 | 1<<5)}
	cases := []struct {
		def  Def
		want int
	}{
		{Def{Kind: Kills}, 12},
		{Def{Kind: Kills, Per: 10}, 1},
		{Def{Kind: Kills, Per: 15}, 0},
		{Def{Kind: Damage}, 3400},
		{Def{Kind: Matches}, 1},
		{Def{Kind: Wins}, 1},
		{Def{Kind: WarWins}, 1},
		{Def{Kind: Medals}, 3},
		{Def{Kind: GoldMedals}, 1},
		{Def{Kind: PartyWins}, 1},
		{Def{Kind: PositiveKD}, 1},
		{Def{Kind: ClanMatches}, 0},
		{Def{Kind: ClassWin, Class: "soldier"}, 1},
		{Def{Kind: ClassPlay, Class: "medic"}, 1},
		{Def{Kind: ClassWin, Class: "spy"}, 0},
	}
	for _, c := range cases {
		if got := c.def.Credit(st); got != c.want {
			t.Errorf("%s/%s per %d: %d, want %d", c.def.Kind, c.def.Class, c.def.Per, got, c.want)
		}
	}
	if (Def{Kind: WarWins}).Credit(Stats{Won: true}) != 0 {
		t.Error("a casual win counted as a war win")
	}
	if (Def{Kind: ClassPlay, Class: "medic"}).Credit(Stats{Classes: []string{"medic"}}) != 1 {
		t.Error("playing a class needs no win")
	}
	if (Def{Kind: PositiveKD}).Credit(Stats{Kills: 2, Deaths: 0}) != 0 {
		t.Error("two kills made a positive match")
	}
}
