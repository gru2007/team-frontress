// Package quests is what the game asks players to do in their matches.
//
// Every player has three daily quests and one weekly quest. They are not
// stored: the day (or week) and the player's SteamID pick them from the pool,
// so every GC that shares a pool agrees on them and nothing needs handing
// out. Progress comes from the match result the game server already sends
// (kills, damage, healing, wins, classes played), and is stored by the player
// store under the quest's key.
package quests

import (
	"fmt"
	"hash/fnv"
	"time"
)

// Kind is what a quest counts.
type Kind string

const (
	Kills    Kind = "kills"    // kills in matches
	Damage   Kind = "damage"   // damage dealt
	Healing  Kind = "healing"  // healing done
	Support  Kind = "support"  // assists and other support points
	Score    Kind = "score"    // scoreboard points
	Wins     Kind = "wins"     // matches won
	Matches  Kind = "matches"  // matches finished
	WarWins  Kind = "war_wins" // war battles won
	ClassWin Kind = "class_wins"
)

// Def is a quest in the pool. Class narrows it to matches where the player
// played that class (by the server's classes_played).
type Def struct {
	ID     string `json:"id"`
	Kind   Kind   `json:"kind"`
	Class  string `json:"class,omitempty"`
	Target int    `json:"target"`
	XP     int    `json:"xp"`
}

// Daily and Weekly are the pools. Daily quests take an evening; the weekly one
// a few.
var (
	Daily = []Def{
		{ID: "kills_25", Kind: Kills, Target: 25, XP: 150},
		{ID: "kills_40", Kind: Kills, Target: 40, XP: 220},
		{ID: "damage_6000", Kind: Damage, Target: 6000, XP: 180},
		{ID: "damage_10000", Kind: Damage, Target: 10000, XP: 250},
		{ID: "healing_4000", Kind: Healing, Target: 4000, XP: 200},
		{ID: "support_30", Kind: Support, Target: 30, XP: 160},
		{ID: "score_150", Kind: Score, Target: 150, XP: 160},
		{ID: "wins_2", Kind: Wins, Target: 2, XP: 200},
		{ID: "matches_3", Kind: Matches, Target: 3, XP: 150},
		{ID: "war_wins_1", Kind: WarWins, Target: 1, XP: 220},
		{ID: "soldier_win", Kind: ClassWin, Class: "soldier", Target: 1, XP: 180},
		{ID: "medic_win", Kind: ClassWin, Class: "medic", Target: 1, XP: 200},
		{ID: "engineer_win", Kind: ClassWin, Class: "engineer", Target: 1, XP: 180},
		{ID: "scout_win", Kind: ClassWin, Class: "scout", Target: 1, XP: 180},
		{ID: "demoman_win", Kind: ClassWin, Class: "demoman", Target: 1, XP: 180},
		{ID: "heavy_win", Kind: ClassWin, Class: "heavy", Target: 1, XP: 180},
		{ID: "pyro_win", Kind: ClassWin, Class: "pyro", Target: 1, XP: 180},
		{ID: "sniper_win", Kind: ClassWin, Class: "sniper", Target: 1, XP: 180},
		{ID: "spy_win", Kind: ClassWin, Class: "spy", Target: 1, XP: 200},
	}
	Weekly = []Def{
		{ID: "war_wins_5", Kind: WarWins, Target: 5, XP: 900},
		{ID: "wins_10", Kind: Wins, Target: 10, XP: 1000},
		{ID: "kills_200", Kind: Kills, Target: 200, XP: 900},
		{ID: "damage_60000", Kind: Damage, Target: 60000, XP: 1000},
		{ID: "matches_15", Kind: Matches, Target: 15, XP: 800},
	}
)

// DailyCount is how many daily quests a player has at once.
const DailyCount = 3

// Classes is TF2's class index order (the bit in classes_played).
var Classes = [...]string{"", "scout", "sniper", "soldier", "demoman", "medic", "heavy", "pyro", "spy", "engineer"}

// Instance is a quest a player has right now.
type Instance struct {
	Def
	// Key identifies this quest for this period ("d2026-11-03:kills_25");
	// progress is stored under it, so tomorrow's kills_25 starts from zero.
	Key    string    `json:"key"`
	Weekly bool      `json:"weekly"`
	Ends   time.Time `json:"ends"`
}

// For returns a player's quests at a moment: DailyCount daily ones and one
// weekly one.
func For(steamID uint64, now time.Time) []Instance {
	now = now.UTC()
	day := now.Format("2006-01-02")
	dayEnd := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	year, week := now.ISOWeek()
	weekStart := dayEnd.AddDate(0, 0, -int((now.Weekday()+6)%7)-1)

	var out []Instance
	for _, d := range pick(Daily, DailyCount, fmt.Sprintf("%d/%s", steamID, day)) {
		out = append(out, Instance{Def: d, Key: "d" + day + ":" + d.ID, Ends: dayEnd})
	}
	w := pick(Weekly, 1, fmt.Sprintf("%d/%d-W%02d", steamID, year, week))[0]
	out = append(out, Instance{Def: w, Key: fmt.Sprintf("w%d-W%02d:%s", year, week, w.ID), Weekly: true, Ends: weekStart.AddDate(0, 0, 7)})
	return out
}

// pick chooses n distinct defs from a pool, the same ones every time for the
// same seed. Class quests count as one family, so a day never asks for two.
func pick(pool []Def, n int, seed string) []Def {
	h := fnv.New64a()
	h.Write([]byte(seed))
	state := h.Sum64()
	next := func() uint64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return state
	}
	var out []Def
	used := map[int]bool{}
	classPicked := false
	for tries := 0; len(out) < n && tries < 1000; tries++ {
		i := int(next() % uint64(len(pool)))
		if used[i] || (pool[i].Kind == ClassWin && classPicked) {
			continue
		}
		used[i] = true
		classPicked = classPicked || pool[i].Kind == ClassWin
		out = append(out, pool[i])
	}
	return out
}

// Stats is one player's part of one match.
type Stats struct {
	Kills, Damage, Healing, Support, Score int
	Won, War                               bool
	Classes                                []string
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

// Credit is how much one match moves a quest.
func (d Def) Credit(s Stats) int {
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
	case ClassWin:
		if !s.Won {
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
