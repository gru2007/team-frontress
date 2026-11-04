// Package mm is the matchmaking arithmetic: which queued parties make a match,
// how they split into teams, which map they play, and whether a party fits
// into a match already running.
//
// Everything here is a pure function of its arguments. The GC owns the queue
// and the clock and asks; this package only answers, which is what makes it
// testable without a server, a client or a timer.
//
// The constraint that shapes it is a small population. The matchmaker forms
// the largest match it can, holds out for the ideal size for a configured
// while, and then settles for the minimum rather than leaving six people in a
// queue that wants twenty-four.
package mm

import (
	"math"
	"sort"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/war"
)

// Team is a side of a match. The values are TF_GC_TEAM's.
type Team int32

const (
	Red Team = 0 // TF_GC_TEAM_DEFENDERS
	Blu Team = 1 // TF_GC_TEAM_INVADERS
)

// Player is one queued player.
type Player struct {
	SteamID uint64
	Rating  int
}

// Entry is one party in one queue. A party is never split.
type Entry struct {
	PartyID  uint64
	QueuedAt time.Time
	Players  []Player
	// Maps the party voted for, already limited to names. Empty means any.
	Maps []string
}

// Size is how many seats the entry needs.
func (e Entry) Size() int { return len(e.Players) }

func (e Entry) rating() int {
	total := 0
	for _, p := range e.Players {
		r := p.Rating
		if r == 0 {
			r = 1500
		}
		total += r
	}
	return total
}

// Sizes is how big a match may be.
type Sizes struct{ Min, Ideal, Max int }

// Formed is a match the queue can make.
type Formed struct {
	Red, Blu []Entry
}

// Seats is how many players the match has.
func (f Formed) Seats() int { return Count(f.Red) + Count(f.Blu) }

// Count is how many seats a set of entries takes.
func Count(es []Entry) int {
	n := 0
	for _, e := range es {
		n += e.Size()
	}
	return n
}

// Form makes the best match it can out of a queue sorted oldest first, or
// reports false. patience is how long the oldest entry is made to wait for
// the ideal size before the minimum will do.
func Form(queue []Entry, sizes Sizes, patience time.Duration, now time.Time) (Formed, bool) {
	if len(queue) == 0 {
		return Formed{}, false
	}
	threshold := sizes.Ideal
	if patience <= 0 || now.Sub(queue[0].QueuedAt) >= patience {
		threshold = sizes.Min
	}
	if Count(queue) < threshold {
		return Formed{}, false
	}

	var picked []Entry
	seats := 0
	for _, e := range queue {
		if seats+e.Size() > sizes.Max {
			continue
		}
		picked = append(picked, e)
		seats += e.Size()
	}
	if seats < sizes.Min {
		return Formed{}, false
	}
	// Drop the newest entry until the rest splits evenly. The oldest are
	// never the ones left behind.
	for len(picked) > 0 {
		red, blu, ok := SplitTeams(picked)
		if ok && Count(picked) >= sizes.Min && Count(red) == Count(blu) {
			return Formed{Red: red, Blu: blu}, true
		}
		picked = picked[:len(picked)-1]
	}
	return Formed{}, false
}

// SplitTeams divides parties into two sides whose sizes differ by at most one,
// preferring the split with the closest average rating.
func SplitTeams(es []Entry) (red, blu []Entry, ok bool) {
	n := len(es)
	total := Count(es)
	if n <= 16 {
		bestDiff := -1
		bestRating := math.MaxFloat64
		var bestMask uint32
		for mask := uint32(0); mask < uint32(1)<<uint(n); mask++ {
			sum := 0
			for i := 0; i < n; i++ {
				if mask&(1<<uint(i)) != 0 {
					sum += es[i].Size()
				}
			}
			diff := absInt(total - 2*sum)
			rd := maskRatingDiff(es, mask)
			if bestDiff == -1 || diff < bestDiff || (diff == bestDiff && rd < bestRating) {
				bestDiff, bestRating, bestMask = diff, rd, mask
			}
		}
		if bestDiff > 1 {
			return nil, nil, false
		}
		for i := 0; i < n; i++ {
			if bestMask&(1<<uint(i)) != 0 {
				red = append(red, es[i])
			} else {
				blu = append(blu, es[i])
			}
		}
		return red, blu, true
	}

	// Too many parties to try every split: largest first, each onto the side
	// that keeps the seats even, then the ratings.
	sorted := append([]Entry(nil), es...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Size() != sorted[j].Size() {
			return sorted[i].Size() > sorted[j].Size()
		}
		return sorted[i].rating() > sorted[j].rating()
	})
	rs, bs, rr, br := 0, 0, 0, 0
	for _, e := range sorted {
		size, rating := e.Size(), e.rating()
		rd := absInt(rs + size - bs)
		bd := absInt(rs - bs - size)
		putRed := rd < bd
		if rd == bd {
			putRed = avgDiff(rr+rating, rs+size, br, bs) <= avgDiff(rr, rs, br+rating, bs+size)
		}
		if putRed {
			red = append(red, e)
			rs += size
			rr += rating
		} else {
			blu = append(blu, e)
			bs += size
			br += rating
		}
	}
	return red, blu, absInt(rs-bs) <= 1
}

func maskRatingDiff(es []Entry, mask uint32) float64 {
	rt, bt, rp, bp := 0, 0, 0, 0
	for i, e := range es {
		for _, p := range e.Players {
			r := p.Rating
			if r == 0 {
				r = 1500
			}
			if mask&(1<<uint(i)) != 0 {
				rt += r
				rp++
			} else {
				bt += r
				bp++
			}
		}
	}
	return avgDiff(rt, rp, bt, bp)
}

func avgDiff(rt, rp, bt, bp int) float64 {
	if rp == 0 || bp == 0 {
		return math.MaxFloat64
	}
	return math.Abs(float64(rt)/float64(rp) - float64(bt)/float64(bp))
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// RoomFor picks the side a party of n may join in a running match with red
// and blu players: the smaller side, as long as it stays within the team cap.
// A party is never split to make it fit.
func RoomFor(red, blu, n, teamCap int) (Team, bool) {
	first, second := Red, Blu
	firstN, secondN := red, blu
	if blu < red {
		first, second = Blu, Red
		firstN, secondN = blu, red
	}
	if firstN+n <= teamCap {
		return first, true
	}
	if secondN+n <= teamCap {
		return second, true
	}
	return 0, false
}

// ChooseMap picks a map: one every voting party accepts if there is one, any
// voted map otherwise, the whole pool if nobody voted -- and among those, the
// one played longest ago.
func ChooseMap(pool []string, picked []Entry, lastPlayed func(string) time.Time) string {
	allowed := map[string]bool{}
	for _, name := range pool {
		allowed[name] = true
	}
	counts := map[string]int{}
	voters := 0
	for _, e := range picked {
		if len(e.Maps) == 0 {
			continue
		}
		voters++
		seen := map[string]bool{}
		for _, name := range e.Maps {
			if !allowed[name] || seen[name] {
				continue
			}
			seen[name] = true
			counts[name]++
		}
	}
	var candidates []string
	if voters > 0 {
		for name, n := range counts {
			if n == voters {
				candidates = append(candidates, name)
			}
		}
		if len(candidates) == 0 {
			for name := range counts {
				candidates = append(candidates, name)
			}
		}
	}
	if len(candidates) == 0 {
		candidates = append(candidates, pool...)
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Strings(candidates)
	best := candidates[0]
	bestPlayed := lastPlayed(best)
	for _, name := range candidates[1:] {
		if p := lastPlayed(name); p.Before(bestPlayed) {
			best, bestPlayed = name, p
		}
	}
	return best
}

// BattleSizes widens a group's sizes to cover every battlefield a war battle
// may be fought on, so that a stage with a 2v2 arena field can form at four
// players even in a group configured for 24.
func BattleSizes(group Sizes, fields []war.Battlefield) Sizes {
	if len(fields) == 0 {
		return group
	}
	s := FieldSizes(group, fields[0])
	for _, f := range fields[1:] {
		fs := FieldSizes(group, f)
		s.Min = min(s.Min, fs.Min)
		s.Ideal = max(s.Ideal, fs.Ideal)
		s.Max = max(s.Max, fs.Max)
	}
	return s
}

// FieldSizes is one battlefield's sizes, falling back to the group's.
func FieldSizes(group Sizes, f war.Battlefield) Sizes {
	s := Sizes{Min: f.MinPlayers, Ideal: f.IdealPlayers, Max: f.MaxPlayers}
	if s.Min <= 0 {
		s.Min = group.Min
	}
	if s.Ideal <= 0 {
		s.Ideal = group.Ideal
	}
	if s.Max <= 0 {
		s.Max = group.Max
	}
	if s.Ideal < s.Min {
		s.Ideal = s.Min
	}
	if s.Max < s.Ideal {
		s.Max = s.Ideal
	}
	return s
}

// FieldsFitting are the battlefields a match of this many seats fits, or all
// of them if none does.
func FieldsFitting(group Sizes, fields []war.Battlefield, seats int) []war.Battlefield {
	var fits []war.Battlefield
	for _, f := range fields {
		fs := FieldSizes(group, f)
		if seats >= fs.Min && seats <= fs.Max {
			fits = append(fits, f)
		}
	}
	if len(fits) == 0 {
		return fields
	}
	return fits
}

// FieldNamed finds a battlefield by map, or returns def.
func FieldNamed(fields []war.Battlefield, name string, def war.Battlefield) war.Battlefield {
	for _, f := range fields {
		if f.Map == name {
			return f
		}
	}
	return def
}

// MapNames lists the maps of some battlefields.
func MapNames(fields []war.Battlefield) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Map)
	}
	return out
}
