// Package players keeps what the GC remembers about a player across matches:
// matches played, wins, abandons, and a rating.
//
// State is a fold over an append-only JSONL log, the same shape as the war's,
// so a restart replays to the same numbers. Delete the file to forget
// everyone; an empty path keeps everything in memory.
package players

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Record is one player.
type Record struct {
	SteamID     uint64    `json:"steam_id,string"`
	Name        string    `json:"name,omitempty"`
	Matches     int       `json:"matches"`
	Wins        int       `json:"wins"`
	Losses      int       `json:"losses"`
	Abandons    int       `json:"abandons"`
	Rating      int       `json:"rating"`
	WarCampaign string    `json:"war_campaign,omitempty"`
	WarSide     string    `json:"war_side,omitempty"`
	LastAbandon time.Time `json:"last_abandon,omitempty"`
	LastMatch   time.Time `json:"last_match,omitempty"`
	// QuestXP is experience from finished quests and war orders, on top of
	// what matches give.
	QuestXP int `json:"quest_xp,omitempty"`
	// Quests is progress by quest key (see package quests).
	Quests       map[string]int                    `json:"-"`
	Acknowledged map[int32]ProgressAcknowledgement `json:"-"`
}

type ProgressAcknowledgement struct {
	Rank  int
	Value int
	At    time.Time
}

// Result of one match for one player.
type Result string

const (
	Win  Result = "win"
	Loss Result = "loss"
	Draw Result = "draw"
)

type event struct {
	Type    string    `json:"t"`
	At      time.Time `json:"at"`
	SteamID uint64    `json:"id,string"`
	Name    string    `json:"name,omitempty"`
	MatchID uint64    `json:"match_id,string,omitempty"`
	Result  Result    `json:"result,omitempty"`
	// Rating is the rating after this event, for "played". Storing the
	// outcome rather than the delta keeps replay independent of the formula.
	Rating int `json:"rating,omitempty"`
	// Quest progress: the total after this event, and the XP it paid out
	// if it finished the quest.
	Quest    string  `json:"quest,omitempty"`
	Progress int     `json:"progress,omitempty"`
	XP       int     `json:"xp,omitempty"`
	Campaign string  `json:"campaign,omitempty"`
	Side     string  `json:"side,omitempty"`
	Players  []event `json:"players,omitempty"` // a complete match in one log record
	Group    int32   `json:"group,omitempty"`
	Value    int     `json:"value,omitempty"`
	Rank     int     `json:"rank,omitempty"`
}

const (
	evProgressAck = "progress_ack"
	evPlayed      = "played"
	evMatch       = "match"
	evAbandon     = "abandon"
	evQuest       = "quest"
	evBonus       = "bonus"
)

// Store is the record set.
type Store struct {
	mu        sync.Mutex
	records   map[uint64]*Record
	played    map[participation]bool
	abandoned map[participation]bool
	f         *os.File
	now       func() time.Time
}

type participation struct{ matchID, steamID uint64 }

// Open opens a store. An empty path keeps everything in memory.
func Open(path string) (*Store, error) {
	s := &Store{records: map[uint64]*Record{}, played: map[participation]bool{},
		abandoned: map[participation]bool{}, now: time.Now}
	if path == "" {
		return s, nil
	}
	if err := s.replay(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("players: %w", err)
	}
	s.f = f
	return s, nil
}

// UseClock replaces the clock. Tests only.
func (s *Store) UseClock(f func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = f
}

// Close flushes the log.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

func (s *Store) replay(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("players: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	line := 0
	var good int64 // bytes up to the end of the last good line
	var torn error
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			if torn == nil {
				good += 1
			}
			continue
		}
		if torn != nil {
			// A bad line followed by good ones is corruption, not a crash.
			return torn
		}
		var ev event
		if err := json.Unmarshal(raw, &ev); err != nil {
			// A torn last line from a crash mid-write is expected.
			torn = fmt.Errorf("players: %s:%d: %w", path, line, err)
			continue
		}
		good += int64(len(raw)) + 1
		s.apply(ev)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if torn != nil {
		// Cut the torn tail off, or the next append would bury it in the
		// middle of the file and the restart after that would refuse it.
		if err := os.Truncate(path, good); err != nil {
			return fmt.Errorf("players: repairing %s: %w", path, err)
		}
	}
	return nil
}

func (s *Store) recordLocked(id uint64) *Record {
	r, ok := s.records[id]
	if !ok {
		r = &Record{SteamID: id, Rating: DefaultRating}
		s.records[id] = r
	}
	return r
}

func (s *Store) apply(ev event) {
	if ev.Type == evMatch {
		for _, p := range ev.Players {
			p.Type, p.MatchID, p.At = evPlayed, ev.MatchID, ev.At
			s.apply(p)
		}
		return
	}
	if ev.SteamID == 0 {
		return
	}
	r := s.recordLocked(ev.SteamID)
	if ev.Name != "" {
		r.Name = ev.Name
	}
	switch ev.Type {
	case "war_side":
		r.WarCampaign, r.WarSide = ev.Campaign, ev.Side
	case evPlayed:
		if ev.MatchID != 0 {
			key := participation{ev.MatchID, ev.SteamID}
			if s.played[key] {
				return
			}
			s.played[key] = true
		}
		r.Matches++
		r.LastMatch = ev.At
		switch ev.Result {
		case Win:
			r.Wins++
		case Loss:
			r.Losses++
		}
		if ev.Rating > 0 {
			r.Rating = ev.Rating
		}
	case evAbandon:
		if ev.MatchID != 0 {
			key := participation{ev.MatchID, ev.SteamID}
			if s.abandoned[key] {
				return
			}
			s.abandoned[key] = true
		}
		r.Abandons++
		r.LastAbandon = ev.At
	case evBonus:
		r.QuestXP += ev.XP
	case evQuest:
		if r.Quests == nil {
			r.Quests = map[string]int{}
		}
		r.Quests[ev.Quest] = ev.Progress
		r.QuestXP += ev.XP
	case evProgressAck:
		if r.Acknowledged == nil {
			r.Acknowledged = make(map[int32]ProgressAcknowledgement)
		}
		r.Acknowledged[ev.Group] = ProgressAcknowledgement{Value: ev.Value, Rank: ev.Rank, At: ev.At}
	}
}

// AcknowledgeProgress advances the baseline only to a value the client saw.
func (s *Store) AcknowledgeProgress(id uint64, group int32, predicted uint32, rating bool, rank int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recordLocked(id)
	value := r.XP()
	if rating {
		value = max(0, r.Rating)
	}
	if uint64(value) != uint64(predicted) {
		return false
	}
	if ack, ok := r.Acknowledged[group]; !ok || ack.Value != value || ack.Rank != rank {
		s.appendLocked(event{Type: evProgressAck, At: s.now(), SteamID: id, Group: group, Value: value, Rank: rank})
	}
	return true
}

// QuestProgress adds progress to one of a player's quests and reports the new
// total and whether this finished it. Progress past the target is not kept,
// and a finished quest pays its XP once.
func (s *Store) QuestProgress(id uint64, key string, add, target, xp int) (total int, finished bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recordLocked(id)
	before := r.Quests[key]
	if add <= 0 || before >= target {
		return before, false
	}
	total = before + add
	if total > target {
		total = target
	}
	finished = total >= target
	ev := event{Type: evQuest, At: s.now(), SteamID: id, Quest: key, Progress: total}
	if finished {
		ev.XP = xp
	}
	s.appendLocked(ev)
	return total, finished
}

// Bonus pays a player extra experience (a war order's), once per key: the
// same match never pays twice, even if its result arrives again.
func (s *Store) Bonus(id uint64, key string, xp int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recordLocked(id)
	if xp <= 0 || r.Quests["bonus:"+key] > 0 {
		return false
	}
	s.appendLocked(event{Type: evQuest, At: s.now(), SteamID: id, Quest: "bonus:" + key, Progress: 1})
	s.appendLocked(event{Type: evBonus, At: s.now(), SteamID: id, XP: xp})
	return true
}

func (s *Store) appendLocked(ev event) {
	s.apply(ev)
	if s.f == nil {
		return
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	// A failed write loses one event, not the process: memory is already
	// right, and only the next restart would notice.
	_, _ = s.f.Write(append(raw, '\n'))
}

// Participant is one player's part in a finished match, as the rating needs it.
type Participant struct {
	SteamID uint64
	Name    string
	Team    int // 0 or 1; which is which does not matter
	Result  Result
}

// RecordMatch writes a finished match for everybody who played it, rating
// included. It returns each player's new record.
func (s *Store) RecordMatch(matchID uint64, ps []Participant) map[uint64]Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	ratings := make(map[uint64]int, len(ps))
	games := make(map[uint64]int, len(ps))
	for _, p := range ps {
		r := s.recordLocked(p.SteamID)
		ratings[p.SteamID] = r.Rating
		games[p.SteamID] = r.Matches
	}
	next := RatingUpdates(ps, ratings, games)
	batch := event{Type: evMatch, At: s.now(), MatchID: matchID}
	out := make(map[uint64]Record, len(ps))
	for _, p := range ps {
		if !s.played[participation{matchID, p.SteamID}] {
			batch.Players = append(batch.Players, event{SteamID: p.SteamID, Name: p.Name,
				Result: p.Result, Rating: next[p.SteamID]})
		}
	}
	if len(batch.Players) != 0 {
		s.appendLocked(batch)
	}
	for _, p := range ps {
		out[p.SteamID] = *s.records[p.SteamID]
	}
	return out
}

// Abandoned records that a player walked out of a match.
func (s *Store) Abandoned(id uint64, name string, matchID uint64) Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.abandoned[participation{matchID, id}] {
		s.appendLocked(event{Type: evAbandon, At: s.now(), SteamID: id, Name: name, MatchID: matchID})
	}
	return *s.records[id]
}

// Get returns a copy of a record. An unknown player is a fresh one.
func (s *Store) Get(id uint64) Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[id]; ok {
		out := *r
		if r.Quests != nil {
			out.Quests = make(map[string]int, len(r.Quests))
			for k, v := range r.Quests {
				out.Quests[k] = v
			}
		}
		if r.Acknowledged != nil {
			out.Acknowledged = make(map[int32]ProgressAcknowledgement, len(r.Acknowledged))
			for group, ack := range r.Acknowledged {
				out.Acknowledged[group] = ack
			}
		}
		return out
	}
	return Record{SteamID: id, Rating: DefaultRating}
}

// Known is how many players the store has seen.
func (s *Store) Known() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// Page is a slice of the record set for lists and leaderboards.
type Page struct {
	Records []Record
	Total   int
}

// List is the players who have played, filtered by a name or SteamID
// fragment (q, case-insensitive) and ordered by sort: "level" (XP, the
// default), "matches", "wins" or "recent" (last match).
func (s *Store) List(q, sortBy string, offset, limit int) Page {
	s.mu.Lock()
	out := make([]Record, 0, len(s.records))
	q = strings.ToLower(strings.TrimSpace(q))
	for _, r := range s.records {
		if r.Matches == 0 && r.QuestXP == 0 {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Name), q) &&
			!strings.Contains(strconv.FormatUint(r.SteamID, 10), q) {
			continue
		}
		c := *r
		c.Quests, c.Acknowledged = nil, nil
		out = append(out, c)
	}
	s.mu.Unlock()
	key := func(r Record) int {
		switch sortBy {
		case "matches":
			return r.Matches
		case "wins":
			return r.Wins
		default:
			return r.XP()
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if sortBy == "recent" {
			if !out[i].LastMatch.Equal(out[j].LastMatch) {
				return out[i].LastMatch.After(out[j].LastMatch)
			}
		} else if a, b := key(out[i]), key(out[j]); a != b {
			return a > b
		}
		return out[i].SteamID < out[j].SteamID
	})
	p := Page{Total: len(out)}
	if offset < len(out) {
		end := min(offset+limit, len(out))
		p.Records = out[offset:end]
	}
	if p.Records == nil {
		p.Records = []Record{}
	}
	return p
}
