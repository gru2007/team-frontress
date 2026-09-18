package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type config struct {
	BotToken      string
	PublicURL     string
	Admins        map[int64]bool
	TestersChatID int64
	DatabasePath  string
	ListenAddr    string
}

func loadConfig() (config, error) {
	c := config{BotToken: strings.TrimSpace(os.Getenv("BOT_TOKEN")), PublicURL: strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_URL")), "/"), Admins: map[int64]bool{}, DatabasePath: os.Getenv("DATABASE_PATH"), ListenAddr: os.Getenv("LISTEN_ADDR")}
	if c.BotToken == "" {
		return c, errors.New("BOT_TOKEN is required")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || c.PublicURL != "https://"+u.Host {
		return c, errors.New("PUBLIC_URL must be an HTTPS origin without path, query or credentials")
	}
	for _, value := range strings.FieldsFunc(os.Getenv("ADMIN_TELEGRAM_IDS"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return c, errors.New("ADMIN_TELEGRAM_IDS must contain positive numeric IDs")
		}
		c.Admins[id] = true
	}
	if value := strings.TrimSpace(os.Getenv("TESTERS_CHAT_ID")); value != "" {
		c.TestersChatID, err = strconv.ParseInt(value, 10, 64)
		if err != nil || c.TestersChatID >= 0 {
			return c, errors.New("TESTERS_CHAT_ID must be a negative group ID")
		}
	}
	if c.DatabasePath == "" {
		c.DatabasePath = "recruiting-bot.db"
	}
	c.DatabasePath, err = filepath.Abs(c.DatabasePath)
	if err != nil {
		return c, errors.New("invalid DATABASE_PATH")
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	return c, nil
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	db, err := openStore(cfg.DatabasePath)
	if err != nil {
		return errors.New("cannot initialize SQLite database")
	}
	defer db.Close()
	client := &http.Client{Timeout: 40 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	a := &app{cfg: cfg, db: db, client: client, telegramBase: "https://api.telegram.org"}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.db.ensureAccessSchema(ctx); err != nil {
		return errors.New("cannot initialize key access controls")
	}
	// A configured webhook prevents getUpdates. Preserve all pending updates.
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = a.telegram(startup, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
	cancel()
	if err != nil {
		return errors.New("cannot initialize Telegram long polling")
	}
	if cfg.TestersChatID != 0 {
		startup, cancel = context.WithTimeout(ctx, 15*time.Second)
		err = a.validateGroup(startup)
		cancel()
		switch {
		case errors.Is(err, errPublicGroupJoinRequestsDisabled):
			return errors.New("public testers group must require administrator approval for new members")
		case errors.Is(err, errJoinRequestQueriesUnsupported):
			return errors.New("Telegram bot does not support join request queries")
		case errors.Is(err, errGuardBotNotConfigured):
			return errors.New("recruiting bot must be assigned to Process Join Requests in the testers group")
		case err != nil:
			return errors.New("cannot validate testers group")
		}
		startup, cancel = context.WithTimeout(ctx, 15*time.Second)
		err = a.validateRemovalRights(startup)
		cancel()
		if errors.Is(err, errCannotRemoveMembers) {
			return errors.New("recruiting bot must have permission to restrict/remove testers group members")
		}
		if err != nil {
			return errors.New("cannot validate testers group removal permissions")
		}
	}
	startup, cancel = context.WithTimeout(ctx, 30*time.Second)
	err = a.reconcileGroupAccessOnce(startup)
	cancel()
	if err != nil {
		return errors.New("cannot reconcile testers group access")
	}
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 55 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(_ net.Listener) context.Context { return ctx }}
	var wg sync.WaitGroup
	for _, worker := range []func(context.Context){a.poll, a.deliver, a.cleanup, a.enforceGroupAccess} {
		wg.Add(1)
		go func(f func(context.Context)) { defer wg.Done(); f(ctx) }(worker)
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- srv.ListenAndServe() }()
	log.Print("recruiting bot started")
	select {
	case <-ctx.Done():
	case err = <-serverErr:
		stop()
	}
	shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	if shutdownErr := srv.Shutdown(shutdown); shutdownErr != nil {
		_ = srv.Close()
	}
	wg.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("HTTP server failed")
	}
	return nil
}

func (a *app) cleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		now := time.Now().Unix()
		for _, table := range []string{"sessions", "steam_flows", "steam_nonces", "join_queries"} {
			if _, err := a.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE expires_at<?", now); err != nil && ctx.Err() == nil {
				log.Print("expired auth cleanup failed")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
