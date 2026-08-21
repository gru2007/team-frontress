package bans

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBanAndCheck(t *testing.T) {
	l, err := Open("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := l.Ban(Ban{SteamID: 7, Reason: "aimbot", Source: SourceManual}); err != nil {
		t.Fatalf("ban: %v", err)
	}
	b, ok := l.Check(7)
	if !ok {
		t.Fatal("account 7 should be banned")
	}
	if !b.Permanent() {
		t.Fatal("a ban with no expiry should be permanent")
	}
	if l.Banned(8) {
		t.Fatal("account 8 was never banned")
	}
}

func TestBanNeedsSteamID(t *testing.T) {
	l, _ := Open("")
	if _, err := l.Ban(Ban{Reason: "nobody"}); err == nil {
		t.Fatal("a ban with no SteamID should be refused")
	}
}

func TestTemporaryBanExpires(t *testing.T) {
	l, _ := Open("")
	now := time.Now()
	l.now = func() time.Time { return now }

	if err := l.Issue(7, string(SourceAbandon), "walked out", 10*time.Minute); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !l.Banned(7) {
		t.Fatal("the ban should be in force straight away")
	}

	now = now.Add(11 * time.Minute)
	if l.Banned(7) {
		t.Fatal("the ban should have expired")
	}
	if got := len(l.Active()); got != 0 {
		t.Fatalf("expired ban still listed: %d active", got)
	}
}

// A ten minute abandon ban landing on an account an operator banned for good
// must not turn that into ten minutes.
func TestShorterBanDoesNotOverrideLonger(t *testing.T) {
	l, _ := Open("")
	if _, err := l.Ban(Ban{SteamID: 7, Reason: "cheating", Source: SourceManual}); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if err := l.Issue(7, string(SourceAbandon), "walked out", time.Minute); err != nil {
		t.Fatalf("issue: %v", err)
	}
	b, ok := l.Check(7)
	if !ok {
		t.Fatal("still banned")
	}
	if !b.Permanent() || b.Reason != "cheating" {
		t.Fatalf("the permanent ban was overwritten: %+v", b)
	}
}

func TestLongerBanReplacesShorter(t *testing.T) {
	l, _ := Open("")
	if err := l.Issue(7, string(SourceAbandon), "walked out", time.Minute); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := l.Ban(Ban{SteamID: 7, Reason: "cheating", Source: SourceManual}); err != nil {
		t.Fatalf("ban: %v", err)
	}
	b, _ := l.Check(7)
	if !b.Permanent() {
		t.Fatalf("the longer ban should win: %+v", b)
	}
}

func TestIssueWithNoDurationDoesNothing(t *testing.T) {
	l, _ := Open("")
	if err := l.Issue(7, string(SourceAbandon), "walked out", 0); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if l.Banned(7) {
		t.Fatal("a zero duration means the rule is off, not a permanent ban")
	}
}

func TestLift(t *testing.T) {
	l, _ := Open("")
	l.Ban(Ban{SteamID: 7, Reason: "cheating"})

	lifted, err := l.Lift(7, "admin", "appealed")
	if err != nil {
		t.Fatalf("lift: %v", err)
	}
	if !lifted {
		t.Fatal("lift should report that it removed a ban")
	}
	if l.Banned(7) {
		t.Fatal("still banned after a lift")
	}

	lifted, _ = l.Lift(7, "admin", "again")
	if lifted {
		t.Fatal("lifting an unbanned account should report nothing was lifted")
	}
}

// The whole point of the file: a restart has to come back holding the same
// bans, including the lifts.
func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bans.jsonl")

	l, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	l.Ban(Ban{SteamID: 7, Reason: "cheating", Source: SourceManual, IssuedBy: "admin"})
	l.Ban(Ban{SteamID: 8, Reason: "spam", Source: SourceManual})
	if _, err := l.Lift(8, "admin", "warned instead"); err != nil {
		t.Fatalf("lift: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()

	b, ok := again.Check(7)
	if !ok {
		t.Fatal("the ban did not survive the restart")
	}
	if b.Reason != "cheating" || b.IssuedBy != "admin" || b.Source != SourceManual {
		t.Fatalf("the ban came back changed: %+v", b)
	}
	if again.Banned(8) {
		t.Fatal("a lifted ban came back")
	}
}

func TestExpiredBanIsNotRestoredByAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bans.jsonl")
	l, _ := Open(path)
	l.Ban(Ban{SteamID: 7, Reason: "walked out", Source: SourceAbandon,
		ExpiresAt: time.Now().Add(-time.Minute)})
	l.Close()

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	if again.Banned(7) {
		t.Fatal("an expired ban should not come back in force")
	}
}

func TestDescribe(t *testing.T) {
	now := time.Now()
	perm := Ban{SteamID: 7, Reason: "cheating"}
	if got := perm.Describe(now); got == "" || got == "you are banned from the war: " {
		t.Fatalf("unhelpful description: %q", got)
	}
	temp := Ban{SteamID: 7, Reason: "walked out", ExpiresAt: now.Add(10 * time.Minute)}
	if got := temp.Describe(now); got == perm.Describe(now) {
		t.Fatal("a temporary ban should not read like a permanent one")
	}
}
