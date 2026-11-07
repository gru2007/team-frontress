// Package gc is the game coordinator: the authority for parties, queues and
// matches that Valve's GC was for the stock game.
//
// It speaks the game's own protocol. The game's stock code -- the party
// panel, the matchmaking dashboard, the "match found" dialog on the client;
// CTFGCServerSystem and CMatchInfo on the server -- does all of its usual work,
// because the objects and messages it receives are the ones it was written
// for. Nothing in the game emulates a GC any more.
//
// Concurrency is one rule: all GC state belongs to one goroutine, the loop in
// Run. Transport handlers decode a message and hand it to the loop; slow work
// (a webhook) runs elsewhere and posts its result back into
// the loop. No lock protects GC state, because nothing but the loop touches it.
package gc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"log/slog"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/clans"
	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/history"
	"github.com/gru2007/team-frontress/services/gc/internal/players"
	"github.com/gru2007/team-frontress/services/gc/internal/reports"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/sink"
	"github.com/gru2007/team-frontress/services/gc/internal/socache"
	"github.com/gru2007/team-frontress/services/gc/internal/state"
	"github.com/gru2007/team-frontress/services/gc/internal/support"
	"github.com/gru2007/team-frontress/services/gc/internal/war"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// GC is the coordinator.
type GC struct {
	cfg      config.Config
	log      *slog.Logger
	sessions *session.Manager
	so       *socache.Store
	players  *players.Store
	war      *war.Engine
	store    *state.Store
	history  *history.Store
	reports  *reports.Store
	hooks    *sink.Webhooks
	clans    *clans.Store
	book     *support.Book
	steam    support.Steam
	commerce *support.Admin
	now      func() time.Time

	loop chan func()

	// Everything below belongs to the loop.
	online            map[uint64]*Player
	parties           map[uint64]*Party
	lobbies           map[uint64]*Lobby
	servers           map[uint64]*Server
	recentMaps        map[string]time.Time
	dirty             bool // lobbies changed since the last save
	matchmakerStats   *gcpb.CMsgGCMatchMakerStatsResponse
	matchmakerStatsAt time.Time

	// Social (social.go).
	chat         map[string][]ChatLine
	chatSeq      uint64
	presenceAt   time.Time
	presenceLast string
	// presenceCounts is the last presence without the list, for Status.
	presenceCounts Presence

	status  atomic.Pointer[Status]
	warView atomic.Pointer[WarView]
}

// Deps are the GC's collaborators. Only Config is required.
type Deps struct {
	Config  config.Config
	Log     *slog.Logger
	Players *players.Store
	War     *war.Engine
	Store   *state.Store
	History *history.Store
	Reports *reports.Store
	Hooks   *sink.Webhooks
	// Clans; nil keeps them in memory.
	Clans *clans.Store
	// Support is the record of donations; Steam takes them. Both are needed
	// for the support tab (config support.enabled).
	Support  *support.Book
	Steam    support.Steam
	Commerce *support.Admin
	Now      func() time.Time
}

// New makes a GC and restores the matches that were running when it last
// stopped.
func New(d Deps) (*GC, error) {
	g := &GC{
		cfg:        d.Config,
		log:        d.Log,
		players:    d.Players,
		war:        d.War,
		store:      d.Store,
		history:    d.History,
		reports:    d.Reports,
		hooks:      d.Hooks,
		clans:      d.Clans,
		book:       d.Support,
		steam:      d.Steam,
		commerce:   d.Commerce,
		chat:       map[string][]ChatLine{},
		now:        d.Now,
		loop:       make(chan func(), 4096),
		online:     map[uint64]*Player{},
		parties:    map[uint64]*Party{},
		lobbies:    map[uint64]*Lobby{},
		servers:    map[uint64]*Server{},
		recentMaps: map[string]time.Time{},
	}
	if g.log == nil {
		g.log = slog.Default()
	}
	if g.now == nil {
		g.now = time.Now
	}
	if g.players == nil {
		g.players, _ = players.Open("")
	}
	if g.store == nil {
		g.store = state.Memory()
	}
	if g.history == nil {
		g.history, _ = history.Open("")
	}
	if g.reports == nil {
		g.reports, _ = reports.Open("")
	}
	if g.hooks == nil {
		g.hooks = sink.New(nil, g.log)
	}
	if g.clans == nil {
		g.clans, _ = clans.Open("")
	}
	if g.book == nil {
		g.book, _ = support.OpenBook("")
	}
	if g.steam == nil && d.Config.Support.Enabled && d.Config.Support.Fake {
		g.steam = &support.Fake{}
	}
	g.so = socache.New(g.sendSO)
	g.sessions = session.NewManager(d.Config.Timing.SessionTTL(), func(s *session.Session) {
		// Sessions can end from inside the loop (a replaced login), so this
		// must not block on the loop's channel.
		go g.Do(func() { g.onSessionEnd(s) })
	})
	g.sessions.UseClock(g.now)

	saved, err := g.store.Load()
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	for _, rec := range saved {
		if archived, ok := g.history.Get(lobbyKey(rec.MatchID)); ok && archived.State == "ended" {
			continue
		}
		g.restoreLobby(rec)
		active[lobbyKey(rec.MatchID)] = true
	}
	if err := g.history.Reconcile(active, g.now()); err != nil {
		return nil, err
	}
	g.publishStatus()
	return g, nil
}

// Sessions is the transport's view of the GC.
func (g *GC) Sessions() *session.Manager { return g.sessions }

// Config is the GC's configuration.
func (g *GC) Config() config.Config { return g.cfg }

// Clans is the clan store.
func (g *GC) Clans() *clans.Store { return g.clans }

// Players is the player record store.
func (g *GC) Players() *players.Store { return g.players }

// Do runs f on the loop. It blocks only when the loop is far behind, which is
// the backpressure that keeps a flood of messages from growing memory forever.
func (g *GC) Do(f func()) { g.loop <- f }

// Call runs f on the loop and waits for it.
func (g *GC) Call(ctx context.Context, f func()) error {
	done := make(chan struct{})
	select {
	case g.loop <- func() { f(); close(done) }:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run is the loop. It returns when ctx ends.
func (g *GC) Run(ctx context.Context) {
	tick := time.NewTicker(g.cfg.Timing.Tick())
	defer tick.Stop()
	sweep := time.NewTicker(5 * time.Second)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			g.save()
			return
		case f := <-g.loop:
			g.safely(f)
		case <-tick.C:
			g.safely(g.tick)
		case <-sweep.C:
			// Sweep calls onEnd, which posts back into the loop; run it off
			// the loop so a full channel cannot deadlock it.
			go g.sessions.Sweep()
		}
	}
}

// safely runs one unit of loop work. A bug in one handler must not take the
// whole coordinator, and every player's match with it, down.
func (g *GC) safely(f func()) {
	defer func() {
		if r := recover(); r != nil {
			g.log.Error("gc: recovered from panic", "panic", r)
		}
	}()
	f()
}

func (g *GC) tick() {
	g.tickParties()
	g.tickLobbies()
	g.matchmake()
	g.tickPresence()
	g.save()
	g.publishStatus()
}

func (g *GC) save() {
	if !g.dirty {
		return
	}
	recs := make([]state.Lobby, 0, len(g.lobbies))
	for _, l := range g.lobbies {
		recs = append(recs, l.record())
	}
	if err := g.store.Save(recs); err != nil {
		g.log.Error("gc: could not save matches", "err", err)
		return
	}
	g.dirty = false
}

// --- sending ---------------------------------------------------------------

// send delivers a message to whoever is connected as steamID.
func (g *GC) send(steamID uint64, msgType uint32, body proto.Message) {
	s := g.sessions.BySteamID(steamID)
	if s == nil {
		return
	}
	g.sendTo(s, msgType, body, wire.NoJob)
}

func (g *GC) sendTo(s *session.Session, msgType uint32, body proto.Message, replyTo uint64) {
	data, err := wire.Encode(msgType, body, replyTo)
	if err != nil {
		g.log.Error("gc: encode", "type", msgType, "err", err)
		return
	}
	s.Push(msgType|wire.ProtoFlag, data)
}

// reply answers a request. The reply only reaches the job that is waiting for
// it if it carries the request's job id back.
func (g *GC) reply(s *session.Session, req *wire.Msg, msgType uint32, body proto.Message) {
	g.sendTo(s, msgType, body, req.SourceJob())
}

func (g *GC) sendSO(owner uint64, msgType uint32, body proto.Message) {
	g.send(owner, msgType, body)
}

// --- dispatch --------------------------------------------------------------

type handler func(g *GC, s *session.Session, m *wire.Msg)

var (
	clientHandlers = map[uint32]handler{}
	serverHandlers = map[uint32]handler{}
)

func onClient(t any, h handler) { clientHandlers[msgID(t)] = h }
func onServer(t any, h handler) { serverHandlers[msgID(t)] = h }

func msgID(t any) uint32 {
	switch v := t.(type) {
	case gcpb.ETFGCMsg:
		return uint32(v)
	case gcpb.ESOMsg:
		return uint32(v)
	case gcpb.EGCBaseClientMsg:
		return uint32(v)
	}
	panic("gc: not a message id")
}

// Handle processes one decoded message from a peer. Transport calls it on the
// loop, in the order the peer sent them.
func (g *GC) Handle(s *session.Session, raw []byte) {
	m, err := wire.Decode(raw)
	if err != nil {
		// The stock client still sends a few of Valve's old struct-framed
		// messages (the MOTD request, 1012) that nothing here answers.
		typ := uint32(0)
		if len(raw) >= 4 {
			typ = binary.LittleEndian.Uint32(raw[:4]) &^ wire.ProtoFlag
		}
		g.log.Debug("gc: ignoring a message that is not protobuf-framed", "peer", s.SteamID, "type", typ, "err", err)
		return
	}
	if s.Closed() {
		return
	}
	table := clientHandlers
	if s.Kind == session.Server {
		table = serverHandlers
	}
	h, ok := table[m.Type]
	if !ok {
		g.log.Debug("gc: unhandled message", "kind", s.Kind, "type", m.Type)
		return
	}
	h(g, s, m)
}

// parse unmarshals a message body, logging and reporting false if it does not
// parse.
func (g *GC) parse(m *wire.Msg, into proto.Message) bool {
	if err := proto.Unmarshal(m.Body, into); err != nil {
		g.log.Warn("gc: bad message body", "type", m.Type, "err", err)
		return false
	}
	return true
}

// newID is a random non-zero 63-bit id. Random rather than sequential so a
// restarted GC can never hand out an id it already used.
func newID() uint64 {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		if id := binary.LittleEndian.Uint64(b[:]) >> 1; id != 0 {
			return id
		}
	}
}
