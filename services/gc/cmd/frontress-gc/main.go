// Command frontress-gc is the Team Frontress game coordinator.
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
	"path/filepath"
	"syscall"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/clans"
	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gc"
	"github.com/gru2007/team-frontress/services/gc/internal/httpapi"
	"github.com/gru2007/team-frontress/services/gc/internal/players"
	"github.com/gru2007/team-frontress/services/gc/internal/sink"
	"github.com/gru2007/team-frontress/services/gc/internal/state"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/support"
	"github.com/gru2007/team-frontress/services/gc/internal/war"
)

func main() {
	path := flag.String("config", "gc.json", "config file")
	debug := flag.Bool("debug", false, "log every unhandled message")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(*path, log); err != nil {
		log.Error("frontress-gc stopped", "err", err)
		os.Exit(1)
	}
}

func run(path string, log *slog.Logger) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	file := func(name string) string {
		if cfg.DataDir == "" {
			return ""
		}
		return filepath.Join(cfg.DataDir, name)
	}
	if cfg.DataDir != "" {
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			return err
		}
	}

	ps, err := players.Open(file("players.jsonl"))
	if err != nil {
		return err
	}
	defer ps.Close()

	cs, err := clans.Open(file("clans.jsonl"))
	if err != nil {
		return err
	}
	defer cs.Close()
	book, err := support.OpenBook(file("support.jsonl"))
	if err != nil {
		return err
	}
	defer book.Close()
	var steam support.Steam
	switch {
	case !cfg.Support.Enabled:
	case cfg.Support.Fake:
		steam = &support.Fake{}
		log.Warn("support.fake: donations are approved without Steam. Never run this in public.")
	default:
		steam = &support.WebAPI{Key: cfg.Support.PublisherKey, AppID: cfg.Support.AppID, Sandbox: cfg.Support.Sandbox}
	}

	var engine *war.Engine
	if cfg.War.Enabled {
		theater, err := war.LoadTheater(cfg.War.Theater)
		if err != nil {
			return fmt.Errorf("war: %w", err)
		}
		// The war's log is its state; it is never kept only in memory.
		wpath := file("war.jsonl")
		if wpath == "" {
			wpath = "war.jsonl"
		}
		wlog, past, err := war.OpenLog(wpath)
		if err != nil {
			return fmt.Errorf("war: %w", err)
		}
		defer wlog.Close()
		if engine, err = war.NewEngine(theater, wlog, past); err != nil {
			return fmt.Errorf("war: %w", err)
		}
	}

	var auth steamauth.Verifier = steamauth.Dev{}
	if cfg.Auth.Verified() {
		auth = &steamauth.WebAPI{APIKey: cfg.Auth.SteamAPIKey, AppIDs: cfg.Auth.AppIDList(), Identity: cfg.Auth.Identity}
	} else {
		log.Warn("auth.mode is dev: the GC believes whatever SteamID a client claims. Never run this in public.")
	}

	hooks := sink.New(cfg.Webhooks, log)
	g, err := gc.New(gc.Deps{
		Config:  cfg,
		Log:     log,
		Players: ps,
		War:     engine,
		Store:   state.Open(file("matches.json")),
		Hooks:   hooks,
		Clans:   cs,
		Support: book,
		Steam:   steam,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go hooks.Run(ctx)
	loopDone := make(chan struct{})
	go func() { g.Run(ctx); close(loopDone) }()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           httpapi.New(g, auth, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Long enough for a long-poll plus slack.
		WriteTimeout: cfg.Timing.Poll() + 30*time.Second,
		IdleTimeout:  2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("frontress-gc listening", "addr", cfg.Listen, "name", cfg.Name,
		"groups", len(cfg.MatchGroups), "server_keys", len(cfg.ServerKeys), "servers", "pool",
		"war", engine != nil)

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	<-loopDone
	return nil
}
