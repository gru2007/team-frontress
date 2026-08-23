package players

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

func TestRecordsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "players.jsonl")
	p := wire.AssignedPlayer{SteamID: "76561198000000001", Name: "gru"}

	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	first.Played(p, "m1", "win")
	first.Played(p, "m2", "loss")
	first.Abandoned(p, "m3")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// This is the whole point of the file: a cooldown that a deploy clears is
	// not a cooldown.
	second, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	got := second.Get(p.SteamID)
	if got.Matches != 2 || got.Wins != 1 || got.Losses != 1 {
		t.Errorf("after replay: %+v, want 2 matches, 1 win, 1 loss", got)
	}
	if got.Abandons != 1 || got.LastAbandon.IsZero() {
		t.Errorf("after replay: %+v, want the abandon and when it happened", got)
	}
	if got.Name != "gru" {
		t.Errorf("name = %q, want the last name we saw", got.Name)
	}
}

func TestAnUnknownPlayerIsAZeroRecord(t *testing.T) {
	s, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get("76561198000000009")
	if got.Matches != 0 || got.Abandons != 0 || !got.LastAbandon.IsZero() {
		t.Errorf("%+v, want a first-time player to look like one", got)
	}
}

func TestTheLogIsAppendOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "players.jsonl")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	s.UseClock(func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) })
	for i := 0; i < 5; i++ {
		s.Played(wire.AssignedPlayer{SteamID: "76561198000000001"}, "m", "draw")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Played(wire.AssignedPlayer{SteamID: "76561198000000001"}, "m", "draw")
	if got := reopened.Get("76561198000000001"); got.Matches != 6 {
		t.Fatalf("matches = %d, want 6: reopening the log truncated it", got.Matches)
	}
}
