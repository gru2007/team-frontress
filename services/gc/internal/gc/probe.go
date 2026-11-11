package gc

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

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
const (
	msgServerList  uint32 = 51021 // GC -> client: {"regions": [{id, name}], "servers": [{id, addr, region}]}
	msgServerPings uint32 = 51022 // client -> GC: {"pings": [{id, ms, ok}]}
)

const (
	probeRefresh   = 10 * time.Minute // measure again this often while online
	probeMinGap    = 10 * time.Second // never ask one player more often than this
	reachNoticeGap = 10 * time.Minute
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
	p.probeKey, p.probeSentAt, p.probeAnswered = key, g.now(), false
	g.send(p.SteamID, msgServerList, &gcpb.CMsgPartyChatMsg{Msg: proto.String(string(raw))})
}

// tickProbes keeps every pinging game's measurements current, and tells a
// queued player who reaches no server at all why their queue does not pop.
func (g *GC) tickProbes() {
	list := g.probeList()
	key := probeKey(list)
	now := g.now()
	for _, p := range g.online {
		if !p.Online || !p.CanPing || now.Sub(p.probeSentAt) < probeMinGap {
			continue
		}
		if p.probeKey != key || now.Sub(p.probeSentAt) >= probeRefresh {
			g.sendProbe(p, list, key)
		}
	}
	if len(list) == 0 {
		return
	}
	g.warnUnreachedServers(list)
	for _, party := range g.parties {
		if len(party.Queues) == 0 {
			continue
		}
		for _, id := range party.Members {
			p := g.online[id]
			if p == nil || !p.CanPing || !p.probeAnswered || now.Sub(p.reachNoticeAt) < reachNoticeGap {
				continue
			}
			reached := false
			for _, srv := range list {
				if g.reaches(id, srv) {
					reached = true
					break
				}
			}
			if !reached {
				p.reachNoticeAt = now
				g.notice(id, "None of the matchmaking servers answers your game within "+
					strconv.Itoa(g.cfg.ServerPing.Max())+" ms. You stay in the queue until one does; "+
					"another region or a VPN may help.")
			}
		}
	}
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
	if r, ok := p.Pings[srv.SteamID]; ok && r.Addr == srv.Connect {
		max := g.cfg.ServerPing.Max()
		return r.OK && (max < 0 || r.MS < 0 || r.MS <= max)
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
