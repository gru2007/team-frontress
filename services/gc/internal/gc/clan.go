package gc

import (
	"errors"
	"strconv"

	"github.com/gru2007/team-frontress/services/gc/internal/clans"
	"github.com/gru2007/team-frontress/services/gc/internal/players"
)

// Clans, as the game sees them: ops in, the clan's document out. The rules
// live in package clans; this is where they meet players, matches and the
// war.

var errNoFront = errors.New("no_such_front")

func (g *GC) handleClan(id uint64, op socialOp) {
	if g.clans == nil {
		g.fail(id, "no_clans")
		return
	}
	who, _ := strconv.ParseUint(op.Who, 10, 64)
	before := g.clanKey(id)
	var err error
	switch op.Op {
	case "clan_browse":
		g.sendClan(id)
		return
	case "clan_create":
		var c *clans.Clan
		c, err = g.clans.Create(id, g.nameOf(id), clans.Settings{Name: op.Name, Tag: op.Tag, Motto: op.Motto,
			Emblem: op.Emblem, Color: op.Color, Open: op.Open})
		if err == nil {
			g.feed("clan", "fui_feed_clan_new", c.Tag, c.Name)
		}
	case "clan_join":
		err = g.clans.Join(id, g.nameOf(id), op.ID)
		if err == nil {
			g.clanFeed(op.ID, "fui_feed_clan_joined", g.nameOf(id))
		}
	case "clan_decline":
		g.clans.Decline(id, op.ID)
	case "clan_leave":
		if c := g.clans.Of(id); c != nil {
			if err = g.clans.Leave(id); err == nil {
				g.clanFeed(c.ID, "fui_feed_clan_left", g.nameOf(id))
				g.sendClanToMembers(c.ID)
			}
		}
	case "clan_invite":
		if err = g.clans.Invite(id, who); err == nil {
			g.sendClan(who)
			if c := g.clans.Of(id); c != nil {
				g.emit(who, "toast", map[string]any{"kind": "clan", "key": "fui_toast_clan_invite", "args": []string{c.Tag, c.Name}})
			}
		}
	case "clan_kick":
		c := g.clans.Of(id)
		if err = g.clans.Kick(id, who); err == nil && c != nil {
			g.clanFeed(c.ID, "fui_feed_clan_kicked", g.nameOf(who))
			g.sendClan(who)
		}
	case "clan_role":
		err = g.clans.SetRole(id, who, clans.Role(op.Role))
	case "clan_settings":
		err = g.clans.Configure(id, clans.Settings{Motto: op.Motto, Emblem: op.Emblem, Color: op.Color, Open: op.Open})
	case "clan_order":
		if op.Front != "" && g.war != nil && !g.frontExists(op.Front) {
			err = errNoFront
			break
		}
		if err = g.clans.SetOrder(id, op.Front); err == nil && op.Front != "" {
			if c := g.clans.Of(id); c != nil {
				g.clanFeed(c.ID, "fui_feed_clan_order", g.frontName(op.Front))
			}
		}
	default:
		return
	}
	if err != nil {
		g.fail(id, err.Error())
		return
	}
	// Everyone the change touched gets the clan again.
	if c := g.clans.Of(id); c != nil {
		g.sendClanToMembers(c.ID)
	} else {
		g.sendClan(id)
	}
	if before != "" && before != g.clanKey(id) {
		g.sendClanToMembers(before[len("clan:"):])
	}
	g.presenceLast = "" // tags changed
}

func (g *GC) nameOf(id uint64) string {
	if p := g.online[id]; p != nil && p.Name != "" {
		return p.Name
	}
	return g.players.Get(id).Name
}

func (g *GC) frontExists(front string) bool {
	if g.war == nil {
		return false
	}
	for _, f := range g.war.Fronts() {
		if f.ID == front {
			return true
		}
	}
	return false
}

// frontName is the sector an operation is on, by name.
func (g *GC) frontName(front string) string {
	if g.war == nil {
		return front
	}
	snap := g.war.Snapshot()
	for _, f := range snap.Fronts {
		if f.ID == front {
			for _, n := range snap.Theater.Nodes {
				if n.ID == f.NodeID {
					return n.Name
				}
			}
		}
	}
	return front
}

func (g *GC) sendClanToMembers(clan string) {
	c := g.clans.Get(clan)
	if c == nil {
		return
	}
	for id := range c.Members {
		g.sendClan(id)
	}
}

// sendClan sends a player their clan (or none), their invites and the clans
// they could join.
func (g *GC) sendClan(id uint64) {
	if g.clans == nil || !g.isOnline(id) {
		return
	}
	doc := map[string]any{"clan": nil}
	if c := g.clans.Of(id); c != nil {
		doc["clan"] = g.clanDoc(c, id)
	}
	invites := []map[string]any{}
	for _, c := range g.clans.InvitesFor(id) {
		invites = append(invites, g.clanCard(c))
	}
	doc["invites"] = invites
	top := []map[string]any{}
	for _, c := range g.clans.Top(30) {
		top = append(top, g.clanCard(c))
	}
	doc["top"] = top
	g.emit(id, "clan", doc)
}

// clanCard is a clan as a line in a list.
func (g *GC) clanCard(c *clans.Clan) map[string]any {
	online := 0
	for id := range c.Members {
		if g.isOnline(id) {
			online++
		}
	}
	sector, _ := c.Banner()
	return map[string]any{
		"id": c.ID, "name": c.Name, "tag": c.Tag, "motto": c.Motto, "emblem": c.Emblem, "color": c.Color,
		"open": c.Open, "level": c.Level(), "xp": c.XP, "count": len(c.Members), "cap": c.Cap(), "online": online,
		"wins": c.Wins, "battles": c.Battles, "banner": g.nodeName(sector), "gold": clans.Has(c.Level(), "gold"),
	}
}

func (g *GC) nodeName(id string) string {
	if id == "" || g.war == nil {
		return ""
	}
	for _, n := range g.war.Snapshot().Theater.Nodes {
		if n.ID == id {
			return n.Name
		}
	}
	return ""
}

// clanDoc is the whole clan, for its members.
func (g *GC) clanDoc(c *clans.Clan, me uint64) map[string]any {
	doc := g.clanCard(c)
	lvl := c.Level()
	from, to := clans.LevelSpan(lvl)
	frac := 1.0
	if to > from {
		frac = float64(c.XP-from) / float64(to-from)
	}
	doc["xp_from"], doc["xp_to"], doc["frac"] = from, to, frac

	members := []map[string]any{}
	for _, m := range c.Roster() {
		status := "offline"
		if g.isOnline(m.SteamID) {
			status = "menu"
			if l := g.lobbyOf(m.SteamID); l != nil {
				status = "match"
			} else if p := g.online[m.SteamID]; p != nil {
				if party := g.parties[p.PartyID]; party != nil && len(party.Queues) > 0 {
					status = "queue"
				}
			}
		}
		name := m.Name
		if n := g.nameOf(m.SteamID); n != "" {
			name = n
		}
		members = append(members, map[string]any{
			"id": strconv.FormatUint(m.SteamID, 10), "name": name, "role": string(m.Role), "status": status,
			"online": status != "offline", "xp": m.XP, "week_xp": m.WeekXP, "level": g.players.Get(m.SteamID).Level(),
			"me": m.SteamID == me,
		})
		if m.SteamID == me {
			doc["role"] = string(m.Role)
		}
	}
	doc["members"] = members

	perks := []map[string]any{}
	for _, p := range clans.Perks {
		perks = append(perks, map[string]any{"id": p.ID, "level": p.Level, "on": lvl >= p.Level})
	}
	doc["perks"] = perks

	ct := clans.ContractFor(c.Week)
	cfrac := float64(c.Progress) / float64(ct.Target)
	if cfrac > 1 {
		cfrac = 1
	}
	doc["contract"] = map[string]any{
		"kind": ct.Kind, "target": ct.Target, "progress": min(c.Progress, ct.Target), "frac": cfrac, "paid": c.Paid,
		"left": int(clans.WeekEnds(c.Week).Sub(g.now()).Seconds()), "on": clans.Has(lvl, "contract"),
		"clan_xp": clans.ContractClanXP, "member_xp": clans.ContractMemberXP,
	}
	doc["order"] = c.Order
	doc["order_name"] = ""
	if c.Order != "" {
		doc["order_name"] = g.frontName(c.Order)
	}
	doc["order_xp"] = clans.OrderBonusXP
	bonus := 0
	if clans.Has(lvl, "veterans") {
		bonus = clans.VeteransBonus
	} else if clans.Has(lvl, "brothers") {
		bonus = clans.BrothersBonus
	}
	doc["bonus"] = bonus
	return doc
}

// creditClans pays clans and their members for a finished match: clan XP,
// the brothers-in-arms bonus, the clan order, the weekly contract.
func (g *GC) creditClans(l *Lobby, results map[uint64]players.Result) {
	if g.clans == nil {
		return
	}
	var ps []clans.Played
	for id, res := range results {
		p := clans.Played{SteamID: id, Won: res == players.Win}
		if l.War != nil {
			p.War, p.Sector, p.Front = true, l.War.NodeID, l.War.FrontID
		}
		ps = append(ps, p)
	}
	match := strconv.FormatUint(l.MatchID, 10)
	touched := map[string]bool{}
	for id, e := range g.clans.RecordMatch(ps) {
		touched[e.Clan] = true
		if e.BonusPct > 0 {
			xp := players.XPPerMatch
			if results[id] == players.Win {
				xp += players.XPWinBonus
			}
			g.players.Bonus(id, "clanmates:"+match, xp*e.BonusPct/100)
		}
		if e.Order {
			g.players.Bonus(id, "clanorder:"+match, clans.OrderBonusXP)
		}
		c := g.clans.Get(e.Clan)
		if c == nil {
			continue
		}
		if e.Levelled > 0 {
			g.feed("clan", "fui_feed_clan_level", c.Tag, strconv.Itoa(e.Levelled))
		}
		if e.Contract {
			week := strconv.Itoa(c.Week)
			for _, m := range g.clans.ContractPaidTo(c.ID) {
				g.players.Bonus(m, "clancontract:"+c.ID+":"+week, clans.ContractMemberXP)
			}
			g.feed("clan", "fui_feed_contract", c.Tag)
		}
	}
	for clan := range touched {
		g.sendClanToMembers(clan)
	}
}
