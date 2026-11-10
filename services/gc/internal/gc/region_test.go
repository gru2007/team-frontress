package gc

import (
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
)

// Parties are matched with their own region on its servers; one that has
// waited out region_wait_secs plays with anyone, anywhere.
func TestMatchesStayInTheirRegion(t *testing.T) {
	grp := config.MatchGroupConfig{MatchGroup: 7, Enabled: true, MinPlayers: 2, IdealPlayers: 2, MaxPlayers: 4}
	cfg := config.Config{
		MatchGroups: []config.MatchGroupConfig{grp},
		Regions: []config.RegionConfig{
			{ID: "eu", Name: "Europe", Pops: []string{"fra", "ams"}},
			{ID: "na", Name: "North America", Pops: []string{"iad", "ord"}},
		},
		RegionWaitSecs: 120,
	}
	g, err := New(Deps{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	server := func(id uint64, region string) *Server {
		s := g.sessions.Open(session.Server, id, nil)
		s.Connect = "10.0.0.1:27015"
		s.Region = region
		g.ServerConnected(s)
		srv := g.servers[id]
		srv.Ready = true
		return srv
	}
	euServer := server(100, "eu")
	naServer := server(200, "na")

	now := g.now()
	queue := func(id uint64, pop string, at time.Time) {
		p := g.player(id)
		p.Online = true
		p.BestDataCenter = pop
		party := g.newParty(id)
		party.Queues[grp.MatchGroup] = at
	}
	queue(1, "fra", now)
	queue(2, "iad", now)
	queue(3, "ams", now)
	queue(4, "ord", now)
	g.matchmake()

	lobbyOf := func(id uint64) *Lobby {
		t.Helper()
		l := g.lobbyOf(id)
		if l == nil {
			t.Fatalf("player %d was not matched", id)
		}
		return l
	}
	eu, na := lobbyOf(1), lobbyOf(2)
	if lobbyOf(3) != eu || lobbyOf(4) != na || eu == na {
		t.Fatal("players were not matched with their own region")
	}
	if eu.Region != "eu" || eu.ServerID != euServer.SteamID || na.Region != "na" || na.ServerID != naServer.SteamID {
		t.Fatalf("matches went to the wrong servers: eu on %d (%q), na on %d (%q)", eu.ServerID, eu.Region, na.ServerID, na.Region)
	}

	// Alone in their regions for long enough: they play together.
	for _, l := range []*Lobby{eu, na} {
		delete(g.lobbies, l.ID)
		g.servers[l.ServerID].LobbyID = 0
	}
	long := now.Add(-3 * time.Minute)
	queue(5, "fra", long)
	queue(6, "iad", long)
	g.matchmake()
	if l := lobbyOf(5); l != lobbyOf(6) || l.Region != "" {
		t.Fatal("parties that waited out their region were not matched together")
	}
}
