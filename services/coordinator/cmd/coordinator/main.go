// Command coordinator runs the GREYLINE FRONTRESS Game Coordinator.
//
//	coordinator -config gc.json
//
// It owns three things and nothing else: the war (a node graph, staged fronts
// and an append-only event log), the queue of players who pressed DEPLOY, and
// the pool of dedicated servers those battles run on. Game servers play games;
// the coordinator decides which game is worth playing next and records what it
// did to the war.
//
// The peer-to-peer host election the earlier prototype used is retired. Its
// code is still in internal/legacy for reference, and nothing here calls it.
//
// See services/coordinator/README.md for the HTTP protocol and
// docs/GREYLINE-WAR.md for the strategic model.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/greyline-frontress/coordinator/internal/config"
	"github.com/greyline-frontress/coordinator/internal/hostelect"
	"github.com/greyline-frontress/coordinator/internal/httpapi"
	"github.com/greyline-frontress/coordinator/internal/mm"
	"github.com/greyline-frontress/coordinator/internal/pool"
	"github.com/greyline-frontress/coordinator/internal/steam"
	"github.com/greyline-frontress/coordinator/internal/war"
)

func main() {
	var (
		configPath  = flag.String("config", "", "path to the coordinator config JSON")
		theaterPath = flag.String("theater", "", "path to the theater definition (overrides the config)")
		warLogPath  = flag.String("war-log", "", "path to the war event log (overrides the config)")
		logLevel    = flag.String("log-level", "info", "debug | info | warn | error")
		printCfg    = flag.Bool("print-config", false, "write the effective default config to stdout and exit")
	)
	flag.Parse()

	if *printCfg {
		if err := dumpDefaults(); err != nil {
			fail(err)
		}
		return
	}

	log := newLogger(*logLevel)

	cfg, err := config.Load(*configPath)
	if err != nil {
		switch {
		case errors.Is(err, config.ErrNoSecret):
			fail(fmt.Errorf("%w\n\nGenerate one with:\n  head -c 32 /dev/urandom | base64", err))
		case errors.Is(err, config.ErrNoPoolKey):
			fail(fmt.Errorf("%w\n\nGenerate one with:\n  head -c 32 /dev/urandom | base64\n"+
				"Every game server in the pool needs it, and holding it means being able\n"+
				"to report battle results that move the war.", err))
		}
		fail(err)
	}
	if *theaterPath != "" {
		cfg.TheaterPath = *theaterPath
	}
	if *warLogPath != "" {
		cfg.WarLogPath = *warLogPath
	}

	theater, err := war.LoadTheater(cfg.TheaterPath)
	if err != nil {
		fail(err)
	}
	warLog, err := war.OpenLog(cfg.WarLogPath)
	if err != nil {
		fail(err)
	}
	engine, err := war.NewEngine(theater, warLog, war.Rules{
		PlayersPerFront:     cfg.War.PlayersPerFront,
		MaxFronts:           cfg.War.MaxFronts,
		MobilizationDeficit: cfg.War.MobilizationDeficit,
		Intermission:        cfg.War.Intermission.D(),
		CampaignName:        cfg.War.CampaignName,
		AttackerTeam:        cfg.War.AttackerTeam,
		FullStrengthPlayers: cfg.War.FullStrengthPlayers,
		MinBattleWeight:     cfg.War.MinBattleWeight,
	})
	if err != nil {
		fail(err)
	}

	auth, err := buildAuthenticator(cfg, log)
	if err != nil {
		fail(err)
	}

	servers := pool.New(cfg.Pool.OfflineAfter.D())
	servers.SetP2POfflineAfter(cfg.Pool.OfflineAfterP2P.D())
	// A server still loading its map is judged by the battle's own boot
	// deadline, not by the heartbeat rule — see pool.silenceBudgetFor. The
	// extra minute of slack is so the matchmaker's BootDeadline is what ends a
	// boot that really has stalled, with the reason that actually explains it,
	// rather than the pool declaring the host gone a moment earlier.
	servers.SetBootGrace(cfg.Timing.HostBootDeadline.D() + time.Minute)
	maker := mm.New(matchmakingConfig(cfg), log, engine, servers)
	api := httpapi.New(log, auth, engine, maker, servers, httpapi.Options{
		PoolKey:         cfg.Pool.Key,
		AdminKey:        cfg.Pool.AdminKey,
		MaxPollWait:     cfg.Pool.PollWait.D(),
		ClientVersions:  cfg.AcceptedClientVersions,
		ProtocolVersion: int(cfg.ProtocolVersion),
	})

	snap := engine.Snapshot()
	log.Info("war loaded",
		"theater", snap.Theater.Name,
		"campaign", snap.Campaign.Name,
		"status", snap.Campaign.Status,
		"revision", snap.Revision,
		"nodes", len(snap.Nodes),
		"active_fronts", len(engine.ActiveFronts()),
		"events", warLog.Seq())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go maker.Run(ctx, cfg.Timing.TickInterval.D())
	admin := startAdmin(ctx, cfg.AdminListen, api, log)

	serveErr := httpapi.Serve(ctx, log, cfg.APIListen, api.Handler())
	shutdownAdmin(admin)
	// The log is the war, so closing it cleanly matters more than a tidy exit
	// code: a flushed log is a world that comes back.
	if err := engine.Close(); err != nil {
		log.Error("could not close the war log cleanly", "err", err)
	}
	if serveErr != nil {
		log.Error("coordinator stopped with an error", "err", serveErr)
		os.Exit(1)
	}
	log.Info("coordinator stopped cleanly")
}

// matchmakingConfig maps the operator's configuration onto the matchmaker's.
func matchmakingConfig(cfg *config.Config) mm.Config {
	c := mm.DefaultConfig()
	if len(cfg.Match.TeamSizes) > 0 {
		c.TeamSizes = cfg.Match.TeamSizes
	}
	if cfg.Match.MinTeamSize > 0 {
		c.MinTeamSize = cfg.Match.MinTeamSize
	}
	if cfg.Match.FormWait > 0 {
		c.FormWait = cfg.Match.FormWait.D()
	}
	if cfg.Match.MaxConcurrentPerFront > 0 {
		c.MaxBattlesPerFront = cfg.Match.MaxConcurrentPerFront
	}
	if cfg.Timing.HostBootDeadline > 0 {
		c.BootDeadline = cfg.Timing.HostBootDeadline.D()
	}
	if cfg.Timing.ClientConnectDeadline > 0 {
		c.JoinDeadline = cfg.Timing.ClientConnectDeadline.D()
	}
	if cfg.Timing.SessionTimeout > 0 {
		c.SessionTimeout = cfg.Timing.SessionTimeout.D()
	}
	if cfg.Timing.HeartbeatInterval > 0 {
		c.HeartbeatInterval = cfg.Timing.HeartbeatInterval.D()
	}
	if cfg.Match.WidenAfter > 0 {
		c.WidenAfter = cfg.Match.WidenAfter.D()
	}
	if cfg.Match.WidenStepBonus > 0 {
		c.WidenStepBonus = cfg.Match.WidenStepBonus
	}
	if cfg.Match.WidenMaxSteps > 0 {
		c.WidenMaxSteps = cfg.Match.WidenMaxSteps
	}
	if cfg.Timing.HostAcceptDeadline > 0 {
		c.HostAcceptDeadline = cfg.Timing.HostAcceptDeadline.D()
	}
	if cfg.Timing.HostFailureCooldown > 0 {
		c.HostFailureCooldown = cfg.Timing.HostFailureCooldown.D()
	}
	// Only verified Steam auth produces a roster a game server can turn people
	// away against. See pool.Assignment.VerifiedIdentities.
	c.VerifiedIdentities = cfg.Auth.Mode == config.AuthWebAPI
	c.HostElection = hostelect.Weights{
		Upload: cfg.Election.WeightUpload, Latency: cfg.Election.WeightLatency,
		CPU: cfg.Election.WeightCPU, Stability: cfg.Election.WeightStability,
		DedicatedBonus: cfg.Election.DedicatedBonus, PublicIPBonus: cfg.Election.PublicIPBonus,
		AbandonPenalty: cfg.Election.AbandonPenalty,
	}
	c.HostRequirements = hostelect.Requirements{
		MinUploadKbps: cfg.Election.MinUploadKbps, MinCPUScore: cfg.Election.MinCPUScore,
		MinMemoryMB: cfg.Election.MinMemoryMB, MaxAcceptableRTT: cfg.Election.MaxAcceptableRTT,
		RequireCanHost: cfg.Election.RequireCanHostBit,
	}
	return c
}

func buildAuthenticator(cfg *config.Config, log *slog.Logger) (steam.Authenticator, error) {
	switch cfg.Auth.Mode {
	case config.AuthWebAPI:
		return steam.NewWebAPIAuthenticator(
			cfg.Auth.WebAPIKey,
			cfg.Auth.AppID,
			cfg.Auth.RejectVACBanned,
			cfg.Auth.RequireOwnership,
			cfg.Auth.TicketCacheTTL.D(),
		), nil
	case config.AuthDev:
		log.Warn("auth_mode=dev: clients are trusted to state their own SteamID. " +
			"Use this only on a network you control")
		return steam.DevAuthenticator{}, nil
	default:
		return nil, fmt.Errorf("unsupported auth mode %q", cfg.Auth.Mode)
	}
}

func startAdmin(ctx context.Context, addr string, api *httpapi.API, log *slog.Logger) *http.Server {
	if addr == "" {
		return nil
	}
	h := &http.Server{
		Addr:              addr,
		Handler:           api.AdminHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("admin endpoint listening", "addr", addr)
		if err := h.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("admin endpoint failed", "err", err)
		}
	}()
	return h
}

func shutdownAdmin(h *http.Server) {
	if h == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = h.Shutdown(ctx)
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func dumpDefaults() error {
	cfg := config.Default()
	cfg.Security.Secret = "REPLACE-ME-WITH-A-32-BYTE-RANDOM-SECRET"
	cfg.Pool.Key = "REPLACE-ME-WITH-A-32-BYTE-RANDOM-POOL-KEY"
	enc := newJSONEncoder()
	return enc(cfg)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "coordinator:", err)
	os.Exit(1)
}
