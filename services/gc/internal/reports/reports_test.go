package reports

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReportsSurviveRestartAndLimitRepeats(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := Report{Reporter: "76561197960265729", Target: "76561197960265730", Reason: 1, At: time.Now()}
	if err := s.Add(r); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatal("report was not saved")
	}
	info, _ := os.Stat(files[0])
	if info.Mode().Perm() != 0o600 {
		t.Fatal("report permissions are not private")
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(r); !errors.Is(err, ErrCooldown) {
		t.Fatalf("restart lost cooldown: %v", err)
	}
	r.At = r.At.Add(5 * time.Minute)
	if err := s.Add(r); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		r.Target += "1"
		if err := s.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	r.Target += "1"
	if err := s.Add(r); !errors.Is(err, ErrCooldown) {
		t.Fatalf("burst limit was not applied: %v", err)
	}
}

func TestFailedWriteCanBeRetried(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reports")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	r := Report{Reporter: "1", Target: "2", Reason: 1, At: time.Now()}
	if err := s.Add(r); err == nil {
		t.Fatal("write into missing directory succeeded")
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(r); err != nil {
		t.Fatalf("failed write consumed allowance: %v", err)
	}
}

func TestCasesStandingAndFeedbackSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const cheater, a, b = "76561197960265740", "76561197960265741", "76561197960265742"
	for i, rep := range []string{a, b} {
		r := Report{Reporter: rep, Target: cheater, Reason: ReasonCheating, Detail: DetailAimbot, At: now.Add(time.Duration(i) * time.Second)}
		if err := s.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(s.OpenReports(cheater)); got != 2 {
		t.Fatalf("open reports: %d", got)
	}
	reporters, err := s.Resolve(cheater, OutcomeActioned, "admin:1", "aimbot on demo", now.Add(time.Hour))
	if err != nil || len(reporters) != 2 {
		t.Fatalf("resolve: %v %v", reporters, err)
	}
	if _, err := s.Resolve(cheater, OutcomeDismissed, "admin:1", "", now.Add(2*time.Hour)); !errors.Is(err, ErrNothingOpen) {
		t.Fatalf("closed an empty case: %v", err)
	}
	if _, err := s.Resolve("../x", OutcomeDismissed, "admin:1", "", now); err == nil {
		t.Fatal("a case outside the store was accepted")
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.OpenReports(cheater)) != 0 || len(s.About(cheater)) != 2 || len(s.Resolutions(cheater)) != 1 {
		t.Fatal("restart lost the closed case")
	}
	if st := s.Standing(a); st.Actioned != 1 || st.Feedback != 1 || st.Weight() <= 1 {
		t.Fatalf("standing after action: %+v weight %.2f", st, st.Weight())
	}
	if n, err := s.TakeFeedback(a); n != 1 || err != nil {
		t.Fatalf("feedback: %d %v", n, err)
	}
	if n, _ := s.TakeFeedback(a); n != 0 {
		t.Fatal("feedback was delivered twice")
	}

	// A later report opens the next case; dismissing it costs weight.
	if err := s.Add(Report{Reporter: b, Target: cheater, Reason: ReasonGriefing, At: now.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if len(s.OpenReports(cheater)) != 1 {
		t.Fatal("a new report did not reopen the case")
	}
	if _, err := s.Resolve(cheater, OutcomeDismissed, "admin:1", "no evidence", now.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if st := s.Standing(b); st.Actioned != 1 || st.Dismissed != 1 || st.Weight() != 1 {
		t.Fatalf("standing after a dismissal: %+v weight %.2f", st, st.Weight())
	}
	if w := (Standing{Dismissed: 20}).Weight(); w != 0.3 {
		t.Fatalf("floor: %.2f", w)
	}
}
