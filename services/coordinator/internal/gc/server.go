// Package gc is the GREYLINE FRONTRESS Game Coordinator.
//
// Concurrency model: one core goroutine owns every piece of mutable
// coordinator state (sessions, queue, matches). Socket goroutines only read and
// write frames and post events to the core. Nothing else takes a lock on match
// state, so the lifecycle logic can be read as ordinary sequential code.
package gc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/greyline-frontress/coordinator/internal/config"
	"github.com/greyline-frontress/coordinator/internal/hostelect"
	"github.com/greyline-frontress/coordinator/internal/security"
	"github.com/greyline-frontress/coordinator/internal/steam"
	"github.com/greyline-frontress/coordinator/internal/war"
	"github.com/greyline-frontress/coordinator/internal/wire"
	"github.com/greyline-frontress/coordinator/internal/wire/pb"
)

type eventKind int

const (
	evAccepted eventKind = iota
	evMessage
	evClosed
	evAuthResult
	evInspect
)

type event struct {
	kind eventKind
	sess *Session
	env  *pb.CMsgEnvelope

	// evAuthResult
	ident   *steam.Identity
	authErr error
	hello   *pb.CMsgHello
	req     *pb.CMsgEnvelope

	// evInspect: run fn on the core goroutine, for the admin endpoint.
	fn func(*Server)
}

// Server is the coordinator.
type Server struct {
	cfg    *config.Config
	log    *slog.Logger
	auth   steam.Authenticator
	world  war.Provider
	policy *security.Policy
	keys   *security.Keyring
	elect  *hostelect.Elector
	oracle *hostelect.PopOracle
	store  *PlayerStore

	events chan event

	// Owned by the core goroutine.
	sessions map[uint64]*Session   // steamID -> session
	pending  map[*Session]struct{} // accepted, not yet authenticated
	matches  map[string]*Match
	byPlayer map[uint64]string // steamID -> matchID

	nextSessionID atomic.Uint64
	nextJobID     atomic.Uint64

	wg sync.WaitGroup

	stats Stats
}

// Stats are counters exposed on the admin endpoint.
type Stats struct {
	Accepted      uint64
	AuthFailures  uint64
	MatchesFormed uint64
	MatchesLive   uint64
	Migrations    uint64
	Aborted       uint64
	Disputed      uint64
	Counted       uint64
}

// New builds a coordinator from its dependencies. Nothing starts until Run.
func New(cfg *config.Config, log *slog.Logger, auth steam.Authenticator, world war.Provider, policy *security.Policy, store *PlayerStore) *Server {
	oracle := hostelect.NewPopOracle()
	elector := hostelect.New(
		hostelect.Weights{
			Upload:         cfg.Election.WeightUpload,
			Latency:        cfg.Election.WeightLatency,
			CPU:            cfg.Election.WeightCPU,
			Stability:      cfg.Election.WeightStability,
			DedicatedBonus: cfg.Election.DedicatedBonus,
			PublicIPBonus:  cfg.Election.PublicIPBonus,
			AbandonPenalty: cfg.Election.AbandonPenalty,
		},
		hostelect.Requirements{
			MinUploadKbps:    cfg.Election.MinUploadKbps,
			MinCPUScore:      cfg.Election.MinCPUScore,
			MinMemoryMB:      cfg.Election.MinMemoryMB,
			MaxAcceptableRTT: cfg.Election.MaxAcceptableRTT,
			RequireCanHost:   cfg.Election.RequireCanHostBit,
		},
		oracle,
	)
	return &Server{
		cfg:      cfg,
		log:      log,
		auth:     auth,
		world:    world,
		policy:   policy,
		keys:     security.NewKeyring(cfg.Security.Secret),
		elect:    elector,
		oracle:   oracle,
		store:    store,
		events:   make(chan event, 1024),
		sessions: make(map[uint64]*Session),
		pending:  make(map[*Session]struct{}),
		matches:  make(map[string]*Match),
		byPlayer: make(map[uint64]string),
	}
}

// post hands an event to the core loop. Socket goroutines must never block
// forever here, so a full queue closes the offending session instead.
func (s *Server) post(e event) {
	select {
	case s.events <- e:
	default:
		s.log.Error("core event queue full, dropping session")
		if e.sess != nil {
			e.sess.Close()
		}
	}
}

// Inspect runs fn on the core goroutine and waits for it. Used by the admin
// HTTP handlers so they can read state without racing the core.
func (s *Server) Inspect(fn func(*Server)) {
	done := make(chan struct{})
	s.post(event{kind: evInspect, fn: func(srv *Server) {
		fn(srv)
		close(done)
	}})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// Run listens for clients and drives the core loop until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Listen, err)
	}
	s.log.Info("coordinator listening",
		"addr", ln.Addr().String(),
		"auth_mode", s.auth.Mode(),
		"policy_version", s.policy.Version,
		"policy_digest", hex.EncodeToString(s.policy.Digest())[:16])

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.acceptLoop(ctx, ln)
	}()

	ticker := time.NewTicker(s.cfg.Timing.TickInterval.D())
	defer ticker.Stop()
	saveTicker := time.NewTicker(15 * time.Second)
	defer saveTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = ln.Close()
			s.shutdown()
			s.wg.Wait()
			return s.persist()
		case <-ticker.C:
			s.onTick()
		case <-saveTicker.C:
			if err := s.persist(); err != nil {
				s.log.Error("persist failed", "err", err)
			}
		case e := <-s.events:
			s.handle(e)
		}
	}
}

func (s *Server) persist() error {
	return errors.Join(s.world.Save(), s.store.Save())
}

func (s *Server) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			s.log.Error("accept failed", "err", err)
			return
		}
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(30 * time.Second)
		}
		id := s.nextSessionID.Add(1)
		sess := newSession(id, wire.NewConn(c, 10*time.Second), s.log.With("session", id))
		s.stats.Accepted++
		s.post(event{kind: evAccepted, sess: sess})

		s.wg.Add(2)
		go func() { defer s.wg.Done(); sess.writeLoop() }()
		go func() { defer s.wg.Done(); sess.readLoop(s) }()
	}
}

func (s *Server) shutdown() {
	for _, sess := range s.sessions {
		sess.SendAndClose(&pb.CMsgEnvelope{
			Body: &pb.CMsgEnvelope_Goodbye{Goodbye: &pb.CMsgGoodbye{
				Reason:          proto.String("coordinator shutting down"),
				ReconnectAfterS: proto.Uint32(10),
			}},
		})
	}
	for sess := range s.pending {
		sess.Close()
	}
}

func (s *Server) handle(e event) {
	switch e.kind {
	case evAccepted:
		s.pending[e.sess] = struct{}{}
	case evMessage:
		s.onMessage(e.sess, e.env)
	case evClosed:
		s.onClosed(e.sess)
	case evAuthResult:
		s.onAuthResult(e.sess, e.hello, e.req, e.ident, e.authErr)
	case evInspect:
		e.fn(s)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (s *Server) newJobID() uint64 { return s.nextJobID.Add(1) }

// newMatchID produces an unguessable match id. It is guessable-resistant on
// purpose: the id is part of the join-token derivation, and a predictable id
// would let an outsider pre-compute tokens if the match secret ever leaked.
func newMatchID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is unrecoverable; a predictable id would weaken
		// join tokens, so refuse to produce one.
		panic("greyline: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// send pushes an unsolicited envelope to a SteamID. It reports whether that
// account currently has a coordinator link.
func (s *Server) send(steamID uint64, env *pb.CMsgEnvelope) bool {
	sess, ok := s.sessions[steamID]
	if !ok {
		return false
	}
	if env.JobId == nil {
		env.JobId = proto.Uint64(s.newJobID())
	}
	sess.Send(env)
	return true
}

// broadcastMatch sends a per-player envelope to every roster member with a live
// link. build may return nil to skip a slot.
func (s *Server) broadcastMatch(m *Match, build func(*RosterSlot) *pb.CMsgEnvelope) {
	for _, slot := range m.slots() {
		if env := build(slot); env != nil {
			s.send(slot.SteamID, env)
		}
	}
}

func (s *Server) matchOf(steamID uint64) *Match {
	id, ok := s.byPlayer[steamID]
	if !ok {
		return nil
	}
	return s.matches[id]
}
