package players

import "time"

// Quest progress and rewards, written one match at a time.
//
// A match moves all of a player's quests in a single log record, and the
// same match never moves them twice: a result the server sends again, or
// one that arrives once more after a restart, changes nothing. Rewards are
// counted, not handed out: items come later, and whatever was earned by
// then is waiting in Rewards.

// QuestCredit is what one match adds to one quest.
type QuestCredit struct {
	Key     string
	Add     int
	Target  int
	XP      int    // paid when this finishes the quest
	Reward  string // reward kind paid with it, "" for none
	RewardN int
}

// QuestOutcome is where one quest stands after a match.
type QuestOutcome struct {
	Key      string
	Add      int // what the match actually added, after the cap
	Total    int
	Finished bool // this match finished it
}

// RewardEntry is one reward earned, for the history the game shows.
type RewardEntry struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	N      int       `json:"n"`
	Source string    `json:"source"` // the quest key, or a bonus key
}

// LastQuestMatch is what the latest match did to a player's quests.
type LastQuestMatch struct {
	MatchID uint64
	At      time.Time
	Added   map[string]int // by quest key
}

// rewardLogKept is how many earned rewards a record remembers.
const rewardLogKept = 20

type questItem struct {
	Quest    string `json:"quest"`
	Progress int    `json:"progress"`
	Add      int    `json:"add,omitempty"`
	XP       int    `json:"xp,omitempty"`
	Reward   string `json:"reward,omitempty"`
	N        int    `json:"n,omitempty"`
}

const evQuests = "quests"

func (s *Store) applyQuests(r *Record, ev event) {
	if ev.MatchID != 0 {
		key := participation{ev.MatchID, ev.SteamID}
		if s.questsCredited[key] {
			return
		}
		s.questsCredited[key] = true
		r.LastQuest = &LastQuestMatch{MatchID: ev.MatchID, At: ev.At, Added: map[string]int{}}
	}
	if r.Quests == nil {
		r.Quests = map[string]int{}
	}
	for _, it := range ev.Items {
		r.Quests[it.Quest] = it.Progress
		r.QuestXP += it.XP
		if ev.MatchID != 0 && it.Add > 0 {
			r.LastQuest.Added[it.Quest] = it.Add
		}
		if it.Reward != "" && it.N > 0 {
			if r.Rewards == nil {
				r.Rewards = map[string]int{}
			}
			r.Rewards[it.Reward] += it.N
			r.RewardLog = append(r.RewardLog, RewardEntry{At: ev.At, Kind: it.Reward, N: it.N, Source: it.Quest})
			if len(r.RewardLog) > rewardLogKept {
				r.RewardLog = r.RewardLog[len(r.RewardLog)-rewardLogKept:]
			}
		}
	}
}

// CreditQuests adds one match's progress to a player's quests, once per
// match. Progress past a target is not kept, and a quest pays its XP and
// reward once, when it finishes. A zero matchID is never deduplicated.
func (s *Store) CreditQuests(id, matchID uint64, cs []QuestCredit) []QuestOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recordLocked(id)
	out := make([]QuestOutcome, 0, len(cs))
	if matchID != 0 && s.questsCredited[participation{matchID, id}] {
		for _, c := range cs {
			out = append(out, QuestOutcome{Key: c.Key, Total: r.Quests[c.Key]})
		}
		return out
	}
	ev := event{Type: evQuests, At: s.now(), SteamID: id, MatchID: matchID}
	for _, c := range cs {
		before := r.Quests[c.Key]
		o := QuestOutcome{Key: c.Key, Total: before}
		if c.Add > 0 && before < c.Target {
			o.Total = min(before+c.Add, c.Target)
			o.Add = o.Total - before
			o.Finished = o.Total >= c.Target
			it := questItem{Quest: c.Key, Progress: o.Total, Add: o.Add}
			if o.Finished {
				it.XP, it.Reward, it.N = c.XP, c.Reward, c.RewardN
			}
			ev.Items = append(ev.Items, it)
		}
		out = append(out, o)
	}
	// A match that moved nothing is still remembered, so a resend of it
	// cannot move anything either.
	if len(ev.Items) != 0 || matchID != 0 {
		s.appendLocked(ev)
	}
	return out
}

// Pay pays a bonus once per key: XP and a reward. It reports whether it
// paid now.
func (s *Store) Pay(id uint64, key string, xp int, reward string, n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recordLocked(id)
	if r.Quests[key] > 0 {
		return false
	}
	s.appendLocked(event{Type: evQuests, At: s.now(), SteamID: id,
		Items: []questItem{{Quest: key, Progress: 1, XP: xp, Reward: reward, N: n}}})
	return true
}

// SetQuestMark stores a number under a quest key (a reroll count, say).
func (s *Store) SetQuestMark(id uint64, key string, value int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordLocked(id).Quests[key] == value {
		return
	}
	s.appendLocked(event{Type: evQuests, At: s.now(), SteamID: id, Items: []questItem{{Quest: key, Progress: value}}})
}
