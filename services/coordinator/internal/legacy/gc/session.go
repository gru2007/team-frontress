package gc

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/greyline-frontress/coordinator/internal/legacy/hostelect"
	"github.com/greyline-frontress/coordinator/internal/wire"
	"github.com/greyline-frontress/coordinator/internal/wire/pb"
)

// sendQueueDepth bounds how far a slow client may fall behind before it is
// dropped. A client that cannot keep up with this many coordinator messages is
// not going to survive a match either.
const sendQueueDepth = 64

// Session is one live coordinator link. Every session has a reader goroutine
// and a writer goroutine; all state transitions happen on the core loop, which
// owns the fields below after handshake.
type Session struct {
	id     uint64
	conn   *wire.Conn
	log    *slog.Logger
	out    chan *pb.CMsgEnvelope
	closed atomic.Bool

	closeOnce sync.Once
	done      chan struct{}
	// closeReq asks the writer to drain the queue and then hang up, so a
	// rejection reason actually reaches the client instead of being lost to an
	// immediate socket close.
	closeReq chan struct{}

	// --- owned by the core loop after the hello is accepted ---
	steamID       uint64
	verified      bool
	clientVersion string
	platform      string
	isDedicated   bool
	pingLocation  string
	caps          hostelect.Capabilities

	lastSeen  time.Time
	matchID   string
	queuedAt  time.Time
	inQueue   bool
	queueSide int32
	frontID   string
	party     []uint64
}

func newSession(id uint64, c *wire.Conn, log *slog.Logger) *Session {
	return &Session{
		id:       id,
		conn:     c,
		log:      log,
		out:      make(chan *pb.CMsgEnvelope, sendQueueDepth),
		done:     make(chan struct{}),
		closeReq: make(chan struct{}, 1),
		lastSeen: time.Now(),
	}
}

// SteamID is the authenticated account on this link, or 0 before the handshake.
func (s *Session) SteamID() uint64 { return s.steamID }

// Send queues an envelope. It never blocks: if the client is too far behind,
// the session is closed instead, which is the only outcome that keeps the core
// loop from stalling on one bad peer.
func (s *Session) Send(env *pb.CMsgEnvelope) {
	if s.closed.Load() {
		return
	}
	select {
	case s.out <- env:
	default:
		s.log.Warn("send queue full, dropping session",
			"session", s.id, "steam_id", s.steamID)
		s.Close()
	}
}

// Reply sends env as the answer to req, carrying the correlation id back.
func (s *Session) Reply(req *pb.CMsgEnvelope, env *pb.CMsgEnvelope) {
	if req.GetJobId() != 0 {
		env.JobIdTarget = proto.Uint64(req.GetJobId())
	}
	s.Send(env)
}

// SendAndClose queues a final message and hangs up once it has gone out. Use
// this for rejections: Close on its own would drop the queued frame.
func (s *Session) SendAndClose(env *pb.CMsgEnvelope) {
	s.Send(env)
	select {
	case s.closeReq <- struct{}{}:
	default:
	}
}

// Close tears the link down once. Safe from any goroutine.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.done)
		_ = s.conn.Close()
	})
}

// writeLoop drains the send queue onto the socket.
func (s *Session) writeLoop() {
	for {
		select {
		case <-s.done:
			return
		case env := <-s.out:
			if err := s.conn.Write(env); err != nil {
				s.log.Debug("session write failed", "session", s.id, "err", err)
				s.Close()
				return
			}
		case <-s.closeReq:
			s.drainAndClose()
			return
		}
	}
}

// drainAndClose flushes whatever is already queued, then hangs up.
func (s *Session) drainAndClose() {
	for {
		select {
		case env := <-s.out:
			if err := s.conn.Write(env); err != nil {
				s.Close()
				return
			}
		default:
			s.Close()
			return
		}
	}
}

// readLoop feeds inbound envelopes to the core loop until the link dies.
func (s *Session) readLoop(srv *Server) {
	defer func() {
		s.Close()
		srv.post(event{kind: evClosed, sess: s})
	}()

	timeout := srv.cfg.Timing.SessionTimeout.D()
	for {
		env, err := s.conn.Read(time.Now().Add(timeout))
		if err != nil {
			s.log.Debug("session read ended", "session", s.id, "steam_id", s.steamID, "err", err)
			return
		}
		select {
		case <-s.done:
			return
		default:
		}
		srv.post(event{kind: evMessage, sess: s, env: env})
	}
}
