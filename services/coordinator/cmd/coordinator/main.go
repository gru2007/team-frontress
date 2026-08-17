// Command coordinator runs the GREYLINE FRONTRESS Game Coordinator.
//
//	coordinator -config gc.json -world world.json
//
// See services/coordinator/README.md for the protocol and the game-side
// integration it expects.
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
	"github.com/greyline-frontress/coordinator/internal/gc"
	"github.com/greyline-frontress/coordinator/internal/security"
	"github.com/greyline-frontress/coordinator/internal/steam"
	"github.com/greyline-frontress/coordinator/internal/war"
)

func main() {
	var (
		configPath = flag.String("config", "", "path to the coordinator config JSON")
		worldPath  = flag.String("world", "world.json", "path to the war world file")
		logLevel   = flag.String("log-level", "info", "debug | info | warn | error")
		printCfg   = flag.Bool("print-config", false, "write the effective default config to stdout and exit")
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
		if errors.Is(err, config.ErrNoSecret) {
			fail(fmt.Errorf("%w\n\nGenerate one with:\n  head -c 32 /dev/urandom | base64", err))
		}
		fail(err)
	}

	world, err := war.LoadFile(*worldPath)
	if err != nil {
		fail(err)
	}

	policy := security.DefaultPolicy()
	if cfg.Security.PolicyPath != "" {
		policy, err = security.LoadPolicy(cfg.Security.PolicyPath)
		if err != nil {
			fail(err)
		}
	}

	auth, err := buildAuthenticator(cfg, log)
	if err != nil {
		fail(err)
	}

	players, err := gc.LoadPlayerStore(cfg.StatePath)
	if err != nil {
		fail(err)
	}

	srv := gc.New(cfg, log, auth, world, policy, players)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	admin := startAdmin(ctx, cfg.AdminListen, srv, log)

	if err := srv.Run(ctx); err != nil {
		log.Error("coordinator stopped with an error", "err", err)
		shutdownAdmin(admin)
		os.Exit(1)
	}
	shutdownAdmin(admin)
	log.Info("coordinator stopped cleanly")
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

func startAdmin(ctx context.Context, addr string, srv *gc.Server, log *slog.Logger) *http.Server {
	if addr == "" {
		return nil
	}
	h := &http.Server{
		Addr:              addr,
		Handler:           srv.AdminHandler(),
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
	enc := newJSONEncoder()
	return enc(cfg)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "coordinator:", err)
	os.Exit(1)
}
