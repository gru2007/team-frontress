package gc

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// Server pings. A server being in a player's region does not mean the player
// can play on it: some networks cannot reach some hosts at all (players in
// Russia and a server in Canada, say), and a party that waited out its
// region may be matched anywhere. So the GC asks every game to ping the pool
// itself (msgServerList), the game answers with what it measured
// (msgServerPings), and a match only goes to a server that every one of its
// players reaches within server_ping.max_ms.
//
// A game says it can ping in its region message ({"ping": 1}, region.go).
// One that never says so -- an older build -- is not held back.
//
// The ping limit is a preference, not a wall: a queued party that no server
// suits -- one of them reaches none within the limit, or no server suits all
// of them -- is not left waiting for nothing. Once a second measurement
// agrees, it plays on the best server there is (checkPartyReach), the lowest
// average ping first, and its members are told so: a game that says
// {"ping": 2} gets msgUnreachable, an older one a line of text.
const (
	msgServerList  uint32 = 51021 // GC -> client: {"regions": [{id, name}], "servers": [{id, addr, region}]}
	msgServerPings uint32 = 51022 // client -> GC: {"pings": [{id, ms, ok}]}
	msgUnreachable uint32 = 51023 // GC -> client: the party plays on the best server there is, see reachReport
)

const (
	probeRefresh = 10 * time.Minute // measure again this often while online
	probeMinGap  = 10 * time.Second // never ask one player more often than this
)

// serverPing is what a player's game measured to one server.
type serverPing struct {
	Addr string // the address that was pinged; a server that moved is unmeasured
	MS   int    // -1: answered, but the game could not time it
	OK   bool   // the server answered
	At   time.Time
}

func init() {
	clientHandlers[msgServerPings] = func(g *GC, s *session.Session, m *wire.Msg) {
		var body gcpb.CMsgPartyChatMsg
		if !g.parse(m, &body) {
			return
		}
		var res struct {
			Pings []struct {
				ID string `json:"id"`
				MS int    `json:"ms"`
				OK bool   `json:"ok"`
			} `json:"pings"`
		}
		if len(body.GetMsg()) > 16384 || json.Unmarshal([]byte(body.GetMsg()), &res) != nil {
			return
		}
		p := g.player(s.SteamID)
		if p.Pings == nil {
			p.Pings = map[uint64]serverPing{}
		}
		now := g.now()
		for _, r := range res.Pings {
			id, _ := strconv.ParseUint(r.ID, 10, 64)
			addr, asked := p.probeAddrs[id]
			if !asked {
				continue
			}
			ms := r.MS
			if ms < 0 || ms > 10000 {
				ms = -1
			}
			p.Pings[id] = serverPing{Addr: addr, MS: ms, OK: r.OK, At: now}
		}
		p.CanPing = true
		p.probeAnswered = true
	}
}

// probeList is every pool server a game could be sent to, by SteamID.
func (g *GC) probeList() map[uint64]*Server {
	out := map[uint64]*Server{}
	for id, srv := range g.servers {
		if srv.Session != nil && !srv.Session.Closed() && srv.Connect != "" {
			out[id] = srv
		}
	}
	return out
}

func probeKey(list map[uint64]*Server) string {
	ids := make([]uint64, 0, len(list))
	for id := range list {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(strconv.FormatUint(id, 10))
		b.WriteByte('=')
		b.WriteString(list[id].Connect)
		b.WriteByte(';')
	}
	return b.String()
}

// sendProbe asks a player's game to ping the pool.
func (g *GC) sendProbe(p *Player, list map[uint64]*Server, key string) {
	type region struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type server struct {
		ID     string `json:"id"`
		Addr   string `json:"addr"`
		Region string `json:"region,omitempty"`
	}
	doc := struct {
		Regions []region `json:"regions"`
		Servers []server `json:"servers"`
		MaxMS   int      `json:"max_ms"`
	}{Regions: []region{}, Servers: []server{}, MaxMS: g.cfg.ServerPing.Max()}
	for _, r := range g.cfg.Regions {
		doc.Regions = append(doc.Regions, region{ID: r.ID, Name: r.Name})
	}
	p.probeAddrs = make(map[uint64]string, len(list))
	for id, srv := range list {
		doc.Servers = append(doc.Servers, server{ID: strconv.FormatUint(id, 10), Addr: srv.Connect, Region: srv.Region})
		p.probeAddrs[id] = srv.Connect
	}
	sort.Slice(doc.Servers, func(i, j int) bool { return doc.Servers[i].ID < doc.Servers[j].ID })
	raw, err := json.Marshal(doc)
	if err != nil {
		return
	}
	p.probeKey, p.probeSentAt, p.probeAnswered, p.probeWanted = key, g.now(), false, false
	g.send(p.SteamID, msgServerList, &gcpb.CMsgPartyChatMsg{Msg: proto.String(string(raw))})
}

// tickProbes keeps every pinging game's measurements current, and lets a
// party that no server suits play on the best one there is.
func (g *GC) tickProbes() {
	list := g.probeList()
	key := probeKey(list)
	now := g.now()
	for _, p := range g.online {
		if !p.Online || !p.CanPing || now.Sub(p.probeSentAt) < probeMinGap {
			continue
		}
		if p.probeKey != key || now.Sub(p.probeSentAt) >= probeRefresh || p.probeWanted {
			g.sendProbe(p, list, key)
		}
	}
	if len(list) == 0 {
		return
	}
	g.warnUnreachedServers(list)
	for _, party := range g.parties {
		g.checkPartyReach(party, list)
	}
}

// pingOK reports whether a player's last measurement lets them play on a
// server. A server their game was not asked about yet counts: it is measured
// on the next probe.
func (g *GC) pingOK(p *Player, srv *Server) bool {
	if p == nil || !p.CanPing {
		return true
	}
	r, ok := p.Pings[srv.SteamID]
	if !ok || r.Addr != srv.Connect {
		return true
	}
	max := g.cfg.ServerPing.Max()
	return r.OK && (max < 0 || r.MS < 0 || r.MS <= max)
}

// queueServers is the pool a group's matches could go to.
func (g *GC) queueServers(grp config.MatchGroupConfig, list map[uint64]*Server) []*Server {
	var out []*Server
	for _, srv := range list {
		if grp.Region != "" && srv.Region != grp.Region {
			continue
		}
		if g.cfg.AntiCheat.RequireProtectedServers && !srv.Protected {
			continue
		}
		out = append(out, srv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SteamID < out[j].SteamID })
	return out
}

// checkPartyReach lets a queued party play on any server once, by what every
// member's game measured, no server of the pool suits all of them. One bad
// measurement is not enough: the first time the party looks stuck its
// members measure again, and only if it still is does it fall back. A member
// whose game does not answer is not waited for. The fallback lasts while the
// party queues or plays the match it found.
func (g *GC) checkPartyReach(party *Party, list map[uint64]*Server) {
	if len(party.Queues) == 0 {
		party.reachDoubt = time.Time{}
		if party.reachRelaxed && g.partyLobby(party) == nil {
			party.reachRelaxed = false
		}
		return
	}
	now := g.now()
	var stuckServers []*Server
	for gid := range party.Queues {
		grp, ok := g.cfg.Group(gid)
		if !ok {
			continue
		}
		cands := g.queueServers(grp, list)
		if len(cands) == 0 || g.partyReachesAny(party, cands) {
			continue
		}
		stuckServers = cands
		break
	}
	if stuckServers == nil {
		party.reachDoubt = time.Time{}
		party.reachRelaxed = false // a good server is back (or a VPN went on)
		return
	}
	if party.reachRelaxed {
		return
	}
	if party.reachDoubt.IsZero() {
		party.reachDoubt = now
		for _, id := range party.Members {
			if p := g.online[id]; p != nil && p.CanPing {
				p.probeWanted = true
			}
		}
		return
	}
	for _, id := range party.Members {
		p := g.online[id]
		if p == nil || !p.CanPing {
			continue
		}
		if !p.probeAnswered || p.probeSentAt.Before(party.reachDoubt) {
			if p.probeSentAt.Before(party.reachDoubt) {
				p.probeWanted = true
			}
			if now.Sub(party.reachDoubt) < probeMinGap+g.cfg.ServerPing.AnswerWait() {
				return // still measuring
			}
		}
	}
	party.reachDoubt = time.Time{}
	party.reachRelaxed = true
	best := slices.Clone(stuckServers)
	g.byPing(best, party.Members)
	g.log.Info("party plays on the best server there is: none within the ping limit for all of it",
		"party", party.ID, "steamids", party.Members, "best", best[0].SteamID, "best_name", best[0].Name)
	g.reportUnreachable(party, best[0])
}

// partyReachesAny reports whether some server lets every member play.
func (g *GC) partyReachesAny(party *Party, servers []*Server) bool {
	for _, srv := range servers {
		all := true
		for _, id := range party.Members {
			if !g.pingOK(g.online[id], srv) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// reachReport is msgUnreachable: why the party plays on whatever server
// there is -- self: the reader reaches none within the limit; who: the
// members who reach none; party: each of them reaches one, but no server all
// of them -- and the server it will most likely play on, with the reader's
// own ping to it (-1: it does not answer them).
type reachReport struct {
	Self  bool        `json:"self"`
	Party bool        `json:"party"`
	Who   []string    `json:"who"`
	MaxMS int         `json:"max_ms"`
	Best  reachServer `json:"best"`
}

type reachServer struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	MS     int    `json:"ms"`
}

func (g *GC) reportUnreachable(party *Party, best *Server) {
	list := g.probeList()
	var blamed []uint64
	for _, id := range party.Members {
		p := g.online[id]
		reachesSome := false
		for _, srv := range list {
			if g.pingOK(p, srv) {
				reachesSome = true
				break
			}
		}
		if !reachesSome {
			blamed = append(blamed, id)
		}
	}
	region := best.Region
	if r := g.regionByID(best.Region); r != nil {
		region = r.Name
	}
	for _, id := range party.Members {
		p := g.online[id]
		if p == nil || !p.Online {
			continue
		}
		rep := reachReport{Party: len(blamed) == 0, Who: []string{}, MaxMS: g.cfg.ServerPing.Max(),
			Best: reachServer{Name: best.Name, Region: region, MS: -1}}
		for _, b := range blamed {
			if b == id {
				rep.Self = true
				continue
			}
			name := g.nameOf(b)
			if name == "" {
				name = strconv.FormatUint(b, 10)
			}
			rep.Who = append(rep.Who, name)
		}
		if r, ok := p.Pings[best.SteamID]; ok && r.Addr == best.Connect && r.OK {
			rep.Best.MS = r.MS
		}
		if p.ReachDialog {
			if raw, err := json.Marshal(rep); err == nil {
				g.send(id, msgUnreachable, &gcpb.CMsgPartyChatMsg{Msg: proto.String(string(raw))})
				continue
			}
		}
		g.notice(id, reachText(rep, langCode(p.Lang) == "ru"))
	}
}

// reachText is the report as a line, for a game without msgUnreachable.
func reachText(rep reachReport, ru bool) string {
	server := rep.Best.Name
	if server == "" {
		server = rep.Best.Region
	} else if rep.Best.Region != "" {
		server += " (" + rep.Best.Region + ")"
	}
	ping := ""
	if rep.Best.MS >= 0 {
		ping = ", ~" + strconv.Itoa(rep.Best.MS) + " ms"
		if ru {
			ping = ", ~" + strconv.Itoa(rep.Best.MS) + " мс"
		}
	}
	limit := strconv.Itoa(rep.MaxMS)
	if ru {
		why := "Ни один сервер не отвечает вам быстрее " + limit + " мс"
		if !rep.Self && len(rep.Who) > 0 {
			why = "Ни один сервер не отвечает игроку " + strings.Join(rep.Who, ", ") + " быстрее " + limit + " мс"
		} else if !rep.Self {
			why = "Нет сервера, который отвечает всей группе быстрее " + limit + " мс"
		}
		return why + ". Ищем матч на лучшем из доступных: " + server + ping + ". Пинг может быть высоким; VPN или регион поближе может помочь."
	}
	why := "No server answers you within " + limit + " ms"
	if !rep.Self && len(rep.Who) > 0 {
		why = "No server answers " + strings.Join(rep.Who, ", ") + " within " + limit + " ms"
	} else if !rep.Self {
		why = "No server answers everyone in your party within " + limit + " ms"
	}
	return why + ". Searching on the best one there is: " + server + ping + ". Expect a high ping; a VPN or a closer region may help."
}

// warnUnreachedServers logs a server no player reaches while several tried:
// more likely the server than every player's network. A matchmade server
// must answer Steam server queries (A2S) on its game port.
func (g *GC) warnUnreachedServers(list map[uint64]*Server) {
	now := g.now()
	if now.Sub(g.reachWarnAt) < probeRefresh {
		return
	}
	for id, srv := range list {
		ok, failed := 0, 0
		for _, p := range g.online {
			if r, measured := p.Pings[id]; measured && r.Addr == srv.Connect && now.Sub(r.At) < probeRefresh {
				if r.OK {
					ok++
				} else {
					failed++
				}
			}
		}
		if ok == 0 && failed >= 3 {
			g.reachWarnAt = now
			g.log.Warn("server_ping: no player's game reaches this server; check it answers Steam server queries on its game port",
				"server", id, "name", srv.Name, "connect", srv.Connect, "region", srv.Region, "players_tried", failed)
		}
	}
}

// reaches reports whether a player may be sent to a server.
func (g *GC) reaches(id uint64, srv *Server) bool {
	p := g.online[id]
	if p == nil || !p.CanPing {
		return true
	}
	if party := g.parties[p.PartyID]; party != nil && party.reachRelaxed {
		return true // no server suits them: the best there is will do (byPing)
	}
	if r, ok := p.Pings[srv.SteamID]; ok && r.Addr == srv.Connect {
		return g.pingOK(p, srv)
	}
	// Not measured yet. A game that has not answered for a while is not
	// waited for any longer.
	return !p.probeSentAt.IsZero() && !p.probeAnswered && g.now().Sub(p.probeSentAt) >= g.cfg.ServerPing.AnswerWait()
}

func (g *GC) entryReaches(e mm.Entry, srv *Server) bool {
	for _, pl := range e.Players {
		if !g.reaches(pl.SteamID, srv) {
			return false
		}
	}
	return true
}

func (g *GC) lobbyReaches(l *Lobby, srv *Server) bool {
	for _, m := range l.Members {
		if !g.reaches(m.SteamID, srv) {
			return false
		}
	}
	return true
}

// avgPing is the players' average measured ping to a server; servers nobody
// timed come last.
func (g *GC) avgPing(ids []uint64, srv *Server) float64 {
	sum, n := 0, 0
	for _, id := range ids {
		if p := g.online[id]; p != nil {
			if r, ok := p.Pings[srv.SteamID]; ok && r.Addr == srv.Connect && r.OK && r.MS >= 0 {
				sum += r.MS
				n++
			}
		}
	}
	if n == 0 {
		return math.MaxFloat64
	}
	return float64(sum) / float64(n)
}

// byPing orders servers best first for these players: lowest average ping,
// then the lowest SteamID, the order the GC always used.
func (g *GC) byPing(servers []*Server, ids []uint64) {
	avg := make(map[uint64]float64, len(servers))
	for _, srv := range servers {
		avg[srv.SteamID] = g.avgPing(ids, srv)
	}
	sort.Slice(servers, func(i, j int) bool {
		a, b := avg[servers[i].SteamID], avg[servers[j].SteamID]
		if a != b {
			return a < b
		}
		return servers[i].SteamID < servers[j].SteamID
	})
}

// pingRegion is the region of the reachable server a player pings best, ""
// if their game measured none.
func (g *GC) pingRegion(p *Player) string {
	best, bestMS := "", math.MaxInt
	for id, r := range p.Pings {
		srv := g.servers[id]
		if srv == nil || srv.Region == "" || r.Addr != srv.Connect || !g.reaches(p.SteamID, srv) || r.MS < 0 {
			continue
		}
		if g.regionByID(srv.Region) != nil && r.MS < bestMS {
			best, bestMS = srv.Region, r.MS
		}
	}
	return best
}
