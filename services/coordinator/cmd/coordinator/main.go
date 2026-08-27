// Command coordinator runs the Team Frontress matchmaking coordinator.
//
//	coordinator -config coordinator.json
//
// It holds the queue, forms matches, reserves a game server for each one and
// tells the clients where to connect. Everything else — the party, the invite,
// the chat — is Steam's, and happens in the game client.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/api"
	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/players"
	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/steamauth"
	"github.com/gru2007/team-frontress/services/coordinator/internal/war"
)

// version is stamped by the build (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "coordinator:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "coordinator.json", "path to the config file")
		logLevel    = flag.String("log-level", "info", "debug, info, warn or error")
		printCfg    = flag.Bool("print-config", false, "write the default config to stdout and exit")
		showVersion = flag.Bool("version", false, "print the build version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	if *printCfg {
		return writeDefaultConfig(os.Stdout)
	}

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("-log-level %q: %w", *logLevel, err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	var verifier steamauth.Verifier = steamauth.DevVerifier{}
	if cfg.Auth.Verified() {
		verifier = &steamauth.WebAPIVerifier{APIKey: cfg.Auth.SteamAPIKey, AppIDs: cfg.Auth.AppIDList()}
	} else {
		log.Warn("auth.mode is dev: clients are believed about who they are. Do not run this on the public internet.")
	}

	registry := pool.NewRegistry(60 * time.Second)
	srvPool, err := pool.New(cfg.Pool, registry)
	if err != nil {
		return err
	}

	var warEngine *war.Engine
	if cfg.War.Enabled {
		theater, err := war.LoadTheater(cfg.War.TheaterFile)
		if err != nil {
			return err
		}
		logPath := cfg.War.EventLog
		if logPath == "" {
			logPath = "war-events.jsonl"
		}
		warLog, past, err := war.OpenLog(logPath)
		if err != nil {
			return err
		}
		defer warLog.Close()
		warEngine, err = war.NewEngine(theater, warLog, past)
		if err != nil {
			return err
		}
		log.Info("war enabled", "theater", theater.ID, "campaign", warEngine.Campaign(), "events", len(past))
	}

	// The player records. Restrictions that talk about the past -- matches
	// played, matches abandoned -- are only enforceable because of this, and
	// only survive a restart when players.file is set.
	records, err := players.New(cfg.Players.File)
	if err != nil {
		return err
	}
	defer records.Close()
	if cfg.Players.File != "" {
		log.Info("player records", "file", cfg.Players.File, "known", records.Known())
	}

	matchmaker := mm.New(cfg, srvPool, mm.NewRCONSetup(cfg.Name+" | %s"), warEngine, log)
	matchmaker.UsePlayers(records)
	handler := api.New(cfg, matchmaker, verifier, registry, warEngine, records, log).Handler()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go matchmaker.Run(ctx)
	go pruneRegistry(ctx, registry)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen, "name", cfg.Name, "auth", cfg.Auth.Mode, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func pruneRegistry(ctx context.Context, reg *pool.Registry) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reg.Prune()
		}
	}
}

func writeDefaultConfig(w *os.File) error {
	cfg := config.Defaults()
	cfg.Secret = "change-me"
	cfg.MatchGroups[0].Maps = []string{"cp_process_final", "koth_product_final", "pl_upward"}
	cfg.Pool.Providers = []config.ProviderConfig{{
		Kind: "static",
		Servers: []config.StaticServer{{
			Name: "local", Connect: "127.0.0.1:27015", RCON: "change-me",
		}},
	}}
	enc := newIndentEncoder(w)
	return enc.Encode(cfg)
}

func newIndentEncoder(w *os.File) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc
}
