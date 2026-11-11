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

func TestInvitesRanksAndExpiry(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s, _ := Open("")
	s.UseClock(clock(&now))
	c, _ := s.Create(1, "A", Settings{Name: "Alpha", Tag: "ALP"})
	_ = s.Invite(1, 2)
	if err := s.Join(2, "B", c.ID); err != nil {
		t.Fatal(err)
	}
	// A member of a closed clan cannot invite; of an open one can.
	if err := s.Invite(2, 3); err != ErrRank {
		t.Fatalf("member invited into a closed clan: %v", err)
	}
	_ = s.Configure(1, Settings{Open: true})
	if err := s.Invite(2, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.Invite(2, 2); err != ErrSelf {
		t.Fatalf("invited themself: %v", err)
	}
	// Taken back, the invite is gone.
	if err := s.Uninvite(1, 3); err != nil {
		t.Fatal(err)
	}
	if len(s.InvitesFor(3)) != 0 {
		t.Fatal("the invite outlived its taking back")
	}
	// A stale invite neither lists nor opens a closed clan.
	_ = s.Configure(1, Settings{Open: false})
	_ = s.Invite(1, 4)
	now = now.Add(InviteTTL + time.Hour)
	if len(s.InvitesFor(4)) != 0 || len(s.Of(1).Invites) != 0 {
		t.Fatal("a stale invite still lists")
	}
	if err := s.Join(4, "D", c.ID); err != ErrClosed {
		t.Fatalf("a stale invite let them in: %v", err)
	}

	// The table: by clan XP.
	d, _ := s.Create(5, "E", Settings{Name: "Bravo", Tag: "BRV"})
	s.RecordMatch([]Played{{SteamID: 5, Won: true}})
	r := s.Ranked()
	if len(r) != 2 || r[0].ID != d.ID || r[0].WeekXP() != XPPerMatch+XPWinBonus {
		t.Fatalf("ranked %+v", r)
	}
	if p := NextPerk(5); p == nil || p.ID != "roster" {
		t.Fatalf("next perk after 5: %+v", p)
	}
	if NextPerk(MaxLevel) != nil || LevelStart(2) != 600 {
		t.Fatal("level table")
	}
}
