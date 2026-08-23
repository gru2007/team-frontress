package mm

import (
	"sort"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/war"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// form tries to build one match for a group. It returns nil when the queue
// cannot fill a match yet.
//
// Two thresholds are at work. IdealPlayers is what the group wants; MinPlayers
// is what it will accept once the oldest ticket has waited PatientSecs. That is
// the whole small-population story: six people do not wait forever for a
// twelfth, they get a 3v3 a minute later.
func (m *Matchmaker) form(group config.MatchGroupConfig) *Match {
	m.mu.Lock()
	defer m.mu.Unlock()

	queue := m.sortedSearchingLocked(group.MatchGroup)
	if len(queue) == 0 {
		return nil
	}

	total := 0
	for _, t := range queue {
		total += t.Size()
	}
	threshold := group.IdealPlayers
	waited := m.now().Sub(queue[0].queuedAt)
	if group.PatientSecs <= 0 || waited >= time.Duration(group.PatientSecs)*time.Second {
		threshold = group.MinPlayers
	}
	if total < threshold {
		return nil
	}

	// Take tickets oldest-first up to the group's ceiling. A ticket that does
	// not fit is skipped, not dropped: a party of six should not keep a party
	// of one out of the match after it.
	var picked []*Ticket
	seats := 0
	for _, t := range queue {
		if seats+t.Size() > group.MaxPlayers {
			continue
		}
		picked = append(picked, t)
		seats += t.Size()
	}
	if seats < group.MinPlayers {
		return nil
	}

	// Trim newest-first until the teams can be made even. Partitioning cannot
	// always balance an arbitrary set of party sizes, and an unbalanced match
	// is worse than a slightly smaller one.
	for len(picked) > 0 {
		red, blu, ok := splitTeams(picked)
		if ok && countSeats(picked) >= group.MinPlayers {
			return m.buildMatchLocked(group, picked, red, blu)
		}
		picked = picked[:len(picked)-1]
	}
	return nil
}

func countSeats(ts []*Ticket) int {
	n := 0
	for _, t := range ts {
		n += t.Size()
	}
	return n
}

// splitTeams puts whole parties on teams so the sides differ by at most one
// player. It reports false when no such split exists.
//
// For a handful of parties this enumerates every split, which is exact. Beyond
// that it falls back to filling the smaller side first, which is not exact but
// is only reached at party counts a small-population coordinator will not see.
func splitTeams(ts []*Ticket) (red, blu []*Ticket, ok bool) {
	n := len(ts)
	total := countSeats(ts)

	if n <= 16 {
		bestDiff := -1
		var bestMask uint32
		for mask := uint32(0); mask < uint32(1)<<uint(n); mask++ {
			sum := 0
			for i := 0; i < n; i++ {
				if mask&(1<<uint(i)) != 0 {
					sum += ts[i].Size()
				}
			}
			diff := total - 2*sum
			if diff < 0 {
				diff = -diff
			}
			if bestDiff == -1 || diff < bestDiff {
				bestDiff, bestMask = diff, mask
			}
			if bestDiff == 0 {
				break
			}
		}
		if bestDiff > 1 {
			return nil, nil, false
		}
		for i := 0; i < n; i++ {
			if bestMask&(1<<uint(i)) != 0 {
				red = append(red, ts[i])
			} else {
				blu = append(blu, ts[i])
			}
		}
		return red, blu, true
	}

	sorted := append([]*Ticket(nil), ts...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Size() > sorted[j].Size() })
	redSeats, bluSeats := 0, 0
	for _, t := range sorted {
		if redSeats <= bluSeats {
			red = append(red, t)
			redSeats += t.Size()
		} else {
			blu = append(blu, t)
			bluSeats += t.Size()
		}
	}
	diff := redSeats - bluSeats
	if diff < 0 {
		diff = -diff
	}
	return red, blu, diff <= 1
}

// buildMatchLocked turns a chosen set of tickets into a match record. The
// caller holds the lock.
func (m *Matchmaker) buildMatchLocked(group config.MatchGroupConfig, picked, red, blu []*Ticket) *Match {
	mt := &Match{
		ID:         m.newID(),
		MatchGroup: group.MatchGroup,
		Password:   randomID()[:8],
		state:      msBooting,
		createdAt:  m.now(),
	}

	attackerTeam := wire.TeamUnassigned
	if m.war != nil {
		if b, front, ok := m.warBattleLocked(); ok {
			mt.Map = b.Stage.Map
			mt.FrontID = front.ID
			attackerTeam = wire.Team(b.AttackerTeam)
			mt.War = &wire.WarBriefing{
				FrontID:      front.ID,
				NodeID:       front.NodeID,
				NodeName:     b.NodeName,
				StageIndex:   b.StageIndex,
				StageCount:   b.StageCount,
				StageKind:    b.Stage.Kind,
				AttackerWar:  string(b.Attacker),
				AttackerTeam: attackerTeam,
			}
		}
	}
	if mt.Map == "" {
		mt.Map = m.chooseMapLocked(group, picked)
	}
	m.recentMap(mt.Map)

	assign := func(ts []*Ticket, team wire.Team) {
		for _, t := range ts {
			t.state = tsMatched
			t.matchID = mt.ID
			mt.tickets = append(mt.tickets, t.ID)
			for _, p := range t.Players {
				p.Team = team
				mt.Players = append(mt.Players, p)
			}
		}
	}
	assign(red, wire.TeamRed)
	assign(blu, wire.TeamBlu)

	m.matches[mt.ID] = mt
	m.log.Info("match formed",
		"match", mt.ID, "group", group.Name, "map", mt.Map,
		"players", len(mt.Players), "red", countSeats(red), "blu", countSeats(blu))
	return mt
}

// warBattleLocked picks the front with the fewest matches on it and returns the
// battle it wants. Nothing here decides what a battle *is*; the theater does.
func (m *Matchmaker) warBattleLocked() (battle war.Battle, front war.Front, ok bool) {
	fronts := m.war.Fronts()
	if len(fronts) == 0 {
		return battle, front, false
	}
	busy := map[string]int{}
	for _, mt := range m.matches {
		if mt.state != msOver && mt.FrontID != "" {
			busy[mt.FrontID]++
		}
	}
	sort.Slice(fronts, func(i, j int) bool {
		if busy[fronts[i].ID] != busy[fronts[j].ID] {
			return busy[fronts[i].ID] < busy[fronts[j].ID]
		}
		return fronts[i].ID < fronts[j].ID
	})
	b, err := m.war.NextBattle(fronts[0].ID)
	if err != nil {
		m.log.Warn("war has no battle for front", "front", fronts[0].ID, "err", err)
		return battle, front, false
	}
	return b, fronts[0], true
}

// chooseMapLocked picks the map every picked party is happy with, preferring
// one that has not been played recently.
//
// Preference is a filter, not a veto: if no map satisfies everyone the union of
// preferences is used, and if nobody expressed one the group's own list is.
func (m *Matchmaker) chooseMapLocked(group config.MatchGroupConfig, picked []*Ticket) string {
	allowed := map[string]bool{}
	for _, name := range group.Maps {
		allowed[name] = true
	}

	counts := map[string]int{}
	voters := 0
	union := map[string]bool{}
	for _, t := range picked {
		if len(t.Maps) == 0 {
			continue
		}
		voters++
		seen := map[string]bool{}
		for _, name := range t.Maps {
			if len(allowed) > 0 && !allowed[name] {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			counts[name]++
			union[name] = true
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
			for name := range union {
				candidates = append(candidates, name)
			}
		}
	}
	if len(candidates) == 0 {
		candidates = append(candidates, group.Maps...)
	}
	if len(candidates) == 0 {
		return ""
	}

	sort.Strings(candidates)
	best := candidates[0]
	bestPlayed := m.lastPlayed(best)
	for _, name := range candidates[1:] {
		if p := m.lastPlayed(name); p.Before(bestPlayed) {
			best, bestPlayed = name, p
		}
	}
	return best
}

func (m *Matchmaker) lastPlayed(mapName string) time.Time {
	if m.recent == nil {
		return time.Time{}
	}
	return m.recent[mapName]
}

func (m *Matchmaker) recentMap(mapName string) {
	if mapName == "" {
		return
	}
	if m.recent == nil {
		m.recent = map[string]time.Time{}
	}
	m.recent[mapName] = m.now()
}
