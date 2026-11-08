package gc

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/anticheat"
	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func TestAntiCheatBansOnlyFromTrustedEnforcingServers(t *testing.T) {
	dir := t.TempDir()
	store, err := anticheat.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Deps{AntiCheat: store})
	if err != nil {
		t.Fatal(err)
	}
	official := g.sessions.Open(session.Server, 90071992547409921, func(s *session.Session) {
		s.ServerName = "official"
		s.AntiCheatAuthority = true
	})
	community := g.sessions.Open(session.Server, 90071992547409922, func(s *session.Session) {
		s.ServerName = "community"
	})
	g.ServerConnected(official)
	g.ServerConnected(community)

	cheater := g.sessions.Open(session.Client, steamauth.MinIndividual+7, nil)
	friend := g.sessions.Open(session.Client, steamauth.MinIndividual+8, nil)
	g.ClientConnected(cheater, true)
	g.ClientConnected(friend, true)
	p := g.parties[g.player(cheater.SteamID).PartyID]
	if err := g.joinParty(p, friend.SteamID); err != nil {
		t.Fatal(err)
	}
	p.Queues[casualGroup] = g.now()

	report := func(s *session.Session, id uint64, ev gcpb.CMsgGC_Frontress_AntiCheatEvent_EEvent, enforced bool) {
		t.Helper()
		raw, err := wire.Encode(uint32(gcpb.ETFGCMsg_k_EMsgGC_Frontress_AntiCheatEvent), &gcpb.CMsgGC_Frontress_AntiCheatEvent{
			SteamId:       proto.Uint64(id),
			ProductUserId: proto.String("0002aabbccdd"),
			Event:         ev.Enum(),
			ActionReason:  proto.Uint32(10),
			Details:       proto.String("test"),
			Enforced:      proto.Bool(enforced),
		}, wire.NoJob)
		if err != nil {
			t.Fatal(err)
		}
		g.Handle(s, raw)
	}

	// A community server's word is recorded, never acted on.
	report(community, cheater.SteamID, gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_PERM_BANNED, true)
	if why := g.antiCheatBlocked(cheater.SteamID); why != "" {
		t.Fatalf("community server banned a player: %s", why)
	}
	// A soft-rollout server (fr_eac_enforce 0) is not enforcing.
	report(official, cheater.SteamID, gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_PERM_BANNED, false)
	if why := g.antiCheatBlocked(cheater.SteamID); why != "" {
		t.Fatalf("an unenforced verdict banned a player: %s", why)
	}
	// Not a player.
	report(official, 0, gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_PERM_BANNED, true)

	report(official, cheater.SteamID, gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_PERM_BANNED, true)
	if why := g.antiCheatBlocked(cheater.SteamID); !strings.Contains(why, "banned") {
		t.Fatalf("trusted ban not applied: %q", why)
	}
	if len(p.Queues) != 0 {
		t.Fatal("the sanctioned player's party is still queued")
	}
	if g.antiCheatBlocked(friend.SteamID) != "" {
		t.Fatal("the ban spilled over to a party member")
	}

	// A temporary ban never shortens a permanent one.
	report(official, cheater.SteamID, gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_TEMP_BANNED, true)
	if sc, ok := g.anticheat.Sanctioned(cheater.SteamID, g.now()); !ok || !sc.Until.IsZero() {
		t.Fatalf("permanent ban was shortened: %+v", sc)
	}

	// Survives a restart.
	reloaded, err := anticheat.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := reloaded.Get(cheater.SteamID)
	if !ok || rec.ProductUserID != "0002aabbccdd" || len(rec.Events) != 4 || rec.Sanction == nil {
		t.Fatalf("record did not persist: %+v", rec)
	}

	if err := g.AntiCheatClear(cheater.SteamID, "appeal accepted", "admin:1"); err != nil {
		t.Fatal(err)
	}
	if g.antiCheatBlocked(cheater.SteamID) != "" {
		t.Fatal("cleared ban still applies")
	}
}

func TestAntiCheatTemporaryBanExpires(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	g, err := New(Deps{Now: func() time.Time { return now }, Config: config.Config{AntiCheat: config.AntiCheatConfig{TempBanHours: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.AntiCheatBan(steamauth.MinIndividual+3, 2, "smurfing a banned account", "admin:1"); err != nil {
		t.Fatal(err)
	}
	if g.antiCheatBlocked(steamauth.MinIndividual+3) == "" {
		t.Fatal("manual ban not applied")
	}
	now = now.Add(2*time.Hour + time.Second)
	if why := g.antiCheatBlocked(steamauth.MinIndividual + 3); why != "" {
		t.Fatalf("expired ban still applies: %s", why)
	}
}

func TestServersWithoutAntiCheatGetNoMatchesWhenRequired(t *testing.T) {
	g, err := New(Deps{Config: config.Config{AntiCheat: config.AntiCheatConfig{RequireProtectedServers: true}}})
	if err != nil {
		t.Fatal(err)
	}
	s := g.sessions.Open(session.Server, 90071992547409923, func(s *session.Session) { s.Connect = "203.0.113.5:27015" })
	g.ServerConnected(s)
	srv := g.servers[s.SteamID]
	srv.Ready = true
	if g.serverEligible(srv, g.now()) {
		t.Fatal("an unprotected server was eligible")
	}
	srv.Protected = true
	if !g.serverEligible(srv, g.now()) {
		t.Fatal("a protected idle server was not eligible")
	}
	g.cfg.AntiCheat.RequireProtectedServers = false
	srv.Protected = false
	if !g.serverEligible(srv, g.now()) {
		t.Fatal("protection was required when the config does not ask for it")
	}
}
