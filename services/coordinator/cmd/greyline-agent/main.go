// Command greyline-agent joins one dedicated game server to a GREYLINE
// FRONTRESS coordinator's server pool.
//
//	greyline-agent -gc https://gc.example.org:27100 \
//	               -key $GREYLINE_POOL_KEY \
//	               -connect 203.0.113.10:27015 \
//	               -rcon 127.0.0.1:27015 -rcon-password ... \
//	               -log-listen 0.0.0.0:27500
//
// It drives a stock srcds over RCON and reads its log stream, so a machine
// joins the pool without a patched game binary: change level to the battle the
// coordinator assigned, lock it to that roster, and report who won.
//
// Everything is dialled outwards. The coordinator never connects to the game
// server, which is what lets a box behind NAT host battles as long as the game
// port itself is reachable.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/greyline-frontress/coordinator/internal/pool"
	"github.com/greyline-frontress/coordinator/internal/rcon"
	"github.com/greyline-frontress/coordinator/internal/srclog"
)

// agentVersion is reported to the coordinator, so an operator can tell which
// machines are running an old agent.
const agentVersion = "0.2.0"

// Config is everything the agent needs to know.
type Config struct {
	GC       string
	PoolKey  string
	Name     string
	Region   string
	Connect  string
	Capacity int

	RCONAddr     string
	RCONPassword string

	// LogListen is the UDP address the agent receives the server's log stream
	// on. LogAdvertise is what the game server is told to send to, which
	// differs when the two are on different machines or in containers.
	LogListen    string
	LogAdvertise string

	IdleMap   string
	StatePath string
}

// Agent is the whole program. One goroutine owns the state machine; the poll
// and log listeners only feed it.
type Agent struct {
	cfg  Config
	log  *slog.Logger
	http *http.Client

	id    string
	token string

	rmu  sync.Mutex
	conn *rcon.Conn

	commands chan pool.Command
	logs     chan srclog.Event

	battle *battle
}

func main() {
	cfg := Config{}
	flag.StringVar(&cfg.GC, "gc", envOr("GREYLINE_GC", "http://127.0.0.1:27100"), "coordinator base URL")
	flag.StringVar(&cfg.PoolKey, "key", os.Getenv("GREYLINE_POOL_KEY"), "server pool key")
	flag.StringVar(&cfg.Name, "name", envOr("GREYLINE_SERVER_NAME", hostname()), "name shown to operators")
	flag.StringVar(&cfg.Region, "region", os.Getenv("GREYLINE_REGION"), "region tag, used to keep players near their servers")
	flag.StringVar(&cfg.Connect, "connect", os.Getenv("GREYLINE_CONNECT"), "address players connect to (host:port)")
	flag.IntVar(&cfg.Capacity, "capacity", 24, "maximum players this server runs well with")
	flag.StringVar(&cfg.RCONAddr, "rcon", envOr("GREYLINE_RCON", "127.0.0.1:27015"), "srcds RCON address")
	flag.StringVar(&cfg.RCONPassword, "rcon-password", os.Getenv("GREYLINE_RCON_PASSWORD"), "srcds RCON password")
	flag.StringVar(&cfg.LogListen, "log-listen", envOr("GREYLINE_LOG_LISTEN", "127.0.0.1:27500"), "UDP address to receive the server log on")
	flag.StringVar(&cfg.LogAdvertise, "log-advertise", os.Getenv("GREYLINE_LOG_ADVERTISE"), "address the game server sends its log to (defaults to -log-listen)")
	flag.StringVar(&cfg.IdleMap, "idle-map", envOr("GREYLINE_IDLE_MAP", "ctf_2fort"), "map to sit on between battles")
	flag.StringVar(&cfg.StatePath, "state", envOr("GREYLINE_AGENT_STATE", "greyline-agent.json"), "where the agent remembers its server id")
	logLevel := flag.String("log-level", "info", "debug | info | warn | error")
	flag.Parse()

	log := newLogger(*logLevel)
	if cfg.PoolKey == "" {
		fail("no pool key: pass -key or set GREYLINE_POOL_KEY")
	}
	if cfg.Connect == "" {
		fail("no connect address: pass -connect host:port, the address players will connect to")
	}
	if cfg.RCONPassword == "" {
		fail("no RCON password: pass -rcon-password or set GREYLINE_RCON_PASSWORD")
	}
	if cfg.LogAdvertise == "" {
		cfg.LogAdvertise = cfg.LogListen
	}

	a := &Agent{
		cfg:      cfg,
		log:      log,
		http:     &http.Client{Timeout: 60 * time.Second},
		commands: make(chan pool.Command, 8),
		logs:     make(chan srclog.Event, 256),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := a.connectRCON(); err != nil {
		fail("could not reach the game server over RCON: " + err.Error())
	}
	if err := a.startLogListener(ctx); err != nil {
		fail(err.Error())
	}
	if err := a.attachLogStream(); err != nil {
		fail("could not attach to the game server's log stream: " + err.Error())
	}
	if err := a.register(ctx); err != nil {
		fail(err.Error())
	}
	log.Info("joined the server pool", "server_id", a.id, "gc", cfg.GC,
		"connect", cfg.Connect, "region", cfg.Region)

	go a.pollLoop(ctx)
	a.run(ctx)

	// Leaving cleanly means the coordinator can stop offering battles to this
	// machine immediately instead of waiting for it to time out.
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.post(shutdown, "/api/v1/servers/deregister", nil, nil); err != nil {
		log.Warn("could not leave the pool cleanly", "err", err)
	}
	log.Info("agent stopped")
}

// ---------------------------------------------------------------------------
// Coordinator link
// ---------------------------------------------------------------------------

type agentState struct {
	ServerID string `json:"server_id"`
}

func (a *Agent) register(ctx context.Context) error {
	reg := pool.Registration{
		Name:           a.cfg.Name,
		Region:         a.cfg.Region,
		ConnectAddress: a.cfg.Connect,
		Capacity:       a.cfg.Capacity,
		AgentVersion:   agentVersion,
		ID:             a.savedID(),
	}
	var resp struct {
		ServerID    string `json:"server_id"`
		ServerToken string `json:"server_token"`
		HeartbeatS  int    `json:"heartbeat_s"`
	}
	body, err := json.Marshal(reg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.cfg.GC+"/api/v1/servers/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.PoolKey)
	req.Header.Set("Content-Type", "application/json")

	httpResp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("register with %s: %w", a.cfg.GC, err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(httpResp.Body, 512))
		return fmt.Errorf("register: %s: %s", httpResp.Status, strings.TrimSpace(string(msg)))
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return fmt.Errorf("register: malformed reply: %w", err)
	}
	a.id, a.token = resp.ServerID, resp.ServerToken
	a.saveID(resp.ServerID)
	return nil
}

// savedID lets a restarted agent reclaim its identity rather than leaving a
// ghost server behind in the pool.
func (a *Agent) savedID() string {
	raw, err := os.ReadFile(a.cfg.StatePath)
	if err != nil {
		return ""
	}
	var st agentState
	if json.Unmarshal(raw, &st) != nil {
		return ""
	}
	return st.ServerID
}

func (a *Agent) saveID(id string) {
	raw, err := json.Marshal(agentState{ServerID: id})
	if err != nil {
		return
	}
	if err := os.WriteFile(a.cfg.StatePath, raw, 0o600); err != nil {
		a.log.Warn("could not remember the server id", "path", a.cfg.StatePath, "err", err)
	}
}

// post sends an authenticated request to the coordinator.
func (a *Agent) post(ctx context.Context, path string, body, into any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.cfg.GC+path+"?server_id="+a.id, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-Greyline-Server", a.id)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if into != nil {
		return json.NewDecoder(resp.Body).Decode(into)
	}
	return nil
}

// pollLoop keeps one long-poll open for the coordinator's next instruction.
func (a *Agent) pollLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		cmd, ok, err := a.pollOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("poll failed", "err", err, "retry_in", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			// A coordinator that restarted has forgotten this server, so the
			// agent has to join the pool again rather than poll into the void.
			if errors.Is(err, errNeedsRegistration) {
				if err := a.register(ctx); err == nil {
					a.log.Info("rejoined the server pool", "server_id", a.id)
					backoff = time.Second
				}
			}
			continue
		}
		backoff = time.Second
		if ok {
			select {
			case a.commands <- cmd:
			case <-ctx.Done():
				return
			}
		}
	}
}

var errNeedsRegistration = errors.New("agent: the coordinator does not know this server")

func (a *Agent) pollOnce(ctx context.Context) (pool.Command, bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/servers/poll?wait=25&server_id=%s", a.cfg.GC, a.id), nil)
	if err != nil {
		return pool.Command{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-Greyline-Server", a.id)

	resp, err := a.http.Do(req)
	if err != nil {
		return pool.Command{}, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return pool.Command{}, false, nil
	case http.StatusOK:
		var cmd pool.Command
		if err := json.NewDecoder(resp.Body).Decode(&cmd); err != nil {
			return pool.Command{}, false, err
		}
		return cmd, true, nil
	case http.StatusUnauthorized:
		return pool.Command{}, false, errNeedsRegistration
	default:
		return pool.Command{}, false, fmt.Errorf("poll: %s", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// Log stream
// ---------------------------------------------------------------------------

// startLogListener opens the UDP sink srcds sends its log to. Reading the log
// rather than polling status is what lets the agent know a round was won the
// moment it happens.
func (a *Agent) startLogListener(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", a.cfg.LogListen)
	if err != nil {
		return fmt.Errorf("log listener address %q: %w", a.cfg.LogListen, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("listen for the server log on %s: %w", a.cfg.LogListen, err)
	}
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go func() {
		buf := make([]byte, 8192)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() == nil {
					a.log.Error("log listener stopped", "err", err)
				}
				return
			}
			for _, line := range strings.Split(string(buf[:n]), "\n") {
				ev, ok := srclog.Parse(line)
				if !ok {
					continue
				}
				select {
				case a.logs <- ev:
				default:
					// A full channel means the state machine is wedged; dropping
					// noise is better than blocking the listener.
				}
			}
		}
	}()
	return nil
}

// ---------------------------------------------------------------------------
// RCON
// ---------------------------------------------------------------------------

func (a *Agent) connectRCON() error {
	conn, err := rcon.Dial(a.cfg.RCONAddr, a.cfg.RCONPassword, 5*time.Second)
	if err != nil {
		return err
	}
	a.rmu.Lock()
	if a.conn != nil {
		_ = a.conn.Close()
	}
	a.conn = conn
	a.rmu.Unlock()
	return nil
}

// attachLogStream points the game server's log at this agent. Everything the
// agent knows about a battle in progress comes down this stream, so a server
// that will not log to us is a server that cannot host: fail loudly at startup
// rather than silently never reporting a result.
func (a *Agent) attachLogStream() error {
	for _, cmd := range []string{
		"log on",
		"sv_logfile 1",
		"sv_logbans 0",
		"mp_logdetail 0",
		// Re-adding an address that is already there is harmless, which matters
		// because the agent may reconnect to a server that never restarted.
		fmt.Sprintf("logaddress_add %s", a.cfg.LogAdvertise),
	} {
		if _, err := a.exec("%s", cmd); err != nil {
			return fmt.Errorf("%s: %w", cmd, err)
		}
	}
	a.log.Info("listening to the game server log",
		"listen", a.cfg.LogListen, "advertised", a.cfg.LogAdvertise)
	return nil
}

// exec runs a console command, redialling once if the connection died. srcds
// restarts and long idles both drop RCON, and neither should cost a battle.
func (a *Agent) exec(format string, args ...any) (string, error) {
	a.rmu.Lock()
	conn := a.conn
	a.rmu.Unlock()
	if conn == nil {
		if err := a.connectRCON(); err != nil {
			return "", err
		}
		a.rmu.Lock()
		conn = a.conn
		a.rmu.Unlock()
	}
	out, err := conn.Execf(format, args...)
	if err == nil {
		return out, nil
	}
	if err := a.connectRCON(); err != nil {
		return "", err
	}
	a.rmu.Lock()
	conn = a.conn
	a.rmu.Unlock()
	return conn.Execf(format, args...)
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "greyline-node"
	}
	return h
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "greyline-agent:", msg)
	os.Exit(1)
}
