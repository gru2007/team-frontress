package gc

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// Regions. A player's region is the one they picked in the game
// (tf_mm_region, msgRegion) or else the one holding the Steam relay data
// center they ping best (handleDataCenterPings). Parties are matched within
// their region, on that region's servers; a party that has waited longer
// than region_wait_secs, or whose region has no servers at all, joins the
// pool that plays anywhere.
const msgRegion uint32 = 51020

func init() {
	clientHandlers[msgRegion] = func(g *GC, s *session.Session, m *wire.Msg) {
		var body gcpb.CMsgPartyChatMsg
		if !g.parse(m, &body) {
			return
		}
		var pick struct {
			Region string `json:"region"`
			Ping   int    `json:"ping"` // the game pings servers when asked (probe.go)
		}
		if len(body.GetMsg()) > 128 || json.Unmarshal([]byte(body.GetMsg()), &pick) != nil {
			return
		}
		region := strings.ToLower(pick.Region)
		if region != "" && g.regionByID(region) == nil {
			region = ""
		}
		p := g.player(s.SteamID)
		p.RegionPref = region
		p.ReachDialog = pick.Ping >= 2
		if pick.Ping > 0 && !p.CanPing {
			p.CanPing = true
			p.probeSentAt = time.Time{}
			list := g.probeList()
			g.sendProbe(p, list, probeKey(list))
		}
	}
}

func (g *GC) regionByID(id string) *config.RegionConfig {
	for i := range g.cfg.Regions {
		if g.cfg.Regions[i].ID == id {
			return &g.cfg.Regions[i]
		}
	}
	return nil
}

// playerRegion is where a player plays: their pick, else the region of the
// server they ping best, else their best data center's region, else ""
// (unknown: anywhere).
func (g *GC) playerRegion(id uint64) string {
	p := g.online[id]
	if p == nil {
		return ""
	}
	if p.RegionPref != "" && g.regionByID(p.RegionPref) != nil {
		return p.RegionPref
	}
	if r := g.pingRegion(p); r != "" {
		return r
	}
	if p.BestDataCenter == "" {
		return ""
	}
	for _, r := range g.cfg.Regions {
		if slices.Contains(r.Pops, p.BestDataCenter) {
			return r.ID
		}
	}
	return ""
}

// partyRegion is the leader's region, or the first member's that is known.
func (g *GC) partyRegion(p *Party) string {
	if p == nil {
		return ""
	}
	if r := g.playerRegion(p.Leader); r != "" {
		return r
	}
	for _, id := range p.Members {
		if r := g.playerRegion(id); r != "" {
			return r
		}
	}
	return ""
}

// regionHasServers reports whether any server of the region is connected,
// free or not: if none is, its players are matched anywhere at once.
func (g *GC) regionHasServers(region string) bool {
	for _, srv := range g.servers {
		if srv.Region == region {
			return true
		}
	}
	return false
}

// entryRegion is the region a queued party is matched in now: "" once it
// may play anywhere.
func (g *GC) entryRegion(e mm.Entry) string {
	if len(g.cfg.Regions) == 0 {
		return ""
	}
	region := g.partyRegion(g.parties[e.PartyID])
	if region == "" || !g.regionHasServers(region) || g.now().Sub(e.QueuedAt) >= g.cfg.RegionWait() {
		return ""
	}
	return region
}

type regionPool struct {
	region  string
	entries []mm.Entry
}

// regionPools splits a group's queue by region, the regions first and the
// pool that plays anywhere last. A group pinned to a region keeps one pool.
func (g *GC) regionPools(grp config.MatchGroupConfig, queue []mm.Entry) []regionPool {
	if grp.Region != "" || len(g.cfg.Regions) == 0 {
		return []regionPool{{region: grp.Region, entries: queue}}
	}
	byRegion := map[string][]mm.Entry{}
	for _, e := range queue {
		r := g.entryRegion(e)
		byRegion[r] = append(byRegion[r], e)
	}
	var pools []regionPool
	for _, r := range g.cfg.Regions {
		if entries := byRegion[r.ID]; len(entries) > 0 {
			pools = append(pools, regionPool{region: r.ID, entries: entries})
		}
	}
	if entries := byRegion[""]; len(entries) > 0 {
		pools = append(pools, regionPool{region: "", entries: entries})
	}
	return pools
}

// freeServersIn lists the servers that could take a match of the group in
// the region now ("": anywhere the group may play).
func (g *GC) freeServersIn(grp config.MatchGroupConfig, region string) []*Server {
	if region == "" {
		region = grp.Region
	}
	var out []*Server
	for _, srv := range g.servers {
		if g.serverEligible(srv, g.now()) && (region == "" || srv.Region == region) {
			out = append(out, srv)
		}
	}
	return out
}

// lobbyTakes reports whether a running match may take a queued party: it is
// in the party's region, or one of them is not tied to a region.
func (g *GC) lobbyTakes(l *Lobby, e mm.Entry) bool {
	if srv := g.servers[l.ServerID]; srv != nil && !g.entryReaches(e, srv) {
		return false
	}
	if l.Region == "" {
		return true
	}
	r := g.entryRegion(e)
	return r == "" || r == l.Region
}
