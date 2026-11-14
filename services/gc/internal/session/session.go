// Package session is the transport state between the GC and one game process:
// a player's client or a dedicated server.
//
// Delivery is at-least-once with deduplication, which is exactly-once as far as
// the game can tell:
//
//   - every outbound message gets the next sequence number and stays queued
//     until the peer acknowledges it on its next poll, so a response lost on
//     the network is simply sent again;
//   - every inbound message carries the peer's own sequence number, and
//     anything at or below the last one processed is dropped, so a request the
//     peer retried is not acted on twice.
//
// A session that stops polling expires. That is how the GC learns a player
// went offline or a server died, and it is the only way it learns it.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// Kind is what is on the other end.
type Kind int

const (
	Client Kind = iota + 1
	Server
)

func (k Kind) String() string {
	switch k {
	case Client:
		return "client"
	case Server:
		return "server"
	}
	return "unknown"
}

// MaxOutbox is how many unacknowledged messages a session may hold. A peer
// that falls this far behind is not keeping up; it is dropped and must start a
// new session, which resends everything it needs from scratch.
const MaxOutbox = 4096

// ErrClosed is returned for a session that has ended.
var ErrClosed = errors.New("session closed")

// ErrSequence leaves the batch unacknowledged so its missing frames can be retried.
var ErrSequence = errors.New("gc batch sequence gap")

// Session is one authenticated peer.
type Session struct {
	ID      string
	Kind    Kind
	SteamID uint64
	AppID   uint32
	Created time.Time

	// Server sessions only.
	ServerName  string  // which key let it in, for logs
	KeyID       string  // config.KeyID of that key: revoking or rotating it ends the session
	Connect     string  // the address players use
	MatchID     *uint64 // nil: legacy peer; zero: fresh server process
	Region      string
	CommunityID string // which community operates this server
	// AntiCheatAuthority: this server's anti-cheat bans are applied
	// (config.ServerKey.AntiCheatAuthority).
	AntiCheatAuthority bool

	mu       sync.Mutex
	outbox   []wire.Frame
	nextOut  uint64
	lastIn   uint64
	lastSeen time.Time
	wake     chan struct{}
	closed   bool
}

func newSession(kind Kind, steamID uint64, now time.Time) *Session {
	return &Session{
		ID:       randomID(),
		Kind:     kind,
		SteamID:  steamID,
		Created:  now,
		nextOut:  1,
		lastSeen: now,
		wake:     make(chan struct{}, 1),
	}
}

// Push queues one message for the peer. It reports false if the session is
// closed or has overflowed (and is closed by this call).
func (s *Session) Push(msgType uint32, data []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if len(s.outbox) >= MaxOutbox {
		s.closeLocked()
		return false
	}
	s.outbox = append(s.outbox, wire.Frame{Seq: s.nextOut, Type: msgType, Data: data})
	s.nextOut++
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return true
}

// Accept filters an inbound batch down to the frames not seen before, in
// order, and records them as seen.
func (s *Session) Accept(frames []wire.Frame, now time.Time) ([]wire.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	s.lastSeen = now
	out := frames[:0:0]
	last := s.lastIn
	for _, f := range frames {
		if f.Seq <= last {
			continue
		}
		if f.Seq != last+1 {
			return nil, ErrSequence
		}
		last = f.Seq
		out = append(out, f)
	}
	s.lastIn = last
	return out, nil
}

// ack drops everything the peer has confirmed.
func (s *Session) ackLocked(upTo uint64) {
	i := 0
	for i < len(s.outbox) && s.outbox[i].Seq <= upTo {
		i++
	}
	if i > 0 {
		s.outbox = append(s.outbox[:0:0], s.outbox[i:]...)
	}
}

// Poll acknowledges upTo and returns what is still pending, waiting up to
// maxWait for something to arrive if nothing is. The returned frames remain
// queued until a later Poll acknowledges them.
func (s *Session) Poll(upTo uint64, maxWait time.Duration, now func() time.Time) ([]wire.Frame, error) {
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, ErrClosed
		}
		s.lastSeen = now()
		s.ackLocked(upTo)
		if len(s.outbox) > 0 {
			out := append([]wire.Frame(nil), s.outbox...)
			s.mu.Unlock()
			return out, nil
		}
		wake := s.wake
		s.mu.Unlock()

		select {
		case <-wake:
		case <-deadline.C:
			return nil, nil
		}
	}
}

// LastSeen is when the peer last talked to us.
func (s *Session) LastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeen
}

// Closed reports whether the session has ended.
func (s *Session) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *Session) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	s.outbox = nil
	// Wake a waiting poll so it returns ErrClosed now rather than at its
	// deadline.
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Manager owns every live session.
type Manager struct {
	mu      sync.Mutex
	byID    map[string]*Session
	bySteam map[uint64]*Session
	ttl     time.Duration
	now     func() time.Time
	// onEnd is told about every session that ends, for whatever reason, after
	// it has been removed. It must not call back into the Manager.
	onEnd func(*Session)
}

// NewManager makes a manager whose sessions expire after ttl without a poll.
func NewManager(ttl time.Duration, onEnd func(*Session)) *Manager {
	return &Manager{
		byID:    map[string]*Session{},
		bySteam: map[uint64]*Session{},
		ttl:     ttl,
		now:     time.Now,
		onEnd:   onEnd,
	}
}

// UseClock replaces the clock. Tests only.
func (m *Manager) UseClock(now func() time.Time) { m.now = now }

// Now is the manager's clock.
func (m *Manager) Now() time.Time { return m.now() }

// Open starts a session for a SteamID. One SteamID has at most one session: a
// second login (a restarted game, a reconnect after a network change) replaces
// the first, and the first is ended.
func (m *Manager) Open(kind Kind, steamID uint64, init func(*Session)) *Session {
	s := newSession(kind, steamID, m.now())
	if init != nil {
		init(s)
	}
	m.mu.Lock()
	old := m.bySteam[steamID]
	if old != nil {
		delete(m.byID, old.ID)
	}
	m.byID[s.ID] = s
	m.bySteam[steamID] = s
	m.mu.Unlock()

	if old != nil {
		old.close()
		if m.onEnd != nil {
			m.onEnd(old)
		}
	}
	return s
}

// Get finds a live session by its token.
func (m *Manager) Get(id string) *Session {
	m.mu.Lock()
	s := m.byID[id]
	m.mu.Unlock()
	if s == nil || s.Closed() {
		return nil
	}
	return s
}

// BySteamID finds the live session of a SteamID, or nil.
func (m *Manager) BySteamID(id uint64) *Session {
	m.mu.Lock()
	s := m.bySteam[id]
	m.mu.Unlock()
	if s == nil || s.Closed() {
		return nil
	}
	return s
}

// End closes a session now.
func (m *Manager) End(s *Session) {
	m.mu.Lock()
	_, live := m.byID[s.ID]
	if live {
		delete(m.byID, s.ID)
		if m.bySteam[s.SteamID] == s {
			delete(m.bySteam, s.SteamID)
		}
	}
	m.mu.Unlock()
	s.close()
	if live && m.onEnd != nil {
		m.onEnd(s)
	}
}

// Sweep ends every session that has not polled within the TTL, and every
// session that closed itself by overflowing.
func (m *Manager) Sweep() {
	now := m.now()
	var dead []*Session
	m.mu.Lock()
	for id, s := range m.byID {
		if s.Closed() || now.Sub(s.LastSeen()) > m.ttl {
			dead = append(dead, s)
			delete(m.byID, id)
			if m.bySteam[s.SteamID] == s {
				delete(m.bySteam, s.SteamID)
			}
		}
	}
	m.mu.Unlock()
	for _, s := range dead {
		s.close()
		if m.onEnd != nil {
			m.onEnd(s)
		}
	}
}

// Count is how many sessions of a kind are live.
func (m *Manager) Count(kind Kind) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.byID {
		if s.Kind == kind {
			n++
		}
	}
	return n
}

func randomID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
