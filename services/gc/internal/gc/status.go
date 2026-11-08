package gc

import (
	"sort"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/war"
)

// Status is the public picture of the GC that the game's main menu shows:
// how many people are around, what is running, what each queue needs. It is
// rebuilt on the loop every tick and read without locks.
type Status struct {
	Name        string `json:"name"`
	Online      int    `json:"online"`
	LiveMatches int    `json:"live_matches"`
	Servers     int    `json:"servers"` // the pool: servers connected to the GC
	FreeServers int    `json:"free_servers"`
	// Where the online players are, as of the last presence push.
	InMenu      int           `json:"in_menu"`
	InQueue     int           `json:"in_queue"`
	InMatch     int           `json:"in_match"`
	LFG         int           `json:"lfg"`
	Clans       int           `json:"clans"`
	Groups      []GroupStatus `json:"groups"`
	Fronts      []war.Front   `json:"fronts,omitempty"`
	Campaign    string        `json:"campaign,omitempty"`
	GeneratedAt time.Time     `json:"generated_at"`
}

// GroupStatus is one queue.
type GroupStatus struct {
	MatchGroup int32    `json:"match_group"`
	Name       string   `json:"name"`
	Mode       string   `json:"mode"`
	Queued     int      `json:"queued"`
	Need       int      `json:"need"`
	Ideal      int      `json:"ideal"`
	Max        int      `json:"max"`
	Matches    int      `json:"matches"`
	Maps       []string `json:"maps"`
}

// Status returns the latest snapshot.
func (g *GC) Status() *Status { return g.status.Load() }

func (g *GC) publishStatus() {
	st := &Status{Name: g.cfg.Name, GeneratedAt: g.now(), Servers: g.poolSize(),
		InMenu: g.presenceCounts.Menu, InQueue: g.presenceCounts.Queue, InMatch: g.presenceCounts.Match,
		LFG: g.presenceCounts.LFG, Clans: g.presenceCounts.Clans}
	for _, p := range g.online {
		if p.Online {
			st.Online++
		}
	}
	perGroup := map[int32]int{}
	for _, l := range g.lobbies {
		if l.State == lobbyRun {
			st.LiveMatches++
		}
		perGroup[l.Group]++
	}
	for _, srv := range g.servers {
		if g.serverEligible(srv, g.now()) {
			st.FreeServers++
		}
	}
	for _, grp := range g.cfg.MatchGroups {
		if !grp.Enabled {
			continue
		}
		queued := 0
		for _, p := range g.parties {
			if _, ok := p.Queues[grp.MatchGroup]; ok {
				queued += len(p.Members)
			}
		}
		maps := grp.EffectiveMaps()
		sort.Strings(maps)
		st.Groups = append(st.Groups, GroupStatus{
			MatchGroup: grp.MatchGroup,
			Name:       grp.Name,
			Mode:       string(grp.EffectiveMode()),
			Queued:     queued,
			Need:       grp.MinPlayers,
			Ideal:      grp.IdealPlayers,
			Max:        grp.MaxPlayers,
			Matches:    perGroup[grp.MatchGroup],
			Maps:       maps,
		})
	}
	if g.war != nil {
		st.Fronts = g.war.Fronts()
		st.Campaign = g.war.Campaign()
	}
	g.status.Store(st)
	g.publishWar()
}
