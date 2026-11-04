package clans

import (
	"path/filepath"
	"testing"
	"time"
)

func clock(t *time.Time) func() time.Time { return func() time.Time { return *t } }

func TestClanLifecycleReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clans.jsonl")
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.UseClock(clock(&now))

	c, err := s.Create(1, "Founder", Settings{Name: "Ржавые Ключи", Tag: "rk", Emblem: "engineer", Color: "gold"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Tag != "RK" || c.Emblem != "engineer" || c.Open {
		t.Fatalf("created %+v", c)
	}
	if _, err := s.Create(2, "Other", Settings{Name: "ржавые ключи", Tag: "XX"}); err != ErrTaken {
		t.Fatalf("duplicate name: %v", err)
	}
	if err := s.Join(2, "Two", c.ID); err != ErrClosed {
		t.Fatalf("closed clan let a stranger in: %v", err)
	}
	if err := s.Invite(1, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Join(2, "Two", c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Kick(2, 1); err != ErrRank {
		t.Fatalf("a member kicked the leader: %v", err)
	}
	if err := s.SetRole(1, 2, Officer); err != nil {
		t.Fatal(err)
	}
	// The leader leaves: the officer takes over.
	if err := s.Leave(1); err != nil {
		t.Fatal(err)
	}
	if got := s.Of(2); got == nil || got.Members[2].Role != Leader {
		t.Fatalf("no heir: %+v", got)
	}
	s.Close()

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again.UseClock(clock(&now))
	got := again.Of(2)
	if got == nil || got.Name != "Ржавые Ключи" || len(got.Members) != 1 || got.Members[2].Role != Leader {
		t.Fatalf("replayed %+v", got)
	}
	if again.Of(1) != nil {
		t.Fatal("the leaver is still in")
	}
}

func TestPlayingTogetherLevelsTheClanAndUnlocksPerks(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s, _ := Open("")
	s.UseClock(clock(&now))
	c, _ := s.Create(1, "A", Settings{Name: "Alpha", Tag: "ALP", Open: true})
	if err := s.Join(2, "B", c.ID); err != nil {
		t.Fatal(err)
	}

	// Alone: base XP, no bonus.
	got := s.RecordMatch([]Played{{SteamID: 1, Won: true}})
	if got[1].ClanXP != XPPerMatch+XPWinBonus || got[1].BonusPct != 0 {
		t.Fatalf("alone: %+v", got[1])
	}
	// Together: more clan XP; the brothers bonus once the clan is level 2.
	got = s.RecordMatch([]Played{{SteamID: 1, Won: true}, {SteamID: 2, Won: true}})
	if got[1].ClanXP != (XPPerMatch+XPWinBonus)*3/2 {
		t.Fatalf("together: %+v", got[1])
	}
	for s.Of(1).Level() < 2 {
		s.RecordMatch([]Played{{SteamID: 1, Won: true, War: true, Sector: "junction"}, {SteamID: 2}})
	}
	got = s.RecordMatch([]Played{{SteamID: 1}, {SteamID: 2}})
	if got[1].BonusPct != BrothersBonus {
		t.Fatalf("level 2 together: %+v", got[1])
	}
	if sector, n := s.Of(1).Banner(); sector != "junction" || n == 0 {
		t.Fatalf("banner %q %d", sector, n)
	}
	// The banner shows on the map only from level 3.
	for s.Of(1).Level() < 3 {
		s.RecordMatch([]Played{{SteamID: 1}, {SteamID: 2}})
	}
	if b := s.Banners()["junction"]; b == nil || b.ID != c.ID {
		t.Fatalf("banners %+v", s.Banners())
	}
	if err := s.SetOrder(1, "sawmill"); err != ErrPerk {
		t.Fatalf("order before level 5: %v", err)
	}
}

func TestWeeklyContractPaysOnce(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s, _ := Open("")
	s.UseClock(clock(&now))
	c, _ := s.Create(1, "A", Settings{Name: "Bravo", Tag: "BRV", Open: true})
	s.Join(2, "B", c.ID)
	for s.Of(1).Level() < 4 {
		s.RecordMatch([]Played{{SteamID: 1}})
	}
	// Move to a fresh week so the contract starts from zero.
	now = WeekEnds(Week(now)).Add(time.Hour)
	ct := ContractFor(Week(now))
	paid := 0
	for i := 0; i < ct.Target*3; i++ {
		got := s.RecordMatch([]Played{{SteamID: 1, Won: true, War: true}, {SteamID: 2, Won: true, War: true}})
		if got[1].Contract || got[2].Contract {
			paid++
		}
	}
	if paid != 1 {
		t.Fatalf("contract %+v paid %d times", ct, paid)
	}
	if who := s.ContractPaidTo(c.ID); len(who) != 2 {
		t.Fatalf("paid to %v", who)
	}
}

func TestNamesAndTags(t *testing.T) {
	for _, bad := range []string{"ab", "this name is far too long for a clan", "bad<name>"} {
		if _, err := CleanName(bad); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	for _, bad := range []string{"a", "TOOLONG", "a-b"} {
		if _, err := CleanTag(bad); err == nil {
			t.Errorf("tag %q accepted", bad)
		}
	}
	if tag, err := CleanTag(" рк2 "); err != nil || tag != "РК2" {
		t.Errorf("tag %q %v", tag, err)
	}
}
