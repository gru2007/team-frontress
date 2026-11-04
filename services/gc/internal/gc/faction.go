package gc

import (
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/war"
)

func (g *GC) warSide(id uint64) war.Side {
	if g.war == nil {
		return ""
	}
	r := g.players.Get(id)
	if r.WarCampaign != g.war.Campaign() {
		return ""
	}
	return war.Side(r.WarSide)
}

func (g *GC) sendFaction(id uint64) {
	if g.war == nil {
		return
	}
	s := g.war.Snapshot()
	restartAt := int64(0)
	if s.EndedAt > 0 {
		restartAt = s.EndedAt + 86400
	}
	g.emit(id, "war_faction", map[string]any{"side": strings.ToLower(string(g.warSide(id))), "campaign": s.Campaign, "winner": strings.ToLower(string(s.Winner)), "restart_at": restartAt})
}

func (g *GC) chooseFaction(id uint64, side string) {
	if g.war == nil || g.war.Snapshot().Winner != "" {
		return
	}
	if g.lobbyOf(id) != nil {
		g.notice(id, "Finish your current match before choosing a faction.")
		return
	}
	if p := g.parties[g.player(id).PartyID]; p != nil && len(p.Queues) > 0 {
		g.notice(id, "Leave matchmaking before choosing a faction.")
		return
	}
	if err := g.players.ChooseWarSide(id, g.war.Campaign(), strings.ToUpper(side)); err != nil {
		g.notice(id, err.Error())
	}
	g.sendFaction(id)
}

func (g *GC) factionTeam(l *Lobby, id uint64) (mm.Team, bool) {
	if l.War == nil {
		return mm.Red, false
	}
	if g.war == nil || g.war.Snapshot().Winner != "" {
		return mm.Red, false
	}
	side := g.warSide(id)
	if side == "" || l.War.Campaign != g.war.Campaign() {
		return mm.Red, false
	}
	t := mm.Team(l.War.AttackerTeam)
	if string(side) != l.War.Attacker {
		if t == mm.Red {
			t = mm.Blu
		} else {
			t = mm.Red
		}
	}
	return t, true
}

func (g *GC) stopWarQueues() {
	for _, p := range g.parties {
		changed := false
		for group := range p.Queues {
			if grp, ok := g.cfg.Group(group); ok && g.cfg.FightsWar(grp) {
				delete(p.Queues, group)
				changed = true
			}
		}
		if lobby := g.partyLobby(p); lobby != nil && lobby.War != nil {
			clear(p.Standby)
			changed = true
		}
		if changed {
			g.publishParty(p)
		}
	}
	for id := range g.online {
		g.sendFaction(id)
	}
}

// formFactions preserves parties and allegiance. Dynamic programming finds
// equal team sizes without moving somebody to the other campaign faction.
func (g *GC) formFactions(queue []mm.Entry, sizes mm.Sizes, patience time.Duration) (mm.Formed, bool) {
	threshold := sizes.Ideal
	if len(queue) == 0 {
		return mm.Formed{}, false
	}
	if patience <= 0 || g.now().Sub(queue[0].QueuedAt) >= patience {
		threshold = sizes.Min
	}
	red, blu := map[int][]mm.Entry{0: {}}, map[int][]mm.Entry{0: {}}
	for _, e := range queue {
		if len(e.Players) == 0 {
			continue
		}
		dp := red
		if g.warSide(e.Players[0].SteamID) == war.SideBLU {
			dp = blu
		}
		for n := sizes.Max/2 - e.Size(); n >= 0; n-- {
			if picked, ok := dp[n]; ok {
				if _, exists := dp[n+e.Size()]; !exists {
					dp[n+e.Size()] = append(append([]mm.Entry{}, picked...), e)
				}
			}
		}
	}
	for n := sizes.Max / 2; n*2 >= threshold; n-- {
		r, rok := red[n]
		b, bok := blu[n]
		if rok && bok {
			return mm.Formed{Red: r, Blu: b}, true
		}
	}
	return mm.Formed{}, false
}
