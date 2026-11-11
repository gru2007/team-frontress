package gc

import (
	"sort"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/players"
	"github.com/gru2007/team-frontress/services/gc/internal/quests"
	"github.com/gru2007/team-frontress/services/gc/internal/sink"
)

// QuestView is one of a player's quests as the game's quest screens read it.
type QuestView struct {
	quests.Instance
	Progress int  `json:"progress"`
	Done     bool `json:"done"`
	// Last is what the player's latest match added to it.
	Last int `json:"last"`
	// CanReroll: the player may swap it for another of its tier now.
	CanReroll bool `json:"can_reroll"`
}

// QuestBonus is what finishing every daily quest pays, and the streak.
type QuestBonus struct {
	XP      int    `json:"xp"`
	Reward  string `json:"reward"`
	RewardN int    `json:"reward_n"`
	Paid    bool   `json:"paid"` // today's is earned
	Streak  int    `json:"streak"`
	// StreakEvery days in a row pay StreakReward; StreakNext is how many
	// more days that takes.
	StreakEvery  int    `json:"streak_every"`
	StreakNext   int    `json:"streak_next"`
	StreakReward string `json:"streak_reward"`
}

// QuestRewards is the rewards a player earned and has not claimed yet.
type QuestRewards struct {
	Counts map[string]int    `json:"counts"`
	Total  int               `json:"total"`
	Recent []QuestRewardView `json:"recent"` // newest first
}

// QuestRewardView is one reward earned, as the game lists it.
type QuestRewardView struct {
	Kind  string `json:"kind"`
	N     int    `json:"n"`
	Src   string `json:"src"`   // daily, weekly, daily_bonus, streak
	Quest string `json:"quest"` // the quest's id, for daily and weekly
	Ago   int    `json:"ago"`   // seconds
}

func rewardView(e players.RewardEntry, now time.Time) QuestRewardView {
	v := QuestRewardView{Kind: e.Kind, N: e.N, Ago: max(0, int(now.Sub(e.At).Seconds()))}
	switch {
	case strings.HasPrefix(e.Source, "daily:"):
		v.Src = "daily_bonus"
	case strings.HasPrefix(e.Source, "streak:"):
		v.Src = "streak"
	case strings.HasPrefix(e.Source, "w"):
		v.Src = "weekly"
	default:
		v.Src = "daily"
	}
	if i := strings.IndexByte(e.Source, ':'); i >= 0 && (v.Src == "daily" || v.Src == "weekly") {
		v.Quest = e.Source[i+1:]
	}
	return v
}

// Quests is everything the game shows about a player's quests.
type Quests struct {
	Daily       []QuestView  `json:"daily"`
	Weekly      []QuestView  `json:"weekly"`
	DailyEnds   time.Time    `json:"daily_ends"`
	WeeklyEnds  time.Time    `json:"weekly_ends"`
	DailyLeft   int          `json:"daily_left"`  // seconds
	WeeklyLeft  int          `json:"weekly_left"` // seconds
	CompletedXP int          `json:"completed_xp"`
	RerollsLeft int          `json:"rerolls_left"`
	Bonus       QuestBonus   `json:"bonus"`
	Rewards     QuestRewards `json:"rewards"`
	// LastMatch is when the latest match that counted for quests ended.
	LastMatch time.Time `json:"last_match,omitzero"`
	// Now is the coordinator's clock; Stamp the same in milliseconds, so
	// the game keeps the newest of a pushed copy and a polled one.
	Now   time.Time `json:"now"`
	Stamp int64     `json:"stamp"`
}

// PlayerQuests is a player's quests right now. Safe from any goroutine: it
// reads the player store, which has its own lock.
func (g *GC) PlayerQuests(id uint64) Quests {
	now := g.now()
	rec := g.players.Get(id)
	day := quests.Day(now)
	out := Quests{Daily: []QuestView{}, Weekly: []QuestView{}, CompletedXP: rec.QuestXP, Now: now,
		Stamp: now.UnixMilli()}
	out.RerollsLeft = max(0, quests.RerollsPerDay-quests.RerollsUsed(rec.Quests, day))
	var last map[string]int
	if rec.LastQuest != nil {
		last, out.LastMatch = rec.LastQuest.Added, rec.LastQuest.At
	}
	for _, in := range quests.For(id, now, rec.Quests) {
		v := QuestView{Instance: in, Progress: rec.Quests[in.Key], Last: last[in.Key]}
		v.Done = v.Progress >= in.Target
		if in.Weekly {
			out.Weekly = append(out.Weekly, v)
			out.WeeklyEnds = in.Ends
		} else {
			v.CanReroll = !v.Done && out.RerollsLeft > 0
			out.Daily = append(out.Daily, v)
			out.DailyEnds = in.Ends
		}
	}
	out.DailyLeft = int(out.DailyEnds.Sub(now).Seconds())
	out.WeeklyLeft = int(out.WeeklyEnds.Sub(now).Seconds())

	streak := quests.Streak(rec.Quests, now)
	out.Bonus = QuestBonus{
		XP: quests.DailyBonusXP, Reward: quests.DailyBonusReward, RewardN: quests.DailyBonusRewardN,
		Paid: rec.Quests[quests.DailyBonusKey(day)] > 0, Streak: streak,
		StreakEvery: quests.StreakDays, StreakReward: quests.RareCrate,
	}
	// Days to the next streak reward, counting today if it is still open.
	into := streak % quests.StreakDays
	out.Bonus.StreakNext = quests.StreakDays - into
	if out.Bonus.Paid && into == 0 && streak > 0 {
		out.Bonus.StreakNext = quests.StreakDays
	}

	out.Rewards = QuestRewards{Counts: map[string]int{}, Recent: []QuestRewardView{}}
	for k, n := range rec.Rewards {
		if n > 0 {
			out.Rewards.Counts[k] = n
			out.Rewards.Total += n
		}
	}
	for i := len(rec.RewardLog) - 1; i >= 0 && len(out.Rewards.Recent) < 8; i-- {
		out.Rewards.Recent = append(out.Rewards.Recent, rewardView(rec.RewardLog[i], now))
	}
	return out
}

// sendQuests pushes a player their quests, so the menu does not wait for
// its next poll.
func (g *GC) sendQuests(id uint64) {
	if !g.isOnline(id) {
		return
	}
	g.emit(id, "quests", map[string]any{"quests": g.PlayerQuests(id)})
}

// rerollQuest swaps one of today's daily quests for another of its tier.
func (g *GC) rerollQuest(id uint64, slot int) {
	now := g.now()
	day := quests.Day(now)
	rec := g.players.Get(id)
	if slot < 0 || slot >= quests.DailyCount {
		g.fail(id, "quest_slot")
		return
	}
	if quests.RerollsUsed(rec.Quests, day) >= quests.RerollsPerDay {
		g.fail(id, "quest_rerolls")
		return
	}
	in := quests.For(id, now, rec.Quests)[slot]
	if rec.Quests[in.Key] >= in.Target {
		g.fail(id, "quest_done")
		return
	}
	key := quests.RerollKey(day, slot)
	g.players.SetQuestMark(id, key, rec.Quests[key]+1)
	g.log.Info("quest rerolled", "steamid", id, "quest", in.Key)
	g.sendQuests(id)
}

// questStats is each player's part in a match, as quests count it.
func (g *GC) questStats(l *Lobby, msg *gcpb.CMsgGC_Match_Result, results map[uint64]players.Result) map[uint64]quests.Stats {
	parties := map[uint64]int{}
	clansIn := map[string]int{}
	clanOf := map[uint64]string{}
	for _, rp := range msg.GetPlayers() {
		id := rp.GetSteamId()
		if _, ok := results[id]; !ok {
			continue
		}
		if p := rp.GetOriginalPartyId(); p != 0 {
			parties[p]++
		}
		if g.clans != nil {
			if _, c, _ := g.clans.TagOf(id); c != "" {
				clanOf[id] = c
				clansIn[c]++
			}
		}
	}
	out := map[uint64]quests.Stats{}
	for _, rp := range msg.GetPlayers() {
		id := rp.GetSteamId()
		res, ok := results[id]
		if !ok {
			continue
		}
		medals, gold := quests.CountMedals(rp.GetScoreMedal(), rp.GetKillsMedal(), rp.GetDamageMedal(),
			rp.GetHealingMedal(), rp.GetSupportMedal())
		out[id] = quests.Stats{
			Kills: int(rp.GetKills()), Deaths: int(rp.GetDeaths()), Damage: int(rp.GetDamage()),
			Healing: int(rp.GetHealing()), Support: int(rp.GetSupport()), Score: int(rp.GetScore()),
			Medals: medals, GoldMedals: gold,
			Won: res == players.Win, War: l.War != nil, Classes: quests.ClassesPlayed(rp.GetClassesPlayed()),
			WithParty: rp.GetOriginalPartyId() != 0 && parties[rp.GetOriginalPartyId()] >= 2,
			WithClan:  clanOf[id] != "" && clansIn[clanOf[id]] >= 2,
		}
	}
	return out
}

// creditQuests moves everybody's quests by what they did in a match, pays
// the daily bonus and the streak, and tells each player at once.
func (g *GC) creditQuests(l *Lobby, msg *gcpb.CMsgGC_Match_Result, results map[uint64]players.Result) {
	now := g.now()
	stats := g.questStats(l, msg, results)
	ids := make([]uint64, 0, len(stats))
	for id := range stats {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		st := stats[id]
		rec := g.players.Get(id)
		list := quests.For(id, now, rec.Quests)
		credits := make([]players.QuestCredit, 0, len(list))
		for _, q := range list {
			credits = append(credits, players.QuestCredit{Key: q.Key, Add: q.Credit(st), Target: q.Target,
				XP: q.XP, Reward: q.Reward, RewardN: q.RewardN})
		}
		outcomes := g.players.CreditQuests(id, l.MatchID, credits)
		for i, o := range outcomes {
			if !o.Finished {
				continue
			}
			q := list[i]
			g.log.Info("quest finished", "steamid", id, "quest", q.Key, "xp", q.XP, "reward", q.Reward)
			g.hooks.Emit(sink.Event{Type: "quest_finished", At: now, Data: map[string]any{
				"steam_id": id, "quest": q.ID, "key": q.Key, "progress": o.Total, "xp": q.XP,
				"reward": q.Reward, "reward_n": q.RewardN, "match_id": l.MatchID,
			}})
		}
		g.payDailyBonus(id, now)
		g.sendQuests(id)
	}
}

// payDailyBonus pays for a day with every daily quest finished, and for
// every StreakDays such days in a row. Each pays once.
func (g *GC) payDailyBonus(id uint64, now time.Time) {
	rec := g.players.Get(id)
	day := quests.Day(now)
	for _, q := range quests.For(id, now, rec.Quests) {
		if !q.Weekly && rec.Quests[q.Key] < q.Target {
			return
		}
	}
	if !g.players.Pay(id, quests.DailyBonusKey(day), quests.DailyBonusXP, quests.DailyBonusReward, quests.DailyBonusRewardN) {
		return
	}
	g.log.Info("daily quests all done", "steamid", id, "day", day)
	rec = g.players.Get(id)
	if s := quests.Streak(rec.Quests, now); s > 0 && s%quests.StreakDays == 0 {
		if g.players.Pay(id, quests.StreakBonusKey(day), 0, quests.RareCrate, 1) {
			g.log.Info("quest streak", "steamid", id, "days", s)
		}
	}
}
