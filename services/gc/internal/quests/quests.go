// Package quests is what the game asks players to do in their matches.
//
// Every player has three daily quests, one of each tier (easy, medium,
// hard), and one weekly quest. They are not stored: the day (or week), the
// player's SteamID and how many times they swapped a slot pick them from
// the pool, so every GC that shares a pool agrees on them and nothing needs
// handing out. Progress comes from the match result the game server already
// sends (kills, damage, healing, medals, wins, classes played, parties), and
// is stored by the player store under the quest's key.
//
// Quests pay XP and a reward. Rewards are counted for now (players.Rewards);
// the items behind them come later.
package quests

import (
	"fmt"
	"hash/fnv"
	"time"
)

// Kind is what a quest counts.
type Kind string

const (
	Kills       Kind = "kills"        // kills
	Damage      Kind = "damage"       // damage dealt
	Healing     Kind = "healing"      // healing done
	Support     Kind = "support"      // assists and other support points
	Score       Kind = "score"        // scoreboard points
	Wins        Kind = "wins"         // matches won
	Matches     Kind = "matches"      // matches finished
	WarWins     Kind = "war_wins"     // war battles won
	ClassWin    Kind = "class_wins"   // wins having played Class
	ClassPlay   Kind = "class_play"   // matches having played Class
	Medals      Kind = "medals"       // end-of-match medals, any tier
	GoldMedals  Kind = "gold_medals"  // gold medals
	PartyWins   Kind = "party_wins"   // wins with someone from your party in the match
	PositiveKD  Kind = "positive_kd"  // matches with more kills than deaths (at least 3)
	ClanMatches Kind = "clan_matches" // matches with a clanmate in them
)

// Reward kinds. The game names them (fui_reward_<kind>).
const (
	Crate     = "crate"      // a supply crate
	RareCrate = "rare_crate" // a rare supply crate
)

// Def is a quest in the pool. Class narrows it to matches where the player
// played that class (by the server's classes_played). Per, when set, makes
// it count matches instead: one for each match where the player reached Per
// of the kind ("15 kills in one match").
type Def struct {
	ID      string `json:"id"`
	Kind    Kind   `json:"kind"`
	Class   string `json:"class,omitempty"`
	Per     int    `json:"per,omitempty"`
	Target  int    `json:"target"`
	Tier    int    `json:"tier"` // 1 easy, 2 medium, 3 hard; 4 weekly
	XP      int    `json:"xp"`
	Reward  string `json:"reward"`
	RewardN int    `json:"reward_n"`
}

// What each tier pays.
var tierPay = map[int]struct {
	XP      int
	Reward  string
	RewardN int
}{
	1: {120, Crate, 1},
	2: {200, Crate, 1},
	3: {320, Crate, 2},
	4: {1000, RareCrate, 1},
}

func def(tier int, id string, kind Kind, target int) Def {
	p := tierPay[tier]
	return Def{ID: id, Kind: kind, Target: target, Tier: tier, XP: p.XP, Reward: p.Reward, RewardN: p.RewardN}
}

func per(d Def, n int) Def { d.Per = n; return d }

func classDefs(tier int, kind Kind, suffix string, target int) []Def {
	var out []Def
	for _, c := range Classes[1:] {
		d := def(tier, c+suffix, kind, target)
		d.Class = c
		out = append(out, d)
	}
	return out
}

// Daily is the daily pool, every tier; Weekly the weekly one.
var (
	Daily = append(append(append([]Def{
		// Easy: one evening's normal play.
		def(1, "matches_2", Matches, 2),
		def(1, "kills_15", Kills, 15),
		def(1, "damage_4000", Damage, 4000),
		def(1, "support_15", Support, 15),
		def(1, "score_80", Score, 80),
		def(1, "medal_1", Medals, 1),
		def(1, "positive_kd_1", PositiveKD, 1),

		// Medium: play well.
		def(2, "kills_30", Kills, 30),
		def(2, "damage_8000", Damage, 8000),
		def(2, "healing_3000", Healing, 3000),
		def(2, "wins_2", Wins, 2),
		def(2, "war_wins_1", WarWins, 1),
		def(2, "medals_3", Medals, 3),

		// Hard: a great match, or a good evening's winning.
		per(def(3, "kills_one_15", Kills, 1), 15),
		per(def(3, "damage_one_5000", Damage, 1), 5000),
		per(def(3, "score_one_50", Score, 1), 50),
		def(3, "wins_4", Wins, 4),
		def(3, "war_wins_2", WarWins, 2),
		def(3, "gold_medal_1", GoldMedals, 1),
		def(3, "positive_kd_3", PositiveKD, 3),
		def(3, "party_wins_2", PartyWins, 2),
	}, classDefs(1, ClassPlay, "_play", 1)...), classDefs(2, ClassWin, "_win", 1)...), classDefs(3, ClassWin, "_wins", 2)...)

	Weekly = []Def{
		def(4, "war_wins_5", WarWins, 5),
		def(4, "wins_10", Wins, 10),
		def(4, "kills_200", Kills, 200),
		def(4, "damage_60000", Damage, 60000),
		def(4, "matches_15", Matches, 15),
		def(4, "medals_12", Medals, 12),
		def(4, "party_wins_5", PartyWins, 5),
		def(4, "clan_matches_8", ClanMatches, 8),
	}
)

const (
	// DailyCount is how many daily quests a player has at once: one a tier.
	DailyCount = 3
	// RerollsPerDay is how many daily quests a player may swap a day.
	RerollsPerDay = 1

	// Finishing all of a day's quests pays this on top.
	DailyBonusXP      = 150
	DailyBonusReward  = Crate
	DailyBonusRewardN = 1
	// Every StreakDays days in a row of finished dailies pays a rare crate.
	StreakDays = 7
)

// Classes is TF2's class index order (the bit in classes_played).
var Classes = [...]string{"", "scout", "sniper", "soldier", "demoman", "medic", "heavy", "pyro", "spy", "engineer"}

// Instance is a quest a player has right now.
type Instance struct {
	Def
	// Key identifies this quest for this period ("d2026-11-03:kills_15");
	// progress is stored under it, so tomorrow's kills_15 starts from zero.
	Key    string    `json:"key"`
	Slot   int       `json:"slot"` // 0-2 daily (tier-1), 3 weekly
	Weekly bool      `json:"weekly"`
	Ends   time.Time `json:"ends"`
}

// Day is the daily period a moment falls in, as it appears in keys.
func Day(now time.Time) string { return now.UTC().Format("2006-01-02") }

// RerollKey is where a player's swaps of a daily slot are counted.
func RerollKey(day string, slot int) string { return fmt.Sprintf("reroll:%s/%d", day, slot) }

// DailyBonusKey marks a day whose quests were all finished.
func DailyBonusKey(day string) string { return "daily:" + day }

// StreakBonusKey marks a streak day that paid.
func StreakBonusKey(day string) string { return "streak:" + day }

// RerollsUsed is how many daily swaps a player made on a day.
func RerollsUsed(marks map[string]int, day string) int {
	n := 0
	for slot := 0; slot < DailyCount; slot++ {
		n += marks[RerollKey(day, slot)]
	}
	return n
}

// Streak is how many days in a row, up to the given day, a player finished
// all their dailies. Today not finished yet does not break it.
func Streak(marks map[string]int, now time.Time) int {
	d := now.UTC()
	if marks[DailyBonusKey(Day(d))] == 0 {
		d = d.AddDate(0, 0, -1)
	}
	n := 0
	for marks[DailyBonusKey(Day(d))] > 0 && n < 3650 {
		n++
		d = d.AddDate(0, 0, -1)
	}
	return n
}

// For returns a player's quests at a moment: one daily quest a tier and one
// weekly one. marks is the player's quest store (players.Record.Quests),
// read for daily swaps; nil means none.
func For(steamID uint64, now time.Time, marks map[string]int) []Instance {
	now = now.UTC()
	day := Day(now)
	dayEnd := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	year, week := now.ISOWeek()
	weekStart := dayEnd.AddDate(0, 0, -int((now.Weekday()+6)%7)-1)

	// The day's picks first, a class quest at most once; then the swaps,
	// each changing its own slot and nothing else.
	picks := make([]Def, DailyCount)
	classPicked := false
	for slot := range picks {
		picks[slot] = pickOne(tier(Daily, slot+1), fmt.Sprintf("%d/%s/%d/0", steamID, day, slot), func(c Def) bool {
			return classPicked && c.Class != ""
		})
		classPicked = classPicked || picks[slot].Class != ""
	}
	for slot := range picks {
		for roll := 1; roll <= marks[RerollKey(day, slot)] && roll <= RerollsPerDay*DailyCount; roll++ {
			prev := picks[slot].ID
			otherClass := false
			for i, p := range picks {
				otherClass = otherClass || (i != slot && p.Class != "")
			}
			picks[slot] = pickOne(tier(Daily, slot+1), fmt.Sprintf("%d/%s/%d/%d", steamID, day, slot, roll), func(c Def) bool {
				return c.ID == prev || (otherClass && c.Class != "")
			})
		}
	}
	var out []Instance
	for slot, d := range picks {
		out = append(out, Instance{Def: d, Key: "d" + day + ":" + d.ID, Slot: slot, Ends: dayEnd})
	}
	w := pickOne(Weekly, fmt.Sprintf("%d/%d-W%02d", steamID, year, week), nil)
	out = append(out, Instance{Def: w, Key: fmt.Sprintf("w%d-W%02d:%s", year, week, w.ID), Slot: DailyCount,
		Weekly: true, Ends: weekStart.AddDate(0, 0, 7)})
	return out
}

func tier(pool []Def, t int) []Def {
	var out []Def
	for _, d := range pool {
		if d.Tier == t {
			out = append(out, d)
		}
	}
	return out
}

// pickOne chooses a def from a pool, the same one every time for the same
// seed, skipping the ones skip rules out (unless it rules out them all).
func pickOne(pool []Def, seed string, skip func(Def) bool) Def {
	h := fnv.New64a()
	h.Write([]byte(seed))
	state := h.Sum64() | 1
	next := func() uint64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return state
	}
	first := pool[int(next()%uint64(len(pool)))]
	if skip == nil || !skip(first) {
		return first
	}
	for tries := 0; tries < 200; tries++ {
		if d := pool[int(next()%uint64(len(pool)))]; !skip(d) {
			return d
		}
	}
	return first
}

// Stats is one player's part of one match.
type Stats struct {
	Kills, Deaths, Damage, Healing, Support, Score int
	Medals, GoldMedals                             int
	Won, War                                       bool
	WithParty                                      bool // someone from their party played it too
	WithClan                                       bool // so did a clanmate
	Classes                                        []string
}

// ClassesPlayed reads the server's classes_played bit field.
func ClassesPlayed(bits uint32) []string {
	var out []string
	for i := 1; i < len(Classes); i++ {
		if bits&(1<<uint(i)) != 0 {
			out = append(out, Classes[i])
		}
	}
	return out
}

// CountMedals counts medals (0 none, 1 bronze, 2 silver, 3 gold) and gold
// ones.
func CountMedals(medals ...uint32) (any, gold int) {
	for _, m := range medals {
		if m >= 1 && m <= 3 {
			any++
		}
		if m == 3 {
			gold++
		}
	}
	return any, gold
}

// Credit is how much one match moves a quest.
func (d Def) Credit(s Stats) int {
	n := d.amount(s)
	if d.Per > 0 {
		return b2i(n >= d.Per)
	}
	return n
}

func (d Def) amount(s Stats) int {
	switch d.Kind {
	case Kills:
		return s.Kills
	case Damage:
		return s.Damage
	case Healing:
		return s.Healing
	case Support:
		return s.Support
	case Score:
		return s.Score
	case Matches:
		return 1
	case Wins:
		return b2i(s.Won)
	case WarWins:
		return b2i(s.Won && s.War)
	case Medals:
		return s.Medals
	case GoldMedals:
		return s.GoldMedals
	case PartyWins:
		return b2i(s.Won && s.WithParty)
	case PositiveKD:
		return b2i(s.Kills >= 3 && s.Kills > s.Deaths)
	case ClanMatches:
		return b2i(s.WithClan)
	case ClassWin, ClassPlay:
		if d.Kind == ClassWin && !s.Won {
			return 0
		}
		for _, c := range s.Classes {
			if c == d.Class {
				return 1
			}
		}
	}
	return 0
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
