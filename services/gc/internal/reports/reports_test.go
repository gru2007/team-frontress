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
	if err := os.Remove(dir); err != nil {
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
