package gc

import (
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
}

// Quests is everything the game shows about a player's quests.
type Quests struct {
	Daily       []QuestView `json:"daily"`
	Weekly      []QuestView `json:"weekly"`
	DailyEnds   time.Time   `json:"daily_ends"`
	WeeklyEnds  time.Time   `json:"weekly_ends"`
	DailyLeft   int         `json:"daily_left"`  // seconds
	WeeklyLeft  int         `json:"weekly_left"` // seconds
	CompletedXP int         `json:"completed_xp"`
}

// PlayerQuests is a player's quests right now. Safe from any goroutine: it
// reads the player store, which has its own lock.
func (g *GC) PlayerQuests(id uint64) Quests {
	now := g.now()
	rec := g.players.Get(id)
	out := Quests{Daily: []QuestView{}, Weekly: []QuestView{}, CompletedXP: rec.QuestXP}
	for _, in := range quests.For(id, now) {
		v := QuestView{Instance: in, Progress: rec.Quests[in.Key]}
		v.Done = v.Progress >= in.Target
		if in.Weekly {
			out.Weekly = append(out.Weekly, v)
			out.WeeklyEnds = in.Ends
		} else {
			out.Daily = append(out.Daily, v)
			out.DailyEnds = in.Ends
		}
	}
	out.DailyLeft = int(out.DailyEnds.Sub(now).Seconds())
	out.WeeklyLeft = int(out.WeeklyEnds.Sub(now).Seconds())
	return out
}

// creditQuests moves everybody's quests by what they did in a match.
func (g *GC) creditQuests(l *Lobby, msg *gcpb.CMsgGC_Match_Result, results map[uint64]players.Result) {
	now := g.now()
	for _, rp := range msg.GetPlayers() {
		id := rp.GetSteamId()
		res, ok := results[id]
		if !ok {
			continue
		}
		st := quests.Stats{
			Kills: int(rp.GetKills()), Damage: int(rp.GetDamage()), Healing: int(rp.GetHealing()),
			Support: int(rp.GetSupport()), Score: int(rp.GetScore()),
			Won: res == players.Win, War: l.War != nil, Classes: quests.ClassesPlayed(rp.GetClassesPlayed()),
		}
		for _, q := range quests.For(id, now) {
			total, finished := g.players.QuestProgress(id, q.Key, q.Credit(st), q.Target, q.XP)
			if !finished {
				continue
			}
			g.log.Info("quest finished", "steamid", id, "quest", q.Key, "xp", q.XP)
			g.hooks.Emit(sink.Event{Type: "quest_finished", At: now, Data: map[string]any{
				"steam_id": id, "quest": q.ID, "key": q.Key, "progress": total, "xp": q.XP,
			}})
		}
	}
}
