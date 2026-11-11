package gc

import (
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/players"
	"github.com/gru2007/team-frontress/services/gc/internal/quests"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
)

// One swap a day; finishing every daily pays the bonus once; the game sees
// the rewards waiting.
func TestQuestRerollBonusAndRewards(t *testing.T) {
	const id = steamauth.MinIndividual + 7
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	g, err := New(Deps{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	before := g.PlayerQuests(id)
	if before.RerollsLeft != 1 || !before.Daily[1].CanReroll || before.Rewards.Total != 0 {
		t.Fatalf("fresh %+v", before)
	}
	g.rerollQuest(id, 1)
	after := g.PlayerQuests(id)
	if after.Daily[1].Key == before.Daily[1].Key || after.Daily[0].Key != before.Daily[0].Key || after.RerollsLeft != 0 {
		t.Fatalf("reroll: %s -> %s, left %d", before.Daily[1].Key, after.Daily[1].Key, after.RerollsLeft)
	}
	g.rerollQuest(id, 2)
	if g.PlayerQuests(id).Daily[2].Key != after.Daily[2].Key {
		t.Fatal("a second swap went through")
	}

	// Finish every daily in one match.
	var credits []players.QuestCredit
	for _, q := range after.Daily {
		credits = append(credits, players.QuestCredit{Key: q.Key, Add: q.Target, Target: q.Target, XP: q.XP, Reward: q.Reward, RewardN: q.RewardN})
	}
	g.players.CreditQuests(id, 55, credits)
	g.payDailyBonus(id, now)
	g.payDailyBonus(id, now)
	got := g.PlayerQuests(id)
	want := quests.DailyBonusRewardN
	for _, q := range got.Daily {
		if !q.Done || q.Last != q.Target || q.CanReroll {
			t.Fatalf("daily %+v", q)
		}
		want += q.RewardN
	}
	if !got.Bonus.Paid || got.Bonus.Streak != 1 || got.Rewards.Counts[quests.Crate] != want || got.Rewards.Total != want {
		t.Fatalf("bonus %+v rewards %+v (want %d crates)", got.Bonus, got.Rewards, want)
	}
	if len(got.Rewards.Recent) != 4 || got.Rewards.Recent[0].Src != "daily_bonus" ||
		got.Rewards.Recent[1].Src != "daily" || got.Rewards.Recent[1].Quest == "" {
		t.Fatalf("recent %+v", got.Rewards.Recent)
	}
}
