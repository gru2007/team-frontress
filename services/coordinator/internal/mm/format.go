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

	// The war, when it is on, decides what kind of battle this is before the
	// queue decides who is in it -- because the battlefields that battle can
	// be fought on are what say how many people it takes. An arena skirmish
	// and a twelve-a-side payload push are both legitimate battles for the
	// same node, and only one of them can be filled by six people in queue.
	//
	// Asking once matters: NextBattle is a read, but the battlefield choice
	// downstream is not, and forming a match around one battle and then
	// booting a different one is exactly the class of bug this avoids.
	var battle *warBattle
	if m.war != nil {
		if b, front, ok := m.warBattleLocked(); ok {
			battle = &warBattle{battle: b, front: front}
		}
	}
	sizes := battleSizes(group, battle)

	total := 0
	for _, t := range queue {
		total += t.Size()
	}
	threshold := sizes.ideal
	waited := m.now().Sub(queue[0].queuedAt)
	if group.PatientSecs <= 0 || waited >= time.Duration(group.PatientSecs)*time.Second {
		threshold = sizes.min
	}
	if total < threshold {
		return nil
	}

	// Take tickets oldest-first up to the ceiling. A ticket that does not fit
	// is skipped, not dropped: a party of six should not keep a party of one
	// out of the match after it.
	var picked []*Ticket
	seats := 0
	for _, t := range queue {
		if seats+t.Size() > sizes.max {
			continue
		}
		picked = append(picked, t)
		seats += t.Size()
	}
	if seats < sizes.min {
		return nil
	}

	// Trim newest-first until the teams can be made even. Partitioning cannot
	// always balance an arbitrary set of party sizes, and an unbalanced match
	// is worse than a slightly smaller one.
	for len(picked) > 0 {
		red, blu, ok := splitTeams(picked)
		if ok && countSeats(picked) >= sizes.min {
			return m.buildMatchLocked(group, battle, picked, red, blu)
		}
		picked = picked[:len(picked)-1]
	}
	return nil
}

// warBattle is the battle the war wants, held together with the front it is
// being fought on so the two cannot drift apart between deciding and building.
type warBattle struct {
	battle war.Battle
	front  war.Front
}

// matchSizes is how big a match may be. It is the group's own numbers, widened
// to whatever the battlefield pool can accommodate.
type matchSizes struct{ min, ideal, max int }

// battleSizes widens the group's thresholds to cover every battlefield the
// battle could be fought on: the smallest min anything in the pool will accept,
// the largest ideal anything will make use of, the largest max anything allows.
//
// Widening rather than replacing is deliberate. The pool says what is possible;
// which of those the queue can actually fill is decided afterwards, once the
// seats are known, in fieldFor.
func battleSizes(group config.MatchGroupConfig, b *warBattle) matchSizes {
	s := matchSizes{min: group.MinPlayers, ideal: group.IdealPlayers, max: group.MaxPlayers}
	if b == nil || len(b.battle.Fields) == 0 {
		return s
	}
	first := true
	for _, f := range b.battle.Fields {
		fs := fieldSizes(group, f)
		if first {
			s = fs
			first = false
			continue
		}
		if fs.min < s.min {
			s.min = fs.min
		}
		if fs.ideal > s.ideal {
			s.ideal = fs.ideal
		}
		if fs.max > s.max {
			s.max = fs.max
		}
	}
	return s
}

// fieldSizes is one battlefield's own numbers, with the group's standing in
// for anything it did not set.
func fieldSizes(group config.MatchGroupConfig, f war.Battlefield) matchSizes {
	s := matchSizes{min: f.MinPlayers, ideal: f.IdealPlayers, max: f.MaxPlayers}
	if s.min <= 0 {
		s.min = group.MinPlayers
	}
	if s.ideal <= 0 {
		s.ideal = group.IdealPlayers
	}
	if s.max <= 0 {
		s.max = group.MaxPlayers
	}
	if s.ideal < s.min {
		s.ideal = s.min
	}
	if s.max < s.ideal {
		s.max = s.ideal
	}
	return s
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
func (m *Matchmaker) buildMatchLocked(group config.MatchGroupConfig, battle *warBattle, picked, red, blu []*Ticket) *Match {
	mt := &Match{
		ID:         m.newID(),
		MatchGroup: group.MatchGroup,
		MaxPlayers: group.MaxPlayers,
		Password:   randomID()[:8],
		state:      msBooting,
		createdAt:  m.now(),
	}

	attackerTeam := wire.TeamUnassigned
	if battle != nil {
		b, front := battle.battle, battle.front

		// The war says what kind of battle and where it may be fought; which
		// of those maps it actually is belongs here, with the half that knows
		// how many people turned up and which maps they voted for.
		field := fieldFor(group, b.Fields, countSeats(picked))
		mt.Map = m.chooseMapLocked(fieldNames(candidateFields(group, b.Fields, countSeats(picked))), picked)
		if mt.Map == "" {
			mt.Map = field.Map
		} else {
			field = fieldByName(b.Fields, mt.Map, field)
		}
		mt.MaxPlayers = fieldSizes(group, field).max

		mt.FrontID = front.ID
		attackerTeam = wire.Team(b.AttackerTeam)
		if field.AttackerTeam != 0 {
			attackerTeam = wire.Team(field.AttackerTeam)
		}
		mt.War = &wire.WarBriefing{
			FrontID:      front.ID,
			NodeID:       front.NodeID,
			NodeName:     b.NodeName,
			StageIndex:   b.StageIndex,
			StageCount:   b.StageCount,
			StageKind:    b.Stage.Kind,
			BattleMode:   field.Mode,
			AttackerWar:  string(b.Attacker),
			AttackerTeam: attackerTeam,
		}
	}
	if mt.Map == "" {
		mt.Map = m.chooseMapLocked(group.EffectiveMaps(), picked)
	}
	m.recentMap(mt.Map)

	assign := func(ts []*Ticket, team wire.Team) {
		for _, t := range ts {
			t.state = tsMatched
			t.matchID = mt.ID
			mt.tickets = append(mt.tickets, t.ID)
			// Index, not range, so the ticket knows its own team too. See
			// the note in addToMatchLocked.
			for i := range t.Players {
				t.Players[i].Team = team
				mt.Players = append(mt.Players, t.Players[i])
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

// candidateFields narrows a battle's pool to the battlefields this many players
// can actually fill. If none of them fits -- a queue that settled below every
// battlefield's floor, or grew past every ceiling -- the whole pool is
// returned rather than nothing: a battle on a slightly wrong-sized map beats
// no battle at all.
func candidateFields(group config.MatchGroupConfig, fields []war.Battlefield, seats int) []war.Battlefield {
	var fits []war.Battlefield
	for _, f := range fields {
		fs := fieldSizes(group, f)
		if seats >= fs.min && seats <= fs.max {
			fits = append(fits, f)
		}
	}
	if len(fits) == 0 {
		return fields
	}
	return fits
}

// fieldFor is candidateFields' first answer, for when only one is wanted.
func fieldFor(group config.MatchGroupConfig, fields []war.Battlefield, seats int) war.Battlefield {
	c := candidateFields(group, fields, seats)
	if len(c) == 0 {
		return war.Battlefield{}
	}
	return c[0]
}

func fieldNames(fields []war.Battlefield) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Map)
	}
	return out
}

// fieldByName finds the battlefield the map choice landed on, so its mode and
// attacker team travel with it. Falls back to def for a map that came from
// somewhere else.
func fieldByName(fields []war.Battlefield, name string, def war.Battlefield) war.Battlefield {
	for _, f := range fields {
		if f.Map == name {
			return f
		}
	}
	return def
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
func (m *Matchmaker) chooseMapLocked(pool []string, picked []*Ticket) string {
	allowed := map[string]bool{}
	for _, name := range pool {
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
		candidates = append(candidates, pool...)
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
