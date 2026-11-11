package players

import (
	"path/filepath"
	"testing"
)

// A match moves quests once, even sent again, and replays to the same
// progress, XP and rewards.
func TestCreditQuestsOncePerMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "players.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const id = 76561198000000001
	kills := func(n int) []QuestCredit {
		return []QuestCredit{
			{Key: "d:kills", Add: n, Target: 25, XP: 150, Reward: "crate", RewardN: 2},
			{Key: "d:wins", Add: 0, Target: 2, XP: 200, Reward: "crate", RewardN: 1},
		}
	}
	if o := s.CreditQuests(id, 1, kills(10)); o[0].Total != 10 || o[0].Finished || o[0].Add != 10 {
		t.Fatalf("10/25: %+v", o)
	}
	if o := s.CreditQuests(id, 1, kills(10)); o[0].Total != 10 || o[0].Add != 0 {
		t.Fatalf("the same match counted twice: %+v", o)
	}
	if o := s.CreditQuests(id, 2, kills(30)); o[0].Total != 25 || !o[0].Finished || o[0].Add != 15 {
		t.Fatalf("finishing: %+v", o)
	}
	if o := s.CreditQuests(id, 3, kills(5)); o[0].Total != 25 || o[0].Finished {
		t.Fatalf("after finishing: %+v", o)
	}
	if !s.Pay(id, "daily:x", 50, "rare_crate", 1) || s.Pay(id, "daily:x", 50, "rare_crate", 1) {
		t.Fatal("a bonus paid twice, or not at all")
	}
	r := s.Get(id)
	if r.QuestXP != 200 || r.Rewards["crate"] != 2 || r.Rewards["rare_crate"] != 1 || len(r.RewardLog) != 2 {
		t.Fatalf("paid %+v", r)
	}
	if r.LastQuest == nil || r.LastQuest.MatchID != 3 || len(r.LastQuest.Added) != 0 {
		t.Fatalf("last match %+v", r.LastQuest)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again := s.Get(id)
	if again.Quests["d:kills"] != 25 || again.QuestXP != 200 || again.Rewards["crate"] != 2 || again.Rewards["rare_crate"] != 1 {
		t.Fatalf("after replay: %+v", again)
	}
	// Replayed, the old matches still cannot move anything.
	if o := s.CreditQuests(id, 1, []QuestCredit{{Key: "d:wins", Add: 1, Target: 2}}); o[0].Total != 0 {
		t.Fatalf("a replayed match counted again: %+v", o)
	}
}
