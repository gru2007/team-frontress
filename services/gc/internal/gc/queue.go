package gc

import (
	"sort"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func init() {
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch, (*GC).handleQueue)
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_RemoveFromQueue, (*GC).handleUnqueue)
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForStandby, (*GC).handleStandby)
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_RemoveFromStandbyQueue, (*GC).handleUnstandby)
}

func (g *GC) handleQueue(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyQueueForMatch
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatchResponse), &gcpb.CMsgPartyQueueForMatchResponse{})
	if !g.parse(m, &msg) {
		return
	}
	p := g.partyOf(s, msg.GetPartyId())
	if p == nil || s.SteamID != p.Leader {
		return
	}
	// The final options travel with the request so the queue never runs on
	// criteria the GC has not seen yet.
	if fo := msg.GetFinalOptions(); fo != nil && fo.GetGroupCriteria() != nil {
		p.Criteria = fo.GetGroupCriteria()
	}
	grp, ok := g.cfg.Group(int32(msg.GetMatchGroup()))
	if !ok {
		g.notice(s.SteamID, "That mode is not available on this coordinator.")
		g.publishParty(p)
		return
	}
	if err := g.admissible(p, grp); err != nil {
		g.notice(s.SteamID, "Could not start searching: "+err.Error()+".")
		g.publishParty(p) // the client predicted "queued"; correct it
		return
	}
	if _, already := p.Queues[grp.MatchGroup]; !already {
		p.Queues[grp.MatchGroup] = g.now()
		g.log.Info("party queued", "party", p.ID, "group", grp.Name, "players", len(p.Members), "steamids", p.Members)
	}
	g.publishParty(p)
}

func (g *GC) handleUnqueue(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyRemoveFromQueue
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCParty_RemoveFromQueueResponse), &gcpb.CMsgPartyRemoveFromQueueResponse{})
	if !g.parse(m, &msg) {
		return
	}
	p := g.partyOf(s, msg.GetPartyId())
	if p == nil {
		return
	}
	if _, queued := p.Queues[int32(msg.GetMatchGroup())]; queued {
		g.log.Debug("party left the queue", "party", p.ID, "group", msg.GetMatchGroup(), "steamid", s.SteamID, "steamids", p.Members)
	}
	delete(p.Queues, int32(msg.GetMatchGroup()))
	g.publishParty(p)
}

// handleStandby is a party member asking to be let into the match the rest of
// the party is playing.
func (g *GC) handleStandby(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyQueueForStandby
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForStandbyResponse), &gcpb.CMsgPartyQueueForStandbyResponse{})
	if !g.parse(m, &msg) {
		return
	}
	p := g.partyOf(s, msg.GetPartyId())
	if p == nil || g.lobbyOf(s.SteamID) != nil {
		return
	}
	p.Standby[s.SteamID] = true
	g.publishParty(p)
	g.seatStandby(p)
}

func (g *GC) handleUnstandby(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyRemoveFromStandbyQueue
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCParty_RemoveFromStandbyQueueResponse), &gcpb.CMsgPartyRemoveFromStandbyQueueResponse{})
	if !g.parse(m, &msg) {
		return
	}
	if p := g.partyOf(s, msg.GetPartyId()); p != nil {
		delete(p.Standby, s.SteamID)
		g.publishParty(p)
	}
}

// seatStandby puts the party's standby members into the party's match if it
// has room. Whoever does not fit stays on standby and is tried again.
func (g *GC) seatStandby(p *Party) {
	l := g.partyLobby(p)
	if l == nil || l.State != lobbyRun {
		return
	}
	srv := g.lateJoinServer(l)
	if srv == nil {
		return
	}
	grp, ok := g.cfg.Group(l.Group)
	if !ok {
		return
	}
	changed := false
	for id := range p.Standby {
		if !g.isOnline(id) || g.lobbyOf(id) != nil {
			delete(p.Standby, id)
			changed = true
			continue
		}
		if srv.LateJoinSlots == 0 || l.kicked[id] {
			continue
		}
		red, blu := l.teamCounts()
		team, ok := mm.RoomFor(red, blu, 1, l.teamCap(grp))
		if l.War != nil {
			team, ok = g.factionTeam(l, id)
			if (team == mm.Red && red >= l.teamCap(grp)) || (team == mm.Blu && blu >= l.teamCap(grp)) {
				ok = false
			}
		}
		if !ok {
			continue
		}
		l.addMember(id, team, p.ID, g.now())
		srv.LateJoinSlots--
		delete(p.Standby, id)
		changed = true
		g.log.Info("standby seated", "lobby", l.ID, "steamid", id)
	}
	if changed {
		g.lobbyChanged(l)
		g.publishParty(p)
	}
}

// partyLobby is the running match of any member of a party.
func (g *GC) partyLobby(p *Party) *Lobby {
	if l := g.lobbyOf(p.Leader); l != nil {
		return l
	}
	for _, m := range p.Members {
		if l := g.lobbyOf(m); l != nil {
			return l
		}
	}
	return nil
}

// --- matchmaking --------------------------------------------------------------

// matchmake runs every tick: first it fills running matches, then it forms
// new ones from whoever is left.
func (g *GC) matchmake() {
	for _, grp := range g.cfg.MatchGroups {
		if !grp.Enabled {
			continue
		}
		queue := g.queueFor(grp)
		if len(queue) == 0 {
			continue
		}
		if grp.Backfills() {
			queue = g.backfill(grp, queue)
		}
		// A match is only formed when a server is free to play it: nobody is
		// told "match found" and then left waiting for one. Each region
		// matches its own players on its own servers (region.go), and a
		// server only takes the players who reach it (probe.go). The servers
		// the queue pings best go first.
		for _, pool := range g.regionPools(grp, queue) {
			entries := pool.entries
			free := g.freeServersIn(grp, pool.region)
			g.byPing(free, entryIDs(entries))
			for _, srv := range free {
				if len(entries) == 0 {
					break
				}
				if !g.serverEligible(srv, g.now()) {
					continue // taken by a match formed this tick
				}
				var reach []mm.Entry
				for _, e := range entries {
					if g.entryReaches(e, srv) {
						reach = append(reach, e)
					}
				}
				if len(reach) == 0 {
					continue
				}
				formed, sizes, battle, ok := g.form(grp, reach)
				if !ok {
					continue
				}
				g.createLobby(grp, formed, sizes, battle, pool.region, srv)
				entries = remaining(entries, formed)
			}
		}
	}
	for _, p := range g.parties {
		if len(p.Standby) > 0 {
			g.seatStandby(p)
		}
	}
}

// queueFor is everybody queued for a group who can play right now, oldest
// first.
func (g *GC) queueFor(grp config.MatchGroupConfig) []mm.Entry {
	var out []mm.Entry
	for _, p := range g.parties {
		at, queued := p.Queues[grp.MatchGroup]
		if !queued {
			continue
		}
		if g.admissible(p, grp) != nil {
			continue
		}
		ready := true
		e := mm.Entry{PartyID: p.ID, QueuedAt: at, Maps: g.partyMaps(p)}
		for _, id := range p.Members {
			if !g.isOnline(id) || g.lobbyOf(id) != nil {
				ready = false
				break
			}
			e.Players = append(e.Players, mm.Player{SteamID: id, Rating: g.players.Get(id).Rating})
		}
		if ready {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].QueuedAt.Equal(out[j].QueuedAt) {
			return out[i].QueuedAt.Before(out[j].QueuedAt)
		}
		return out[i].PartyID < out[j].PartyID
	})
	return out
}

// partyMaps turns the casual panel's map bitfield into names, using the
// configured def index table.
func (g *GC) partyMaps(p *Party) []string {
	if len(g.cfg.MapIndex) == 0 || p.Criteria.GetCasualCriteria() == nil {
		return nil
	}
	bits := p.Criteria.GetCasualCriteria().GetSelectedMapsBits()
	var out []string
	for name, idx := range g.cfg.MapIndex {
		word, bit := idx/32, idx%32
		if int(word) < len(bits) && bits[word]&(1<<bit) != 0 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func entryIDs(es []mm.Entry) []uint64 {
	var out []uint64
	for _, e := range es {
		for _, p := range e.Players {
			out = append(out, p.SteamID)
		}
	}
	return out
}

func remaining(queue []mm.Entry, f mm.Formed) []mm.Entry {
	used := map[uint64]bool{}
	for _, e := range f.Red {
		used[e.PartyID] = true
	}
	for _, e := range f.Blu {
		used[e.PartyID] = true
	}
	out := queue[:0:0]
	for _, e := range queue {
		if !used[e.PartyID] {
			out = append(out, e)
		}
	}
	return out
}

// backfill fills the most populated matches first. A frontline search
// includes running matches by default and honors an explicit opt-out.
func (g *GC) backfill(grp config.MatchGroupConfig, queue []mm.Entry) []mm.Entry {
	open := g.openLobbies(grp)
	if len(open) == 0 {
		return queue
	}
	var left []mm.Entry
	for _, e := range queue {
		p := g.parties[e.PartyID]
		if p == nil || (p.Criteria != nil && p.Criteria.LateJoinOk != nil && !p.Criteria.GetLateJoinOk()) {
			left = append(left, e)
			continue
		}
		seated := false
		for _, l := range open {
			srv := g.servers[l.ServerID]
			if e.Size() > srv.LateJoinSlots || !g.lobbyTakes(l, e) {
				continue
			}
			blocked := false
			for _, player := range e.Players {
				blocked = blocked || l.kicked[player.SteamID]
			}
			if blocked {
				continue
			}
			red, blu := l.teamCounts()
			team, ok := mm.RoomFor(red, blu, e.Size(), l.teamCap(grp))
			if l.War != nil {
				team, ok = g.factionTeam(l, e.Players[0].SteamID)
				if (team == mm.Red && red+e.Size() > l.teamCap(grp)) || (team == mm.Blu && blu+e.Size() > l.teamCap(grp)) {
					ok = false
				}
			}
			if !ok {
				continue
			}
			for _, pl := range e.Players {
				l.addMember(pl.SteamID, team, p.ID, g.now())
			}
			srv.LateJoinSlots -= e.Size()
			delete(p.Queues, grp.MatchGroup)
			g.lobbyChanged(l)
			g.publishParty(p)
			g.log.Info("party backfilled", "party", p.ID, "lobby", l.ID, "players", e.Size())
			seated = true
			break
		}
		if !seated {
			left = append(left, e)
		}
	}
	return left
}

func (g *GC) openLobbies(grp config.MatchGroupConfig) []*Lobby {
	now := g.now()
	var out []*Lobby
	for _, l := range g.lobbies {
		if l.Group != grp.MatchGroup || l.State != lobbyRun || g.lateJoinServer(l) == nil {
			continue
		}
		if grp.BackfillSecs > 0 && now.Sub(l.StartedAt) > time.Duration(grp.BackfillSecs)*time.Second {
			continue
		}
		red, blu := l.teamCounts()
		if red+blu >= l.MaxPlayers {
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, bi := out[i].teamCounts()
		rj, bj := out[j].teamCounts()
		if ri+bi != rj+bj {
			return ri+bi > rj+bj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (g *GC) lateJoinServer(l *Lobby) *Server {
	srv := g.servers[l.ServerID]
	if l.resultSeen || srv == nil || srv.LobbyID != l.ID || srv.Session == nil ||
		srv.Session.Closed() || srv.LateJoinSlots <= 0 {
		return nil
	}
	return srv
}

// form asks the arithmetic for a match, sized by the war's battle if this
// group fights the war.
func (g *GC) form(grp config.MatchGroupConfig, queue []mm.Entry) (mm.Formed, mm.Sizes, *battle, bool) {
	sizes := mm.Sizes{Min: grp.MinPlayers, Ideal: grp.IdealPlayers, Max: grp.MaxPlayers}
	b := g.nextBattle(grp, queue)
	if g.cfg.FightsWar(grp) && b == nil {
		return mm.Formed{}, sizes, nil, false
	}
	if b != nil {
		sizes = mm.BattleSizes(sizes, b.Battle.Fields)
	}
	patience := time.Duration(grp.PatientSecs) * time.Second
	if b != nil {
		f, ok := g.formFactions(queue, sizes, patience)
		if ok || !waitedOut(queue, patience, g.now()) {
			return f, sizes, b, ok
		}
		// A battle needs both factions. When the queue cannot field them
		// (everybody waiting is RED, say), it plays an ordinary match rather
		// than wait forever.
		sizes = mm.Sizes{Min: grp.MinPlayers, Ideal: grp.IdealPlayers, Max: grp.MaxPlayers}
		f, ok = mm.Form(queue, sizes, patience, g.now())
		return f, sizes, nil, ok
	}
	f, ok := mm.Form(queue, sizes, patience, g.now())
	return f, sizes, b, ok
}

// waitedOut reports whether the longest-waiting entry has waited past the
// group's patience.
func waitedOut(queue []mm.Entry, patience time.Duration, now time.Time) bool {
	for _, e := range queue {
		if patience <= 0 || now.Sub(e.QueuedAt) >= patience {
			return true
		}
	}
	return false
}
