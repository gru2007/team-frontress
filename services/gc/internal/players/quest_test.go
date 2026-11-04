package players

import (
	"path/filepath"
	"testing"
)

func TestQuestProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "players.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const id = 76561198000000001
	if total, done := s.QuestProgress(id, "d:kills", 10, 25, 150); total != 10 || done {
		t.Fatalf("10/25: %d %v", total, done)
	}
	if total, done := s.QuestProgress(id, "d:kills", 30, 25, 150); total != 25 || !done {
		t.Fatalf("finishing: %d %v", total, done)
	}
	if total, done := s.QuestProgress(id, "d:kills", 5, 25, 150); total != 25 || done {
		t.Fatalf("after finishing: %d %v", total, done)
	}
	before := s.Get(id).XP()
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Get(id)
	if r.Quests["d:kills"] != 25 || r.QuestXP != 150 || r.XP() != before {
		t.Fatalf("after replay: %+v xp %d (was %d)", r, r.XP(), before)
	}
}
