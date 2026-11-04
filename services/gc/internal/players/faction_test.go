package players

import (
	"path/filepath"
	"testing"
)

func TestFactionLockedAndDurablePerCampaign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "players.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChooseWarSide(1, "war-1", "RED"); err != nil {
		t.Fatal(err)
	}
	if err = s.ChooseWarSide(1, "war-1", "BLU"); err == nil {
		t.Fatal("changed faction during campaign")
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if r := s.Get(1); r.WarSide != "RED" || r.WarCampaign != "war-1" {
		t.Fatal("lost faction", r)
	}
	if err = s.ChooseWarSide(1, "war-2", "BLU"); err != nil {
		t.Fatal("new campaign should unlock choice", err)
	}
}
