// Command greyline-agent runs next to a Team Frontress dedicated server and
// tells the coordinator what is happening on it.
//
//	greyline-agent -coordinator http://gc:27100 -secret ... \
//	               -rcon 127.0.0.1:27015 -rcon-password ...
//
// Without it a match ends the only way the coordinator can end one on its own:
// when the server has been empty for a while, or when the clock runs out. That
// works, but it means a finished match holds its server for another five
// minutes, nobody's record is written, and the war never hears who won.
//
// The agent closes that loop with two stock server features and no game-side
// plugin:
//
//   - the match id is in sv_tags as "tfmm:<id>", which the coordinator put
//     there over RCON when it set the match up;
//   - the server sends its console log to whatever `logaddress_add` points at,
//     which is how the agent sees "Game_Over" and the final scores.
//
// Who was in the match comes from RCON `status` at the moment it ended, not
// from the log: the roster the coordinator handed out is who was *supposed* to
// play, and the difference between the two is exactly what an abandon is.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/rcon"
	"github.com/gru2007/team-frontress/services/coordinator/internal/srclog"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// version is stamped by the build (-ldflags "-X main.version=...").
var version = "dev"

type options struct {
	coordinator  string
	secret       string
	rconAddr     string
	rconPassword string
	connect      string
	logListen    string
	logAdvertise string
	interval     time.Duration
	logLevel     string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "greyline-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	opt := options{}
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.StringVar(&opt.coordinator, "coordinator", env("GC_URL", ""), "coordinator base URL, e.g. http://gc:27100")
	flag.StringVar(&opt.secret, "secret", env("GC_SECRET", ""), "the coordinator's shared server secret")
	flag.StringVar(&opt.rconAddr, "rcon", env("RCON_ADDR", "127.0.0.1:27015"), "the game server's RCON address")
	flag.StringVar(&opt.rconPassword, "rcon-password", env("RCON_PASSWORD", ""), "the game server's RCON password")
	flag.StringVar(&opt.connect, "connect", env("SERVER_CONNECT", ""), "the address players connect to, for the heartbeat")
	flag.StringVar(&opt.logListen, "log-listen", env("LOG_LISTEN", "127.0.0.1:27115"), "UDP address to receive the server's console log on")
	flag.StringVar(&opt.logAdvertise, "log-advertise", env("LOG_ADVERTISE", ""), "what to tell the server to log to (defaults to -log-listen)")
	flag.DurationVar(&opt.interval, "interval", 15*time.Second, "how often to heartbeat")
	flag.StringVar(&opt.logLevel, "log-level", "info", "debug, info, warn or error")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if opt.coordinator == "" || opt.secret == "" {
		return fmt.Errorf("-coordinator and -secret are required (or GC_URL and GC_SECRET)")
	}
	if opt.rconPassword == "" {
		return fmt.Errorf("-rcon-password is required: without RCON there is nothing to report")
	}

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(opt.logLevel)); err != nil {
		return fmt.Errorf("-log-level %q: %w", opt.logLevel, err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := &agent{opt: opt, log: log, client: &http.Client{Timeout: 15 * time.Second}}
	log.Info("starting", "coordinator", opt.coordinator, "rcon", opt.rconAddr, "version", version)
	return a.run(ctx)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// agent is the whole program: a log listener, a heartbeat loop, and the state
// of the match currently being played.
type agent struct {
	opt    options
	log    *slog.Logger
	client *http.Client

	mu       sync.Mutex
	matchID  string
	scores   map[string]int
	reported map[string]bool
	// gameReport is a result the game is in the middle of printing: the
	// header arrived, the per-player lines are still coming, and it is sent
	// when the closing line does. Nil when the game is not reporting, which is
	// every server without our game DLL.
	gameReport *wire.MatchResult
}

func (a *agent) run(ctx context.Context) error {
	conn, err := net.ListenPacket("udp", a.opt.logListen)
	if err != nil {
		return fmt.Errorf("listening for the server's log: %w", err)
	}
	defer conn.Close()
	a.log.Info("listening for console log", "addr", conn.LocalAddr())

	a.reported = map[string]bool{}
	a.scores = map[string]int{}

	go a.readLog(ctx, conn)

	// Point the server at us. It is idempotent -- logaddress_add on an address
	// already in the list is a no-op -- so it is also the reconnect path after
	// a server restart, which is why the heartbeat repeats it.
	a.subscribeLog(ctx)

	t := time.NewTicker(a.opt.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			a.tick(ctx)
		}
	}
}

// tick asks the server what it is doing and tells the coordinator.
func (a *agent) tick(ctx context.Context) {
	c, err := a.dial()
	if err != nil {
		a.log.Debug("rcon unavailable", "err", err)
		return
	}
	defer c.Close()

	tags, err := c.Exec("sv_tags")
	if err != nil {
		a.log.Debug("could not read sv_tags", "err", err)
		return
	}
	matchID := matchIDFromTags(rcon.TagValue(tags))

	a.mu.Lock()
	if matchID != a.matchID {
		// A new match on this server. Whatever we were counting belonged to
		// the previous one and is not ours to report any more.
		a.log.Info("match changed", "from", a.matchID, "to", matchID)
		a.matchID = matchID
		a.scores = map[string]int{}
	}
	a.mu.Unlock()

	if matchID == "" {
		return // an idle server between matches; nothing to say
	}

	status, err := c.Exec("status")
	if err != nil {
		return
	}
	players, _ := rcon.StatusPlayers(status)
	a.post(ctx, "/v1/gs/heartbeat", wire.ServerHeartbeat{
		Connect: a.opt.connect,
		Secret:  a.opt.secret,
		MatchID: matchID,
		Players: players,
	})
	// Re-subscribing costs one RCON command and fixes the case where the
	// server restarted and forgot us.
	if _, err := c.Exec(fmt.Sprintf("logaddress_add %s", a.advertiseAddr())); err != nil {
		a.log.Debug("could not re-add the log address", "err", err)
	}
}

// readLog turns console log packets into the one thing that ends a match.
func (a *agent) readLog(ctx context.Context, conn net.PacketConn) {
	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			continue // deadline, or a packet we could not read; both are fine
		}
		line, ok := srclog.Strip(buf[:n])
		if !ok {
			continue
		}
		ev, ok := srclog.Parse(line)
		if !ok {
			continue
		}
		a.handle(ctx, ev)
	}
}

func (a *agent) handle(ctx context.Context, ev srclog.Event) {
	switch ev.Kind {
	case srclog.KindReport:
		a.handleReport(ctx, ev)
	case srclog.KindTeamScore:
		if team := srclog.NormalizeTeam(ev.Team); team != "" {
			a.mu.Lock()
			a.scores[team] = ev.Score
			a.mu.Unlock()
		}
	case srclog.KindGameOver:
		a.reportResult(ctx, ev.Reason)
	case srclog.KindMapLoad:
		// A map change without a game over is a match that was cut short --
		// an admin, a crash, the coordinator setting up the next one. Drop the
		// scores rather than attaching them to whatever comes next.
		a.mu.Lock()
		a.scores = map[string]int{}
		a.mu.Unlock()
	}
}

// handleReport acts on a line the game's own matchmaking backend printed.
//
// This is the better source and it wins where it exists. The log-scraping path
// below reconstructs a result from console lines and an RCON `status`, which
// gets the winner and roughly who was there; the game knows who was in the
// match, who walked out of it and what everybody scored, because it has the
// match object. When the game reports, that report is what is sent.
func (a *agent) handleReport(ctx context.Context, ev srclog.Event) {
	matchID := ev.Fields["match_id"]
	if matchID == "" {
		return
	}

	switch ev.Event {
	case "match_result":
		a.mu.Lock()
		a.gameReport = &wire.MatchResult{
			MatchID:  matchID,
			Secret:   a.opt.secret,
			RedScore: atoiField(ev.Fields, "red"),
			BluScore: atoiField(ev.Fields, "blu"),
			Winner:   teamFromNumber(atoiField(ev.Fields, "winner")),
		}
		a.mu.Unlock()

	case "match_player":
		id := ev.Fields["steamid"]
		a.mu.Lock()
		if a.gameReport != nil && a.gameReport.MatchID == matchID && id != "" {
			a.gameReport.Players = append(a.gameReport.Players, wire.AssignedPlayer{
				SteamID: wire.SteamID(id),
				Team:    wire.Team(atoiField(ev.Fields, "team")),
			})
		}
		a.mu.Unlock()

	case "match_result_end":
		a.mu.Lock()
		res := a.gameReport
		already := a.reported[matchID]
		if res != nil {
			a.reported[matchID] = true
		}
		a.gameReport = nil
		a.mu.Unlock()

		if res == nil || already {
			return
		}
		a.log.Info("reporting the game's own result",
			"match", matchID, "red", res.RedScore, "blu", res.BluScore,
			"winner", res.Winner, "players", len(res.Players))
		a.post(ctx, "/v1/gs/result", *res)

	case "player_left":
		// Informational for now: the coordinator works out abandons from the
		// roster it handed out against the players in the result. Logging it
		// makes the two agree or visibly disagree.
		a.log.Info("a player left the match",
			"match", matchID, "player", ev.Fields["steamid"], "abandon", ev.Fields["abandon"])
	}
}

// atoiField reads a numeric field, or zero.
func atoiField(fields map[string]string, key string) int {
	n, err := strconv.Atoi(fields[key])
	if err != nil {
		return 0
	}
	return n
}

// teamFromNumber turns the game's team index into the coordinator's team.
func teamFromNumber(n int) wire.Team {
	switch n {
	case int(wire.TeamRed):
		return wire.TeamRed
	case int(wire.TeamBlu):
		return wire.TeamBlu
	}
	return wire.TeamUnassigned
}

// reportResult sends the finished match to the coordinator, once.
//
// The final scores come from the log; who was there comes from RCON, because
// the log would have to be replayed from the start of the match to answer it
// and a player who joined before the agent started would be missing.
func (a *agent) reportResult(ctx context.Context, reason string) {
	a.mu.Lock()
	matchID := a.matchID
	red, blu := a.scores["RED"], a.scores["BLU"]
	already := a.reported[matchID]
	if matchID != "" {
		a.reported[matchID] = true
	}
	a.mu.Unlock()

	if matchID == "" {
		a.log.Info("a match ended on a server with no match tag; nothing to report")
		return
	}
	if already {
		return // Game_Over can be logged more than once for one match
	}

	res := wire.MatchResult{
		MatchID:  matchID,
		Secret:   a.opt.secret,
		RedScore: red,
		BluScore: blu,
		Winner:   winner(red, blu),
	}
	if c, err := a.dial(); err == nil {
		if status, err := c.Exec("status"); err == nil {
			for _, p := range rcon.StatusRoster(status) {
				if p.Bot || p.SteamID == "" {
					continue
				}
				res.Players = append(res.Players, wire.AssignedPlayer{
					SteamID: wire.SteamID(p.SteamID),
					Name:    p.Name,
				})
			}
		}
		c.Close()
	}

	a.log.Info("match over", "match", matchID, "red", red, "blu", blu,
		"winner", res.Winner, "players", len(res.Players), "reason", reason)
	a.post(ctx, "/v1/gs/result", res)
}

// winner is the team a score line pair says won, or unassigned for a draw.
func winner(red, blu int) wire.Team {
	switch {
	case red > blu:
		return wire.TeamRed
	case blu > red:
		return wire.TeamBlu
	default:
		return wire.TeamUnassigned
	}
}

// matchIDFromTags pulls the match id out of sv_tags. The coordinator writes it
// as "tfmm:<id>", alongside whatever other tags the server has.
func matchIDFromTags(tags string) string {
	for _, tag := range strings.Split(tags, ",") {
		tag = strings.TrimSpace(tag)
		if id, ok := strings.CutPrefix(tag, "tfmm:"); ok {
			return id
		}
	}
	return ""
}

func (a *agent) subscribeLog(ctx context.Context) {
	c, err := a.dial()
	if err != nil {
		a.log.Warn("could not reach the game server over RCON yet", "err", err)
		return
	}
	defer c.Close()
	for _, cmd := range []string{
		"log on",
		fmt.Sprintf("logaddress_add %s", a.advertiseAddr()),
		"sv_logflush 0",
	} {
		if _, err := c.Exec(cmd); err != nil {
			a.log.Warn("rcon failed", "cmd", cmd, "err", err)
		}
	}
	_ = ctx
}

// advertiseAddr is what the server is told to log to. On the same host as the
// server that is the listen address; in a container next to it, it is not, so
// it can be overridden.
func (a *agent) advertiseAddr() string {
	if a.opt.logAdvertise != "" {
		return a.opt.logAdvertise
	}
	return a.opt.logListen
}

func (a *agent) dial() (*rcon.Conn, error) {
	return rcon.Dial(a.opt.rconAddr, a.opt.rconPassword, 10*time.Second)
}

// post sends one JSON body to the coordinator. Failures are logged and
// dropped: the coordinator's own timeouts are the backstop, and an agent that
// retried forever would report a match twice.
func (a *agent) post(ctx context.Context, path string, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		return
	}
	url := strings.TrimRight(a.opt.coordinator, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		a.log.Warn("coordinator unreachable", "path", path, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		a.log.Warn("coordinator refused", "path", path, "status", resp.StatusCode)
	}
}
