// Package pool finds a game server for a formed match and gives it back when
// the match is over.
//
// A provider is anything that can produce a server with an address and an RCON
// password: servers the operator runs ("static"), servers that register
// themselves ("registered"), and reservations bought from a serveme.tf fork
// ("serveme"). The matchmaker does not know which kind it got.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
)

// ErrNoServer means this provider has nothing free right now. It is an
// expected outcome, not a failure: the next provider gets a turn.
var ErrNoServer = errors.New("no server available")

// Server is a game server reserved for one match.
type Server struct {
	Name    string
	Connect string // ip:port, what the client types after "connect"
	RCON    string
	Region  string
	// STV is the SourceTV address, if this server runs one. Reported only.
	STV string

	// Provider is the kind that produced it, for logging and for Release.
	Provider string
	// Handle is provider-private (a serveme reservation id, a pool index).
	Handle string
	// Ephemeral servers are destroyed on release rather than returned to a
	// free list. A serveme reservation is ephemeral; the operator's own box is
	// not.
	Ephemeral bool
}

// Request describes what the match needs.
//
// Map, Password and ServerConfig are here for the providers that build a
// server rather than hand one out. A serveme reservation writes the config and
// downloads the first map before the game starts, so it has to be told all
// three up front; a static server is already running and ignores them, because
// RCONSetup sets the same things over RCON a moment later.
type Request struct {
	MatchID string
	Region  string
	Players int
	// Minutes the match is expected to last, for providers that reserve time.
	Minutes int
	// Map the match starts on.
	Map string
	// Password is the match password. A provider that can set it saves the
	// server a window between booting and being locked -- and saves us the
	// case where the provider invents its own and the two disagree.
	Password string
	// ServerConfig is the ruleset the match runs, e.g. "frontress_ranked".
	ServerConfig string
	// Mode is the match group's mode, "frontline" or "ranked". Providers pass
	// it on for logging and for forks that treat ranked servers differently.
	Mode string
}

// Provider is one source of servers.
type Provider interface {
	Kind() string
	Acquire(ctx context.Context, req Request) (*Server, error)
	Release(ctx context.Context, s *Server) error
}

// Pool tries its providers in order.
type Pool struct {
	mu        sync.Mutex
	providers []Provider
	inUse     map[string]*Server // connect -> server
}

// New builds a pool from config. reg, if non-nil, backs "registered"
// providers; it is shared with the HTTP API so servers can register themselves.
func New(cfg config.PoolConfig, reg *Registry) (*Pool, error) {
	p := &Pool{inUse: map[string]*Server{}}
	for i, pc := range cfg.Providers {
		switch pc.Kind {
		case "static":
			p.providers = append(p.providers, NewStatic(pc))
		case "registered":
			if reg == nil {
				return nil, fmt.Errorf("pool.providers[%d]: registered provider needs a registry", i)
			}
			p.providers = append(p.providers, reg)
		case "serveme":
			p.providers = append(p.providers, NewServeme(pc))
		default:
			return nil, fmt.Errorf("pool.providers[%d]: unknown kind %q", i, pc.Kind)
		}
	}
	return p, nil
}

// AddProvider appends a provider. Tests use it; New covers the config path.
func (p *Pool) AddProvider(pr Provider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.providers = append(p.providers, pr)
}

// Acquire returns a server for the match, or ErrNoServer if every provider is
// empty. Errors from individual providers are collected and only surface when
// nothing at all could be found — one broken provider must not stop the others.
func (p *Pool) Acquire(ctx context.Context, req Request) (*Server, error) {
	p.mu.Lock()
	providers := append([]Provider(nil), p.providers...)
	p.mu.Unlock()

	var errs []error
	for _, pr := range providers {
		s, err := pr.Acquire(ctx, req)
		if errors.Is(err, ErrNoServer) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", pr.Kind(), err))
			continue
		}
		if s == nil {
			continue
		}
		s.Provider = pr.Kind()
		p.mu.Lock()
		if p.inUse == nil {
			p.inUse = map[string]*Server{}
		}
		p.inUse[s.Connect] = s
		p.mu.Unlock()
		return s, nil
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w (%w)", ErrNoServer, errors.Join(errs...))
	}
	return nil, ErrNoServer
}

// Release gives a server back to whichever provider produced it.
func (p *Pool) Release(ctx context.Context, s *Server) error {
	if s == nil {
		return nil
	}
	p.mu.Lock()
	delete(p.inUse, s.Connect)
	providers := append([]Provider(nil), p.providers...)
	p.mu.Unlock()

	for _, pr := range providers {
		if pr.Kind() == s.Provider {
			return pr.Release(ctx, s)
		}
	}
	return fmt.Errorf("release %s: no provider named %q", s.Connect, s.Provider)
}

// InUse reports how many servers are running matches.
func (p *Pool) InUse() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.inUse)
}

// Free reports how many servers could be handed out right now. Providers that
// cannot answer without a network call (serveme) report zero, so this is a
// floor, not a total.
func (p *Pool) Free() int {
	p.mu.Lock()
	providers := append([]Provider(nil), p.providers...)
	p.mu.Unlock()

	n := 0
	for _, pr := range providers {
		if c, ok := pr.(interface{ FreeCount() int }); ok {
			n += c.FreeCount()
		}
	}
	return n
}

//
// static provider
//

// Static hands out servers the operator listed in the config.
type Static struct {
	mu      sync.Mutex
	servers []staticEntry
	region  string
}

type staticEntry struct {
	cfg  config.StaticServer
	busy bool
}

// NewStatic builds a static provider.
func NewStatic(pc config.ProviderConfig) *Static {
	s := &Static{region: pc.Region}
	for _, sv := range pc.Servers {
		s.servers = append(s.servers, staticEntry{cfg: sv})
	}
	return s
}

func (s *Static) Kind() string { return "static" }

func (s *Static) Acquire(_ context.Context, req Request) (*Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.servers {
		e := &s.servers[i]
		if e.busy {
			continue
		}
		if req.Region != "" && e.cfg.Region != "" && e.cfg.Region != req.Region {
			continue
		}
		e.busy = true
		region := e.cfg.Region
		if region == "" {
			region = s.region
		}
		return &Server{
			Name:    e.cfg.Name,
			Connect: e.cfg.Connect,
			RCON:    e.cfg.RCON,
			Region:  region,
			STV:     e.cfg.STV,
			Handle:  e.cfg.Connect,
		}, nil
	}
	return nil, ErrNoServer
}

func (s *Static) Release(_ context.Context, sv *Server) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.servers {
		if s.servers[i].cfg.Connect == sv.Handle {
			s.servers[i].busy = false
			return nil
		}
	}
	return fmt.Errorf("static: %s is not one of ours", sv.Connect)
}

// FreeCount reports idle servers.
func (s *Static) FreeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.servers {
		if !e.busy {
			n++
		}
	}
	return n
}

//
// registered provider
//

// Registry holds servers that registered themselves over HTTP and keep
// heartbeating. A server that stops heartbeating leaves the pool; if it was
// running a match, the matchmaker finds out through the match's own timeout.
type Registry struct {
	mu      sync.Mutex
	servers map[string]*registered
	ttl     time.Duration
	now     func() time.Time
}

type registered struct {
	srv      Server
	lastSeen time.Time
	busy     bool
}

// NewRegistry builds a registry. ttl is how long a server survives without a
// heartbeat.
func NewRegistry(ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &Registry{servers: map[string]*registered{}, ttl: ttl, now: time.Now}
}

func (r *Registry) Kind() string { return "registered" }

// Register adds or refreshes a server.
func (r *Registry) Register(s Server) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.servers[s.Connect]; ok {
		// Keep the busy flag: a server re-registering mid-match is still
		// running that match.
		e.srv = s
		e.lastSeen = r.now()
		return
	}
	r.servers[s.Connect] = &registered{srv: s, lastSeen: r.now()}
}

// Heartbeat refreshes a server's liveness. Reports whether it is known.
func (r *Registry) Heartbeat(connect string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.servers[connect]
	if !ok {
		return false
	}
	e.lastSeen = r.now()
	return true
}

// Prune drops servers that stopped heartbeating.
func (r *Registry) Prune() {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := r.now().Add(-r.ttl)
	for k, e := range r.servers {
		if e.lastSeen.Before(cutoff) && !e.busy {
			delete(r.servers, k)
		}
	}
}

func (r *Registry) Acquire(_ context.Context, req Request) (*Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := r.now().Add(-r.ttl)
	for _, e := range r.servers {
		if e.busy || e.lastSeen.Before(cutoff) {
			continue
		}
		if req.Region != "" && e.srv.Region != "" && e.srv.Region != req.Region {
			continue
		}
		e.busy = true
		s := e.srv
		s.Handle = s.Connect
		return &s, nil
	}
	return nil, ErrNoServer
}

func (r *Registry) Release(_ context.Context, sv *Server) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.servers[sv.Handle]
	if !ok {
		return nil // it went away while we were using it; nothing to give back
	}
	e.busy = false
	return nil
}

// FreeCount reports live, idle registered servers.
func (r *Registry) FreeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := r.now().Add(-r.ttl)
	n := 0
	for _, e := range r.servers {
		if !e.busy && !e.lastSeen.Before(cutoff) {
			n++
		}
	}
	return n
}
