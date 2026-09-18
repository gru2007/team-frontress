package players

import "testing"

func TestProgressionIsArithmeticAndCannotGoNegative(t *testing.T) {
	fresh := Record{}
	if fresh.XP() != 0 || fresh.Level() != 1 {
		t.Fatalf("a new player is xp=%d level=%d, want 0 and 1", fresh.XP(), fresh.Level())
	}

	played := Record{Matches: 10, Wins: 4}
	if got, want := played.XP(), 10*XPPerMatch+4*XPWinBonus; got != want {
		t.Fatalf("xp = %d, want %d", got, want)
	}

	// A player who has only ever walked out is at zero, not below it: a
	// negative total would wrap or read as an enormous number wherever it is
	// displayed unsigned, and there is nothing to gain by tracking debt.
	quitter := Record{Abandons: 5}
	if quitter.XP() != 0 {
		t.Fatalf("xp = %d, want 0", quitter.XP())
	}
	if quitter.Level() != 1 {
		t.Fatalf("level = %d, want 1", quitter.Level())
	}
}

func TestLevelCaps(t *testing.T) {
	huge := Record{Matches: 100000}
	if huge.Level() != MaxLevel {
		t.Fatalf("level = %d, want the cap %d", huge.Level(), MaxLevel)
	}
	into, needed := huge.LevelProgress()
	if into != needed {
		t.Fatalf("capped level shows %d/%d, want a full bar", into, needed)
	}
}

func TestLevelProgressTracksTheLevel(t *testing.T) {
	// Enough for level 2 with a bit over.
	r := Record{Matches: 12} // 1200 xp
	if r.Level() != 2 {
		t.Fatalf("level = %d, want 2", r.Level())
	}
	into, needed := r.LevelProgress()
	if into != 200 || needed != XPPerLevel {
		t.Fatalf("progress = %d/%d, want 200/%d", into, needed, XPPerLevel)
	}
}
