package gc

import (
	"strconv"

	"github.com/gru2007/team-frontress/services/gc/internal/history"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
)

func (g *GC) History() *history.Store { return g.history }

// archiveMatch runs on the loop. Former participants and a received result
// survive roster removals, backfill and the destruction of the live lobby.
func (g *GC) archiveMatch(l *Lobby, result *history.Result, reason string) {
	id := strconv.FormatUint(l.MatchID, 10)
	m, _ := g.history.Get(id)
	m.MatchID, m.Group, m.Map, m.State, m.FormedAt = id, l.Group, l.Map, string(l.State), l.FormedAt
	if l.War != nil {
		m.Campaign, m.UniformSwap = l.War.Campaign, l.War.UniformSwap
	}
	if grp, ok := g.cfg.Group(l.Group); ok {
		m.GroupName = grp.Name
		if grp.Region != "" {
			m.Region = grp.Region
		}
	}
	if srv := g.servers[l.ServerID]; srv != nil && srv.Region != "" {
		m.Region = srv.Region
	}
	if !l.StartedAt.IsZero() {
		at := l.StartedAt
		m.StartedAt = &at
	}
	if result != nil {
		m.Result = result
	}
	if reason != "" {
		at := g.now()
		m.State, m.Reason, m.EndedAt = "ended", reason, &at
	}
	for i := range m.Members {
		m.Members[i].Left = true
	}
	for _, member := range l.Members {
		sid := strconv.FormatUint(member.SteamID, 10)
		team := "red"
		if member.Team == mm.Blu {
			team = "blu"
		}
		entry := history.Member{SteamID: sid, Name: g.players.Get(member.SteamID).Name, Team: team}
		found := false
		for i := range m.Members {
			if m.Members[i].SteamID == sid {
				m.Members[i] = entry
				found = true
				break
			}
		}
		if !found {
			m.Members = append(m.Members, entry)
		}
	}
	if err := g.history.Put(m); err != nil {
		g.log.Error("could not archive match", "match", id, "err", err)
	}
}
