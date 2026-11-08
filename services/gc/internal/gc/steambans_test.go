package gc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/anticheat"
	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/steamban"
)

type fakeSteamBans struct {
	mu      sync.Mutex
	reports []steamban.Report
	bans    []steamban.Ban
	removed []uint32
	failApp uint32
	nextID  uint64
}

func (f *fakeSteamBans) ReportPlayerCheating(_ context.Context, r steamban.Report) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, r)
	if r.AppID == f.failApp {
		return 0, errors.New("HTTP 403")
	}
	if r.NoReportID {
		return 0, nil
	}
	f.nextID++
	return 1000 + f.nextID, nil
}

func (f *fakeSteamBans) RequestPlayerGameBan(_ context.Context, b steamban.Ban) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bans = append(f.bans, b)
	return nil
}

func (f *fakeSteamBans) RemovePlayerGameBan(_ context.Context, _ uint64, app uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, app)
	return nil
}

// runOne runs the next function queued on the loop: the result of a Steam
// call made off it.
func runOne(t *testing.T, g *GC) {
	t.Helper()
	select {
	case f := <-g.loop:
		f()
	case <-time.After(5 * time.Second):
		t.Fatal("nothing came back from the Steam call")
	}
}

func steamBanGC(t *testing.T, mode string, fake *fakeSteamBans) *GC {
	t.Helper()
	ac, err := anticheat.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	cfg.AntiCheat.SteamGameBans = config.SteamGameBansConfig{Mode: mode, AppIDs: []uint32{5147380, 5147520}}
	g, err := New(Deps{Config: cfg, AntiCheat: ac, SteamBans: fake})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestSteamGameBanIsReportedThenBannedInEveryApp(t *testing.T) {
	fake := &fakeSteamBans{failApp: 5147520}
	g := steamBanGC(t, "manual", fake)
	const cheater = steamauth.MinIndividual + 7

	if err := g.SteamGameBan(cheater, "aimbot, demo checked", "admin:1"); err != nil {
		t.Fatal(err)
	}
	if err := g.SteamGameBan(cheater, "again", "admin:1"); !errors.Is(err, ErrSteamBanPending) {
		t.Fatalf("a second ban while the first is on its way: %v", err)
	}
	runOne(t, g)

	if len(fake.bans) != 1 || fake.bans[0].AppID != 5147380 || fake.bans[0].ReportID != 1001 ||
		!fake.bans[0].Delay || fake.bans[0].Duration != 0 || fake.bans[0].Description != "aimbot, demo checked" {
		t.Fatalf("bans: %+v", fake.bans)
	}
	if !fake.reports[0].PlayerReport || fake.reports[0].Detection {
		t.Fatalf("a moderator's ban is filed as a player report: %+v", fake.reports[0])
	}
	rec, _ := g.anticheat.Get(cheater)
	if rec.SteamBan == nil || len(rec.SteamBan.AppIDs) != 1 || rec.SteamBan.ReportIDs[0] != "1001" {
		t.Fatalf("record: %+v", rec.SteamBan)
	}
	kinds := map[string]bool{}
	for _, ev := range rec.Events {
		kinds[ev.Kind] = true
	}
	if !kinds[anticheat.EventSteamBan] || !kinds[anticheat.EventSteamBanFailed] {
		t.Fatalf("the ban and the app that refused should both be on record: %+v", rec.Events)
	}
	if err := g.SteamGameBan(cheater, "again", "admin:1"); !errors.Is(err, ErrSteamBanned) {
		t.Fatalf("banning twice: %v", err)
	}

	if err := g.SteamGameUnban(cheater, "appeal accepted", "admin:2"); err != nil {
		t.Fatal(err)
	}
	runOne(t, g)
	rec, _ = g.anticheat.Get(cheater)
	if rec.SteamBan != nil || len(fake.removed) != 1 || fake.removed[0] != 5147380 {
		t.Fatalf("lifting: record %+v, removed %v", rec.SteamBan, fake.removed)
	}
}

func TestSteamGameBansOffAndAuto(t *testing.T) {
	off := steamBanGC(t, "off", &fakeSteamBans{})
	if err := off.SteamGameBan(steamauth.MinIndividual+1, "x", "admin:1"); !errors.Is(err, ErrSteamBansOff) {
		t.Fatalf("mode off: %v", err)
	}

	fake := &fakeSteamBans{}
	g := steamBanGC(t, "auto", fake)
	if err := g.steamGameBan(steamauth.MinIndividual+2, "Easy Anti-Cheat permanent ban", "eac:official", steamBanEAC); err != nil {
		t.Fatal(err)
	}
	runOne(t, g)
	if len(fake.bans) != 2 || !fake.reports[0].Detection || fake.reports[0].PlayerReport {
		t.Fatalf("an EAC ban is a detection, in both apps: reports %+v bans %+v", fake.reports, fake.bans)
	}
}
