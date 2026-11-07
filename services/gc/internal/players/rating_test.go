package players

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRatingIsZeroSum(t *testing.T) {
	ps := []Participant{{SteamID: 1, Team: 0, Result: Win}, {SteamID: 2, Team: 1, Result: Loss}}
	next := RatingUpdates(ps, map[uint64]int{1: 1500, 2: 1500}, map[uint64]int{1: 0, 2: 0})
	if next[1] != 1516 || next[2] != 1484 {
		t.Fatalf("got %v", next)
	}
}

func TestOneSidedMatchChangesNobody(t *testing.T) {
	ps := []Participant{{SteamID: 1, Team: 0, Result: Win}}
	if next := RatingUpdates(ps, map[uint64]int{1: 1500}, nil); next[1] != 1500 {
		t.Fatalf("got %v", next)
	}
}

func TestReplayAndTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "players.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordMatch(7, []Participant{{SteamID: 1, Team: 0, Result: Win}, {SteamID: 2, Team: 1, Result: Loss}})
	s.Abandoned(2, "", 8)
	s.Close()

	// A crash mid-write leaves half a line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"t":"played","id":"1"`)
	f.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Get(1); r.Wins != 1 || r.Rating != 1516 {
		t.Fatalf("alice %+v", r)
	}
	if r := s.Get(2); r.Abandons != 1 || r.Losses != 1 {
		t.Fatalf("bob %+v", r)
	}
	s.RecordMatch(7, []Participant{{SteamID: 1, Team: 0, Result: Win}, {SteamID: 2, Team: 1, Result: Loss}})
	s.Abandoned(2, "", 8)
	if a, b := s.Get(1), s.Get(2); a.Matches != 1 || a.Rating != 1516 || b.Matches != 1 || b.Abandons != 1 {
		t.Fatalf("replayed result or abandon counted twice: %+v / %+v", a, b)
	}
	// And the repaired file takes appends that replay cleanly.
	s.Abandoned(1, "", 9)
	s.Close()
	if s, err = Open(path); err != nil || s.Get(1).Abandons != 1 {
		t.Fatalf("after repair: %v %+v", err, s.Get(1))
	}
}
