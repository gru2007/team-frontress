package gc

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gru2007/team-frontress/services/gc/internal/anticheat"
	"github.com/gru2007/team-frontress/services/gc/internal/steamban"
)

// Steam game bans: the "1 game ban on record" on a Steam profile, issued
// through ICheatReportingService with the publisher key
// (anticheat.steam_game_bans in the config).
//
// A Steam ban is permanent and public, so the GC only issues one on top of a
// permanent matchmaking ban: a moderator's (admin API, console) or, with mode
// "auto", Easy Anti-Cheat's. Steam then keeps the account off secure servers
// of the banned apps by itself.

// SteamBanner is ICheatReportingService; *steamban.Client in production.
type SteamBanner interface {
	ReportPlayerCheating(ctx context.Context, r steamban.Report) (uint64, error)
	RequestPlayerGameBan(ctx context.Context, b steamban.Ban) error
	RemovePlayerGameBan(ctx context.Context, steamID uint64, appID uint32) error
}

var (
	ErrSteamBansOff      = errors.New("steam game bans are off (anticheat.steam_game_bans.mode)")
	ErrSteamBanPending   = errors.New("a steam game ban for this player is already in progress")
	ErrSteamBanned       = errors.New("this player already has a steam game ban from us")
	ErrNotSteamBanned    = errors.New("this player has no steam game ban from us")
	ErrSteamBanNeedsPerm = errors.New("a steam game ban goes with a permanent ban only")
)

// Where a Steam ban came from: what Steam's report says it is.
type steamBanSource int

const (
	steamBanModerator steamBanSource = iota // reports reviewed by a moderator
	steamBanEAC                             // an Easy Anti-Cheat detection
)

func (g *GC) steamBansOn() bool {
	return g.steamBans != nil && g.cfg.AntiCheat.SteamGameBans.Enabled()
}

// SteamGameBansEnabled is for the admin API and the console.
func (g *GC) SteamGameBansEnabled() bool { return g.steamBansOn() }

// SteamGameBan bans a player on Steam in every configured app. It returns at
// once; the outcome lands in the player's anti-cheat record (steam_game_ban
// or steam_game_ban_failed).
func (g *GC) SteamGameBan(id uint64, reason, by string) error {
	return g.steamGameBan(id, reason, by, steamBanModerator)
}

func (g *GC) steamGameBan(id uint64, reason, by string, source steamBanSource) error {
	if !g.steamBansOn() {
		return ErrSteamBansOff
	}
	if g.steamBanBusy[id] {
		return ErrSteamBanPending
	}
	if rec, ok := g.anticheat.Get(id); ok && rec.SteamBan != nil {
		return ErrSteamBanned
	}
	cfg := g.cfg.AntiCheat.SteamGameBans
	apps := append([]uint32(nil), cfg.AppIDs...)
	delay := !cfg.Immediate
	description := truncate(strings.TrimSpace(reason), 200)
	if description == "" {
		description = "Cheating"
	}
	if g.steamBanBusy == nil {
		g.steamBanBusy = map[uint64]bool{}
	}
	g.steamBanBusy[id] = true

	var banned []uint32
	var reportIDs []string
	var failures []string
	bans := g.steamBans
	g.offSteam(func(ctx context.Context) error {
		for _, app := range apps {
			reportID, err := bans.ReportPlayerCheating(ctx, steamban.Report{
				SteamID:      id,
				AppID:        app,
				Detection:    source == steamBanEAC,
				PlayerReport: source == steamBanModerator,
			})
			if err == nil {
				err = bans.RequestPlayerGameBan(ctx, steamban.Ban{
					SteamID: id, AppID: app, ReportID: reportID, Description: description, Delay: delay,
				})
			}
			if err != nil {
				failures = append(failures, fmt.Sprintf("app %d: %v", app, err))
				continue
			}
			banned = append(banned, app)
			reportIDs = append(reportIDs, strconv.FormatUint(reportID, 10))
		}
		return nil
	}, func(error) {
		delete(g.steamBanBusy, id)
		now := g.now()
		if len(banned) > 0 {
			ban := &anticheat.SteamBan{At: now, AppIDs: banned, ReportIDs: reportIDs, Reason: description, By: by}
			ev := anticheat.Event{At: now, Kind: anticheat.EventSteamBan, Server: by, Enforced: true,
				Details: truncate(fmt.Sprintf("apps %v; %s", banned, description), 300)}
			if err := g.anticheat.SetSteamBan(id, ban, ev); err != nil {
				g.log.Error("save steam game ban", "steamid", id, "err", err)
			}
			g.log.Info("steam game ban", "steamid", id, "apps", banned, "by", by, "delayed", delay)
		}
		if len(failures) > 0 {
			ev := anticheat.Event{At: now, Kind: anticheat.EventSteamBanFailed, Server: by,
				Details: truncate(strings.Join(failures, "; "), 300)}
			if err := g.anticheat.SetSteamBan(id, nil, ev); err != nil {
				g.log.Error("save steam game ban failure", "steamid", id, "err", err)
			}
			g.log.Error("steam game ban failed", "steamid", id, "by", by, "failures", failures)
		}
	})
	return nil
}

// SteamGameUnban lifts the Steam ban the GC issued, in the apps it was issued
// in. Like SteamGameBan it returns at once.
func (g *GC) SteamGameUnban(id uint64, reason, by string) error {
	if g.steamBans == nil {
		return ErrSteamBansOff
	}
	if g.steamBanBusy[id] {
		return ErrSteamBanPending
	}
	rec, ok := g.anticheat.Get(id)
	if !ok || rec.SteamBan == nil {
		return ErrNotSteamBanned
	}
	apps := append([]uint32(nil), rec.SteamBan.AppIDs...)
	if g.steamBanBusy == nil {
		g.steamBanBusy = map[uint64]bool{}
	}
	g.steamBanBusy[id] = true

	var failures []string
	bans := g.steamBans
	g.offSteam(func(ctx context.Context) error {
		for _, app := range apps {
			if err := bans.RemovePlayerGameBan(ctx, id, app); err != nil {
				failures = append(failures, fmt.Sprintf("app %d: %v", app, err))
			}
		}
		return nil
	}, func(error) {
		delete(g.steamBanBusy, id)
		now := g.now()
		if len(failures) > 0 {
			ev := anticheat.Event{At: now, Kind: anticheat.EventSteamBanFailed, Server: by,
				Details: truncate("lifting: "+strings.Join(failures, "; "), 300)}
			_ = g.anticheat.SetSteamBan(id, nil, ev)
			g.log.Error("steam game unban failed", "steamid", id, "by", by, "failures", failures)
			return
		}
		ev := anticheat.Event{At: now, Kind: anticheat.EventSteamUnban, Server: by, Enforced: true,
			Details: truncate(reason, 300)}
		if err := g.anticheat.SetSteamBan(id, nil, ev); err != nil {
			g.log.Error("save steam game unban", "steamid", id, "err", err)
		}
		g.log.Info("steam game ban lifted", "steamid", id, "apps", apps, "by", by)
	})
	return nil
}

// forwardCheatingReport files a player's "cheating" report with Steam too
// (forward_player_reports), where Steamworks lists it. Nothing is banned on
// it: Steam gets no report id back.
func (g *GC) forwardCheatingReport(reporter, target uint64, appID uint32, detail uint32) {
	cfg := g.cfg.AntiCheat.SteamGameBans
	if g.steamBans == nil || !cfg.ForwardPlayerReports {
		return
	}
	if appID == 0 && len(cfg.AppIDs) > 0 {
		appID = cfg.AppIDs[0]
	}
	if appID == 0 {
		return
	}
	bans := g.steamBans
	g.offSteam(func(ctx context.Context) error {
		_, err := bans.ReportPlayerCheating(ctx, steamban.Report{
			SteamID: target, AppID: appID, Reporter: reporter, PlayerReport: true, NoReportID: true,
			AppData: uint64(detail),
		})
		return err
	}, func(err error) {
		if err != nil {
			g.log.Warn("forward report to steam", "target", target, "err", err)
		}
	})
}
