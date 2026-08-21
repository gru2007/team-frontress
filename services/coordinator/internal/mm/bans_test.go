package mm

import (
	"errors"
	"testing"
	"time"

	"github.com/greyline-frontress/coordinator/internal/hostelect"
	"github.com/greyline-frontress/coordinator/internal/war"
)

// fakeBanner is the ban list as the matchmaker sees it, without a file.
type fakeBanner struct {
	issued []issuedBan
	barred map[uint64]bool
}

type issuedBan struct {
	steamID uint64
	source  string
	reason  string
	d       time.Duration
}

func newFakeBanner() *fakeBanner {
	return &fakeBanner{barred: map[uint64]bool{}}
}

func (f *fakeBanner) Issue(steamID uint64, source, reason string, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	f.issued = append(f.issued, issuedBan{steamID, source, reason, d})
	f.barred[steamID] = true
	return nil
}

func (f *fakeBanner) Banned(steamID uint64) bool { return f.barred[steamID] }

func TestDeployIsRefusedForABannedAccount(t *testing.T) {
	m, _ := newTestMaker(t)
	banner := newFakeBanner()
	m.SetBanner(banner)
	banner.barred[1] = true

	p := m.Hello(1, "merc", war.SideRed, "", hostelect.Capabilities{})
	_, err := m.Deploy(p, "", true, "")
	if !errors.Is(err, ErrBanned) {
		t.Fatalf("deploy while banned: err = %v, want ErrBanned", err)
	}
	if p.State == StateQueued {
		t.Fatal("a banned player was put in the queue anyway")
	}
}

// Walking out of a battle people are playing is the one thing that costs
// something.
func TestLeavingALiveBattleEarnsABan(t *testing.T) {
	m, servers := newTestMaker(t)
	banner := newFakeBanner()
	m.SetBanner(banner)
	m.cfg.AbandonBan = 10 * time.Minute

	match, _, _ := runningBattle(t, m, servers)
	quitter := m.players[1]
	if quitter.State != StatePlaying {
		t.Fatalf("player 1 is %s, expected to be playing in %s", quitter.State, match.ID)
	}

	m.Leave(quitter, "alt+f4")

	if len(banner.issued) != 1 {
		t.Fatalf("issued %d bans, want 1: %+v", len(banner.issued), banner.issued)
	}
	got := banner.issued[0]
	if got.steamID != 1 || got.source != "abandon" || got.d != 10*time.Minute {
		t.Fatalf("wrong ban: %+v", got)
	}
}

// The rule is off by default, and off means nothing is recorded at all —
// not a permanent ban, which is what a zero duration would mean if it were
// passed straight through.
func TestAbandonBanIsOffByDefault(t *testing.T) {
	m, servers := newTestMaker(t)
	banner := newFakeBanner()
	m.SetBanner(banner)

	runningBattle(t, m, servers)
	m.Leave(m.players[1], "alt+f4")

	if len(banner.issued) != 0 {
		t.Fatalf("a ban was issued with the rule switched off: %+v", banner.issued)
	}
}

// Leaving a queue is free. The coordinator cannot tell an impatient player
// from one whose host never came up, and punishing the second is worse than
// letting the first go.
func TestLeavingAQueueEarnsNothing(t *testing.T) {
	m, _ := newTestMaker(t)
	banner := newFakeBanner()
	m.SetBanner(banner)
	m.cfg.AbandonBan = 10 * time.Minute

	p := deploy(t, m, 1, war.SideRed, true)
	m.Leave(p, "changed my mind")

	if len(banner.issued) != 0 {
		t.Fatalf("leaving a queue was punished: %+v", banner.issued)
	}
	if p.State != StateIdle {
		t.Fatalf("player state = %s, want idle", p.State)
	}
}

// A battle that formed but never went live is not one anybody was playing.
func TestLeavingABattleThatNeverStartedEarnsNothing(t *testing.T) {
	m, _ := newTestMaker(t)
	banner := newFakeBanner()
	m.SetBanner(banner)
	m.cfg.AbandonBan = 10 * time.Minute

	deploy(t, m, 1, war.SideRed, true)
	deploy(t, m, 2, war.SideBlu, true)
	deploy(t, m, 3, war.SideRed, true)
	deploy(t, m, 4, war.SideBlu, true)
	m.Tick()

	p := m.players[1]
	if p.State == StatePlaying {
		t.Fatal("no server was registered, so nothing should be live")
	}
	m.Leave(p, "waited too long")

	if len(banner.issued) != 0 {
		t.Fatalf("leaving a battle that never started was punished: %+v", banner.issued)
	}
}

// Kick is the enforcement half: the session ends, so the client's next call is
// a 401 and it goes back through hello, where the ban is waiting.
func TestKickEndsTheSession(t *testing.T) {
	m, _ := newTestMaker(t)
	p := deploy(t, m, 1, war.SideRed, true)
	token := p.Token()

	if !m.Kick(1, "you are banned from the war: cheating") {
		t.Fatal("Kick should report that it ended a session")
	}
	if _, ok := m.Authenticate(token); ok {
		t.Fatal("the token still works after a kick")
	}
	// Told before the token went, so a parked long-poll still gets the reason.
	events := p.drain(0)
	if len(events) == 0 || events[len(events)-1].Type != EventNotice {
		t.Fatalf("the kicked player was not told why: %+v", events)
	}

	if m.Kick(1, "again") {
		t.Fatal("there was no second session to end")
	}
}

func TestKickOnSomebodyWhoIsNotOnlineIsHarmless(t *testing.T) {
	m, _ := newTestMaker(t)
	if m.Kick(999, "banned") {
		t.Fatal("Kick should report that there was no session")
	}
}

// A matchmaker with no ban list at all must behave exactly as it did before
// bans existed, since that is what every test and every dev coordinator does.
func TestNoBannerMeansNoBans(t *testing.T) {
	m, servers := newTestMaker(t)
	m.cfg.AbandonBan = 10 * time.Minute

	runningBattle(t, m, servers)
	m.Leave(m.players[1], "alt+f4") // must not panic on a nil Banner

	p := m.Hello(9, "merc", war.SideRed, "", hostelect.Capabilities{})
	if _, err := m.Deploy(p, "", true, ""); err != nil {
		t.Fatalf("deploy with no ban list: %v", err)
	}
}
