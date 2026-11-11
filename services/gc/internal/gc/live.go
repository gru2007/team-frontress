package gc

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
)

// Live is what the coordinator is doing right now, for the admin panel:
// the server pool, the running matches and who stands in which queue. It
// holds no session tokens, server keys or private addresses beyond what an
// admin already sees in the server's own connect string.
type Live struct {
	Status  *Status      `json:"status"`
	Servers []LiveServer `json:"servers"`
	Matches []LiveMatch  `json:"matches"`
	Parties []LiveParty  `json:"parties"`
}

// LiveServer is one server of the pool.
type LiveServer struct {
	SteamID    string    `json:"steam_id"`
	Name       string    `json:"name"`
	Community  string    `json:"community"`
	Region     string    `json:"region"`
	Connect    string    `json:"connect"`
	Map        string    `json:"map"`
	State      string    `json:"state"` // free, busy, resting, not_ready, offline
	MatchID    string    `json:"match_id,omitempty"`
	Protected  bool      `json:"protected"`
	LastStatus time.Time `json:"last_status,omitzero"`
	// KeyID is the server key it connected with (config.KeyID).
	KeyID       string    `json:"key_id,omitempty"`
	ConnectedAt time.Time `json:"connected_at,omitzero"`
	// Players is how many of its match's players are connected.
	Players int `json:"players"`
}

// LiveMatch is one lobby.
type LiveMatch struct {
	MatchID   string       `json:"match_id"`
	Group     int32        `json:"match_group"`
	GroupName string       `json:"group_name"`
	Kind      string       `json:"kind"` // casual, ranked or war
	Sector    string       `json:"sector,omitempty"`
	Map       string       `json:"map"`
	State     string       `json:"state"`
	Region    string       `json:"region"`
	Server    string       `json:"server"`
	FormedAt  time.Time    `json:"formed_at"`
	StartedAt time.Time    `json:"started_at,omitzero"`
	Members   []LiveMember `json:"members"`
}

// LiveMember is one seat of a running match.
type LiveMember struct {
	SteamID string `json:"steam_id"`
	Name    string `json:"name"`
	Team    string `json:"team"`
	Connect string `json:"connect"`
}

// LiveParty is a party standing in at least one queue.
type LiveParty struct {
	ID      string           `json:"id"`
	Leader  string           `json:"leader"`
	Members []LiveMember     `json:"members"`
	Queues  map[string]int64 `json:"queues"` // match group -> seconds waiting
}

// Live builds the picture. Run on the loop.
func (g *GC) Live() Live {
	now := g.now()
	out := Live{Status: g.status.Load(), Servers: []LiveServer{}, Matches: []LiveMatch{}, Parties: []LiveParty{}}
	groupName := map[int32]string{}
	for _, grp := range g.cfg.MatchGroups {
		groupName[grp.MatchGroup] = grp.Name
	}
	for _, srv := range g.servers {
		s := LiveServer{SteamID: lobbyKey(srv.SteamID), Name: srv.Name, Community: srv.Community,
			Region: srv.Region, Connect: srv.Connect, Map: srv.Map, Protected: srv.Protected, LastStatus: srv.LastStatus}
		switch {
		case srv.Session == nil || srv.Session.Closed():
			s.State = "offline"
		case srv.LobbyID != 0:
			s.State = "busy"
		case now.Before(srv.RestUntil):
			s.State = "resting"
		case !srv.Ready:
			s.State = "not_ready"
		default:
			s.State = "free"
		}
		if srv.Session != nil {
			s.KeyID, s.ConnectedAt = srv.Session.KeyID, srv.Session.Created
		}
		if l := g.lobbies[srv.LobbyID]; l != nil {
			s.MatchID = lobbyKey(l.MatchID)
			for _, m := range l.Members {
				if m.Connect == gcpb.CTFLobbyPlayerProto_CONNECTED {
					s.Players++
				}
			}
		}
		out.Servers = append(out.Servers, s)
	}
	sort.Slice(out.Servers, func(i, j int) bool { return out.Servers[i].Name < out.Servers[j].Name })
	for _, l := range g.lobbies {
		m := LiveMatch{MatchID: lobbyKey(l.MatchID), Group: l.Group, GroupName: groupName[l.Group], Kind: g.matchKind(l), Map: l.Map,
			State: string(l.State), Region: l.Region, FormedAt: l.FormedAt, StartedAt: l.StartedAt, Members: []LiveMember{}}
		if l.War != nil {
			m.Sector = l.War.NodeName
		}
		if srv := g.servers[l.ServerID]; srv != nil {
			m.Server = srv.Name
		}
		for _, mem := range l.Members {
			team := "red"
			if mem.Team == mm.Blu {
				team = "blu"
			}
			m.Members = append(m.Members, LiveMember{SteamID: lobbyKey(mem.SteamID), Name: g.nameOf(mem.SteamID),
				Team: team, Connect: strings.ToLower(strings.TrimPrefix(mem.Connect.String(), "k_"))})
		}
		out.Matches = append(out.Matches, m)
	}
	sort.Slice(out.Matches, func(i, j int) bool { return out.Matches[i].FormedAt.After(out.Matches[j].FormedAt) })
	for _, p := range g.parties {
		if len(p.Queues) == 0 {
			continue
		}
		lp := LiveParty{ID: lobbyKey(p.ID), Leader: lobbyKey(p.Leader), Queues: map[string]int64{}}
		for _, id := range p.Members {
			lp.Members = append(lp.Members, LiveMember{SteamID: lobbyKey(id), Name: g.nameOf(id)})
		}
		for grp, since := range p.Queues {
			lp.Queues[strconv.Itoa(int(grp))] = int64(now.Sub(since).Seconds())
		}
		out.Parties = append(out.Parties, lp)
	}
	sort.Slice(out.Parties, func(i, j int) bool { return out.Parties[i].ID < out.Parties[j].ID })
	return out
}

// ErrNoSuchMatch: no running match has that ID.
var ErrNoSuchMatch = errors.New("no running match has this ID")

// AdminEndMatch ends a running match the way a missed deadline does: the
// lobby goes away, the server is freed and the archive records why. The
// parties are not put back in the queue. Run on the loop.
func (g *GC) AdminEndMatch(matchID uint64, by, reason string) error {
	for _, l := range g.lobbies {
		if l.MatchID == matchID {
			g.log.Info("match ended by an admin", "match", matchID, "by", by, "reason", reason)
			g.endLobby(l, "ended by an admin: "+reason, false)
			return nil
		}
	}
	return ErrNoSuchMatch
}
