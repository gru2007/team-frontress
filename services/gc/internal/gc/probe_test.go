package gc

import (
	"strings"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
)

// A match only goes to a server every player in it reaches within the ping
// limit, even once the party has waited out its region.
func TestMatchesOnlyGoWherePlayersReach(t *testing.T) {
	grp := config.MatchGroupConfig{MatchGroup: 7, Enabled: true, MinPlayers: 2, IdealPlayers: 2, MaxPlayers: 4}
	cfg := config.Config{
		MatchGroups: []config.MatchGroupConfig{grp},
		Regions: []config.RegionConfig{
			{ID: "ru", Name: "RU/Asia"},
			{ID: "eu", Name: "EU/Germany", Pops: []string{"fra", "hel"}},
			{ID: "na", Name: "NA/Canada", Pops: []string{"iad"}},
		},
		RegionWaitSecs: 120,
		ServerPing:     config.ServerPingConfig{MaxMS: 150},
	}
	g, err := New(Deps{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	server := func(id uint64, region, addr string) *Server {
		s := g.sessions.Open(session.Server, id, nil)
		s.Connect = addr
		s.Region = region
		g.ServerConnected(s)
		srv := g.servers[id]
		srv.Ready = true
		return srv
	}
	canada := server(100, "na", "10.0.0.1:27015")
	germany := server(200, "eu", "10.0.0.2:27015")
	germany.Ready = false // busy for now

	long := g.now().Add(-3 * time.Minute)
	pinger := func(id uint64, pop string, pings map[uint64]serverPing) {
		p := g.player(id)
		p.Online, p.BestDataCenter, p.CanPing = true, pop, true
		p.probeSentAt, p.probeAnswered = g.now(), true
		p.Pings = map[uint64]serverPing{}
		for srv, r := range pings {
			r.Addr = g.servers[srv].Connect
			p.Pings[srv] = r
		}
		g.newParty(id).Queues[grp.MatchGroup] = long
	}
	// Two players in Russia: Canada does not answer them, Germany does.
	ruPings := map[uint64]serverPing{canada.SteamID: {OK: false, MS: -1}, germany.SteamID: {OK: true, MS: 60}}
	pinger(1, "hel", ruPings)
	pinger(2, "hel", ruPings)
	g.matchmake()
	if g.lobbyOf(1) != nil || g.lobbyOf(2) != nil {
		t.Fatal("players were sent to a server they cannot reach")
	}

	germany.Ready = true
	g.matchmake()
	l := g.lobbyOf(1)
	if l == nil || g.lobbyOf(2) != l || l.ServerID != germany.SteamID {
		t.Fatalf("players who reach Germany were not matched there: %+v", l)
	}

	// A ping over the limit is as good as no answer.
	if g.reaches(1, &Server{SteamID: canada.SteamID, Connect: canada.Connect}) {
		t.Fatal("an unreachable server counted as reachable")
	}
	g.player(1).Pings[canada.SteamID] = serverPing{Addr: canada.Connect, OK: true, MS: 190}
	if g.reaches(1, canada) {
		t.Fatal("a server over the ping limit counted as reachable")
	}

	// The region a player pings best is theirs, whatever their data center.
	if r := g.playerRegion(1); r != "eu" {
		t.Fatalf("region by ping = %q, want eu", r)
	}

	// A game that does not ping (an older build) is not held back.
	if !g.reaches(99, canada) {
		t.Fatal("a player whose game does not ping was held back")
	}
	// A pinging game that has not answered is waited for, but not forever.
	p := g.player(3)
	p.Online, p.CanPing = true, true
	p.probeSentAt, p.probeAnswered = g.now(), false
	if g.reaches(3, canada) {
		t.Fatal("a server was used before the player's game answered")
	}
	p.probeSentAt = g.now().Add(-time.Minute)
	if !g.reaches(3, canada) {
		t.Fatal("a game that never answered held its player back forever")
	}
}

// A party no server suits within the ping limit is not left waiting: once a
// second measurement agrees, it plays on the best server there is.
func TestUnsuitedPartyPlaysOnBestServer(t *testing.T) {
	grp := config.MatchGroupConfig{MatchGroup: 7, Enabled: true, MinPlayers: 2, IdealPlayers: 2, MaxPlayers: 4}
	now := time.Unix(1_800_000_000, 0)
	g, err := New(Deps{Now: func() time.Time { return now }, Config: config.Config{
		MatchGroups: []config.MatchGroupConfig{grp},
		Regions:     []config.RegionConfig{{ID: "na", Name: "NA/Canada"}},
		ServerPing:  config.ServerPingConfig{MaxMS: 150},
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := func(id uint64, addr string) *Server {
		s := g.sessions.Open(session.Server, id, nil)
		s.Connect, s.Region = addr, "na"
		g.ServerConnected(s)
		srv := g.servers[id]
		srv.Ready = true
		return srv
	}
	far := server(100, "10.0.0.1:27015")
	dead := server(200, "10.0.0.2:27015")

	// Two players in Russia, each queued alone.
	p, q := g.player(1), g.player(2)
	party, other := g.newParty(1), g.newParty(2)
	p.PartyID, q.PartyID = party.ID, other.ID
	for _, pl := range []*Player{p, q} {
		pl.Online, pl.CanPing = true, true
	}
	party.Queues[grp.MatchGroup] = now
	other.Queues[grp.MatchGroup] = now
	answer := func(farMS int) {
		for _, pl := range []*Player{p, q} {
			pl.probeSentAt, pl.probeAnswered, pl.probeWanted = now, true, false
			pl.probeKey = probeKey(g.probeList())
			pl.Pings = map[uint64]serverPing{
				far.SteamID:  {Addr: far.Connect, OK: true, MS: farMS, At: now},
				dead.SteamID: {Addr: dead.Connect, OK: false, MS: -1, At: now},
			}
		}
	}
	answer(240)

	now = now.Add(time.Second)
	g.tickProbes()
	g.matchmake()
	if party.reachRelaxed || g.lobbyOf(1) != nil {
		t.Fatal("one measurement over the limit was enough to fall back")
	}
	if !p.probeWanted && p.probeSentAt.Before(party.reachDoubt) {
		t.Fatal("the player was not asked to measure again")
	}

	// A VPN went on: the second measurement is within the limit, no fallback.
	now = now.Add(probeMinGap)
	answer(90)
	now = now.Add(time.Second)
	g.tickProbes()
	if party.reachRelaxed || !party.reachDoubt.IsZero() {
		t.Fatal("a party a server suits after all fell back")
	}

	// Over the limit again, twice: it plays on the best server there is,
	// the one that answers, not the one that does not.
	answer(240)
	now = now.Add(time.Second)
	g.tickProbes()
	now = now.Add(probeMinGap)
	answer(240)
	now = now.Add(time.Second)
	g.tickProbes()
	if !party.reachRelaxed {
		t.Fatal("a party no server suits did not fall back to the best one")
	}
	if _, queued := party.Queues[grp.MatchGroup]; !queued {
		t.Fatal("the party was taken out of the queue")
	}
	g.matchmake()
	l := g.lobbyOf(1)
	if l == nil || g.lobbyOf(2) != l || l.ServerID != far.SteamID {
		t.Fatalf("the party was not matched on the best server: %+v", l)
	}
	// The fallback lasts while the match it found is on.
	g.tickProbes()
	if !party.reachRelaxed {
		t.Fatal("the fallback ended before the match did")
	}

	en, ru := reachText(reachReport{Self: true, MaxMS: 150, Best: reachServer{Name: "Frontress #1", Region: "NA/Canada", MS: 240}}, false),
		reachText(reachReport{Self: true, MaxMS: 150, Best: reachServer{Name: "Frontress #1", Region: "NA/Canada", MS: 240}}, true)
	if !strings.Contains(en, "Frontress #1 (NA/Canada), ~240 ms") || !strings.Contains(ru, "~240 мс") {
		t.Fatalf("notice text: %q / %q", en, ru)
	}
}
