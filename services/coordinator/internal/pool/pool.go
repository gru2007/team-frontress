// Package pool is the coordinator's registry of dedicated game servers.
//
// Servers are infrastructure, not players. Nobody's client can become one: a
// machine joins the pool by running the Greyline agent with the pool key, and
// from then on the coordinator tells it which battle to host. Where the machine
// lives — a VPS, a box in a university basement, someone's spare desktop — the
// war does not care.
//
// The link is ordinary HTTP and it is always dialled by the agent, never by the
// coordinator: an agent registers, then long-polls for its next assignment.
// That is what lets a server behind NAT join the pool with no port forwarding
// beyond the game port players already need.
package pool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Status is where a server is in its cycle.
type Status string

const (
	// StatusIdle is registered, healthy and free to take a battle.
	StatusIdle Status = "idle"
	// StatusReserved has been handed an assignment and is expected to boot.
	StatusReserved Status = "reserved"
	// StatusBooting is changing level for its assignment.
	StatusBooting Status = "booting"
	// StatusLive is running a battle.
	StatusLive Status = "live"
	// StatusDraining will not take new battles; used to retire a machine.
	StatusDraining Status = "draining"
	// StatusOffline stopped heartbeating.
	StatusOffline Status = "offline"
)

var (
	ErrUnknownServer = errors.New("pool: unknown server")
	ErrBadToken      = errors.New("pool: bad server token")
	ErrNoServer      = errors.New("pool: no server available")
	ErrBusy          = errors.New("pool: server is not free")
)

// Kind is what sort of machine a pool member is.
type Kind string

const (
	// KindDedicated is the coordinator's own infrastructure: an operator ran
	// the agent with the pool key. Its word about a battle's result is taken
	// as it stands.
	KindDedicated Kind = "dedicated"
	// KindP2P is an ordinary player's own machine, elected to host one battle
	// because nothing dedicated was free. It is preferred less than a
	// dedicated server when both are idle, and its reported results are not
	// trusted on their own — see mm's result corroboration.
	KindP2P Kind = "p2p"
)

// Server is one machine in the pool.
type Server struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Region string   `json:"region"`
	Tags   []string `json:"tags,omitempty"`
	// ConnectAddress is what a player's "connect" command takes: the address the
	// game server is reachable at, as the operator declared it. The coordinator
	// only forwards it. A P2P host registers before it has one — its own engine
	// has not finished allocating a Steam FakeIP yet — and patches it in later
	// through SetConnectAddress.
	ConnectAddress string `json:"connect_address"`
	Capacity       int    `json:"capacity"`
	AgentVersion   string `json:"agent_version,omitempty"`
	GameVersion    string `json:"game_version,omitempty"`

	// Kind and Trusted are set only by the registration path itself, never by
	// anything in the request body a caller controls — see Register vs.
	// RegisterElectedHost. Trusted governs whether a reported result is
	// recorded immediately or held for roster corroboration.
	Kind    Kind `json:"kind"`
	Trusted bool `json:"trusted"`

	Status  Status `json:"status"`
	MatchID string `json:"match_id,omitempty"`
	Map     string `json:"map,omitempty"`
	Players int    `json:"players"`

	RegisteredAt time.Time `json:"registered_at"`
	LastSeen     time.Time `json:"last_seen"`
	// Hosted counts battles this machine has run, and Failed counts assignments
	// it did not get up. Together they are the only reputation a server has.
	Hosted int `json:"hosted"`
	Failed int `json:"failed"`

	token   string
	mailbox chan Command
}

// Free reports whether the server can take a new battle.
func (s *Server) Free() bool { return s.Status == StatusIdle }

// Public returns a copy without the token, safe to serve on the admin endpoint.
func (s *Server) Public() Server {
	c := *s
	c.token = ""
	c.mailbox = nil
	c.Tags = append([]string(nil), s.Tags...)
	return c
}

// CommandType is what the coordinator is asking a server to do.
type CommandType string

const (
	// CommandAssign hands the server a battle to host.
	CommandAssign CommandType = "assign"
	// CommandAbort tells it to stop hosting the named battle.
	CommandAbort CommandType = "abort"
	// CommandRoster adds players to a battle that is already running. A battle
	// is not sealed at the moment it forms: people who deploy while it is
	// being played join it rather than wait for the next one, and the server
	// has to be told who they are before they arrive.
	CommandRoster CommandType = "roster"
	// CommandIdle sends it back to its idle map.
	CommandIdle CommandType = "idle"
	// CommandShutdown asks the agent to leave the pool.
	CommandShutdown CommandType = "shutdown"
)

// Command is one instruction on the server's long-poll channel.
type Command struct {
	Type       CommandType `json:"type"`
	IssuedAt   time.Time   `json:"issued_at"`
	MatchID    string      `json:"match_id,omitempty"`
	Reason     string      `json:"reason,omitempty"`
	Assignment *Assignment `json:"assignment,omitempty"`
	// Roster carries the players CommandRoster is adding, and is empty for
	// every other command type. It is only ever the new arrivals, never the
	// whole roster: adding somebody who is already on it is harmless, but
	// re-sending everybody on every join is noise the server has to re-apply.
	Roster []RosterEntry `json:"roster,omitempty"`
}

// Assignment is the whole contract for one battle, in the form the agent needs
// to stand it up. It is deliberately self-contained: the agent never has to ask
// the coordinator a follow-up question to run the battle.
type Assignment struct {
	MatchID string `json:"match_id"`
	// Map and Mode are what to load. Mode is informational for the agent; the
	// map decides the rules.
	Map  string `json:"map"`
	Mode string `json:"mode"`
	// Password gates the server for the length of the battle, so only the roster
	// the coordinator picked can get in.
	Password   string        `json:"password"`
	MaxPlayers int           `json:"max_players"`
	Roster     []RosterEntry `json:"roster"`

	// Briefing is the war context this battle belongs to. The agent pushes it
	// into the game server so players are told, in game, which offensive they
	// are fighting in and why this is the map they got.
	Briefing Briefing `json:"briefing"`

	// MinPlayers is how many people must actually be on the server before the
	// battle starts. A game that begins while one person is still on the
	// loading screen is a game that person did not play, and on a small
	// population that is most of them — so the server holds in
	// waiting-for-players until the roster is there.
	MinPlayers int `json:"min_players"`
	// MusterTimeoutS is how long it may hold before starting anyway, so a
	// roster member who never arrives costs a wait rather than the battle.
	MusterTimeoutS int `json:"muster_timeout_s"`

	// BootDeadlineS is how long the agent has to report the server ready.
	BootDeadlineS int `json:"boot_deadline_s"`
	// ResultDeadlineS is how long the battle may run before the coordinator
	// gives up on it.
	ResultDeadlineS int `json:"result_deadline_s"`
}

// RosterEntry is one player's place in the battle.
type RosterEntry struct {
	SteamID uint64 `json:"steam_id"`
	Name    string `json:"name,omitempty"`
	// Side is the war allegiance: RED or BLU in the campaign.
	Side string `json:"side"`
	// Team is the in-game team to put the player on. It differs from Side on
	// directional maps, where the attacking side has to play as BLU because the
	// map is built that way.
	Team string `json:"team"`
	// Contract marks a mercenary fighting for the other side this battle.
	Contract bool `json:"contract,omitempty"`
}

// Briefing is the war context for one battle, in the form the game server
// announces to players. Node and stage are sent as localization tokens so the
// game can say it in the player's own language.
type Briefing struct {
	FrontID    string `json:"front_id"`
	FrontName  string `json:"front_name"`
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name"`
	Campaign   string `json:"campaign"`
	Attacker   string `json:"attacker"`
	Defender   string `json:"defender"`
	Stage      int    `json:"stage"`
	StageCount int    `json:"stage_count"`
	StageKind  string `json:"stage_kind"`
	// Reason is why this battle is being fought here, in one line.
	Reason string `json:"reason"`
	// Mobilized names a side under defensive mobilization, if any.
	Mobilized string `json:"mobilized,omitempty"`
}

// Registration is what an agent sends to join the pool.
type Registration struct {
	Name           string   `json:"name"`
	Region         string   `json:"region"`
	ConnectAddress string   `json:"connect_address"`
	Capacity       int      `json:"capacity"`
	Tags           []string `json:"tags,omitempty"`
	AgentVersion   string   `json:"agent_version,omitempty"`
	GameVersion    string   `json:"game_version,omitempty"`
	// ID lets an agent reclaim its identity after a restart instead of leaving a
	// ghost behind in the pool.
	ID string `json:"id,omitempty"`
}

// Pool is the registry. It is safe for concurrent use: HTTP handlers touch it
// from every request goroutine.
type Pool struct {
	mu      sync.Mutex
	servers map[string]*Server
	// offlineAfter is how long a silent dedicated server stays in the pool
	// before it is considered gone.
	offlineAfter time.Duration
	// offlineAfterP2P is the same, for a player-hosted server. It defaults much
	// shorter: a home connection dropping is far more likely, and far less
	// recoverable, than a VPS hiccup, and a stuck P2P "battle" wastes the whole
	// roster's time.
	offlineAfterP2P time.Duration
	// bootGrace replaces both of the above while a server is still getting its
	// map up. See silenceBudgetFor.
	bootGrace time.Duration
}

// New builds an empty pool.
func New(offlineAfter time.Duration) *Pool {
	if offlineAfter <= 0 {
		offlineAfter = 45 * time.Second
	}
	return &Pool{
		servers:         make(map[string]*Server),
		offlineAfter:    offlineAfter,
		offlineAfterP2P: 20 * time.Second,
	}
}

// SetP2POfflineAfter overrides how long a silent player-hosted server stays in
// the pool. Call it before the pool is serving traffic.
func (p *Pool) SetP2POfflineAfter(d time.Duration) {
	if d <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.offlineAfterP2P = d
}

// SetBootGrace overrides how long a server that has been handed a battle but
// has not reported it live yet may stay silent. Call it before the pool is
// serving traffic.
func (p *Pool) SetBootGrace(d time.Duration) {
	if d <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bootGrace = d
}

func (p *Pool) offlineAfterFor(k Kind) time.Duration {
	if k == KindP2P {
		return p.offlineAfterP2P
	}
	return p.offlineAfter
}

// silenceBudgetFor is how long this particular server may go without a
// heartbeat before it is presumed gone.
//
// A server that has been handed a battle and has not reported it live yet is
// loading a map, and a player-hosted one cannot heartbeat at all while it
// does: the heartbeats come from the menu page, which lives inside the very
// game process that is busy loading the level. Silence during boot therefore
// carries no information about whether the host is alive, and judging it by
// the same rule as a running server kills exactly the hosts whose machines
// are slowest — the ones the whole election just decided were worth waiting
// for. That phase is bounded by the battle's own boot deadline instead, which
// is what bootGrace is set from.
func (p *Pool) silenceBudgetFor(s *Server) time.Duration {
	base := p.offlineAfterFor(s.Kind)
	if s.Status == StatusReserved || s.Status == StatusBooting {
		if p.bootGrace > base {
			return p.bootGrace
		}
	}
	return base
}

// Register admits a dedicated server, or re-admits one that restarted. It
// returns the server's identity and the token it must present from then on.
// A dedicated server is the coordinator's own infrastructure: it is trusted,
// and its results are recorded without a player vote.
func (p *Pool) Register(reg Registration, now time.Time) (id, token string, err error) {
	if reg.ConnectAddress == "" {
		return "", "", errors.New("pool: registration has no connect_address; players would have nowhere to go")
	}
	if reg.Capacity <= 0 {
		reg.Capacity = 24
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	id = reg.ID
	if id == "" {
		id = "srv-" + randHex(6)
	}
	token = randHex(24)

	if existing, ok := p.servers[id]; ok {
		// A restarted agent takes its slot back. Anything it was hosting is gone
		// with the process, so the battle is not carried over.
		close(existing.mailbox)
	}
	p.servers[id] = &Server{
		ID:             id,
		Name:           firstNonEmpty(reg.Name, id),
		Region:         reg.Region,
		Tags:           reg.Tags,
		ConnectAddress: reg.ConnectAddress,
		Capacity:       reg.Capacity,
		AgentVersion:   reg.AgentVersion,
		GameVersion:    reg.GameVersion,
		Kind:           KindDedicated,
		Trusted:        true,
		Status:         StatusIdle,
		RegisteredAt:   now,
		LastSeen:       now,
		token:          token,
		// One slot is enough: the coordinator only ever has one outstanding
		// instruction per server, and a second would mean it lost track.
		mailbox: make(chan Command, 4),
	}
	return id, token, nil
}

// RegisterElectedHost admits a player's own machine as a pool member for
// exactly one battle, already reserved for matchID. Unlike Register, it never
// touches a caller-supplied trust flag: Kind and Trusted are hardcoded here,
// because the only door a random player's session token can walk through is
// this one, and it must never be able to claim to be dedicated infrastructure.
//
// ConnectAddress may be empty — the host's own engine has not finished
// allocating a Steam FakeIP yet — and is filled in later by SetConnectAddress.
// There is no restart-reclaims-its-slot behaviour here: a P2P registration is
// one battle's worth, not a standing identity.
func (p *Pool) RegisterElectedHost(reg Registration, matchID string, now time.Time) (id, token string, err error) {
	if matchID == "" {
		return "", "", errors.New("pool: an elected host registration needs a match id")
	}
	if reg.Capacity <= 0 {
		reg.Capacity = 24
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	id = "p2p-" + randHex(6)
	token = randHex(24)
	p.servers[id] = &Server{
		ID:             id,
		Name:           firstNonEmpty(reg.Name, id),
		Region:         reg.Region,
		ConnectAddress: reg.ConnectAddress,
		Capacity:       reg.Capacity,
		AgentVersion:   reg.AgentVersion,
		GameVersion:    reg.GameVersion,
		Kind:           KindP2P,
		Trusted:        false,
		Status:         StatusReserved,
		MatchID:        matchID,
		RegisteredAt:   now,
		LastSeen:       now,
		token:          token,
		mailbox:        make(chan Command, 4),
	}
	return id, token, nil
}

// SetConnectAddress patches in a server's address after registration — the
// one thing a P2P host does not know until its own engine has finished
// allocating a Steam FakeIP.
func (p *Pool) SetConnectAddress(id, token, addr string) error {
	if addr == "" {
		return errors.New("pool: refusing to set an empty connect address")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.servers[id]
	if !ok {
		return ErrUnknownServer
	}
	if s.token != token {
		return ErrBadToken
	}
	s.ConnectAddress = addr
	return nil
}

// Authenticate resolves a server by ID and token.
func (p *Pool) Authenticate(id, token string) (*Server, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.servers[id]
	if !ok {
		return nil, ErrUnknownServer
	}
	if s.token == "" || s.token != token {
		return nil, ErrBadToken
	}
	return s, nil
}

// Heartbeat records that a server is alive and what it is doing.
func (p *Pool) Heartbeat(id, token string, status Status, matchID, mapName string, players int, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.servers[id]
	if !ok {
		return ErrUnknownServer
	}
	if s.token != token {
		return ErrBadToken
	}
	s.LastSeen = now
	s.Players = players
	if mapName != "" {
		s.Map = mapName
	}
	// The coordinator owns reservation; the agent owns everything downstream of
	// it. An idle heartbeat from a reserved server is the agent not having
	// picked up its assignment yet, not a release.
	switch status {
	case StatusBooting, StatusLive, StatusDraining:
		s.Status = status
		s.MatchID = matchID
	case StatusIdle:
		if s.Status == StatusOffline {
			s.Status = StatusIdle
		}
	}
	return nil
}

// Poll blocks until the server has a command, the wait elapses, or ctx ends.
func (p *Pool) Poll(ctx context.Context, id, token string, wait time.Duration) (Command, bool, error) {
	p.mu.Lock()
	s, ok := p.servers[id]
	if !ok {
		p.mu.Unlock()
		return Command{}, false, ErrUnknownServer
	}
	if s.token != token {
		p.mu.Unlock()
		return Command{}, false, ErrBadToken
	}
	s.LastSeen = time.Now()
	mailbox := s.mailbox
	p.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case cmd, open := <-mailbox:
		if !open {
			return Command{}, false, ErrUnknownServer
		}
		return cmd, true, nil
	case <-timer.C:
		return Command{}, false, nil
	case <-ctx.Done():
		return Command{}, false, nil
	}
}

// Reserve takes a free server for a battle. Dedicated infrastructure is
// preferred over a player-hosted one whenever both are free — a P2P host is
// the fallback money is short enough to need, not the default — and within a
// kind, preferring the least recently used server spreads battles across the
// pool instead of hammering whichever machine registered first.
func (p *Pool) Reserve(matchID string, region string, now time.Time) (*Server, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var best *Server
	for _, s := range p.servers {
		if !s.Free() || now.Sub(s.LastSeen) > p.offlineAfterFor(s.Kind) {
			continue
		}
		if region != "" && s.Region != "" && s.Region != region {
			continue
		}
		if best == nil || preferred(s, best) {
			best = s
		}
	}
	if best == nil {
		return nil, ErrNoServer
	}
	best.Status = StatusReserved
	best.MatchID = matchID
	return best, nil
}

// preferred reports whether s should be picked over the current best.
func preferred(s, best *Server) bool {
	sDedicated, bestDedicated := s.Kind != KindP2P, best.Kind != KindP2P
	if sDedicated != bestDedicated {
		return sDedicated
	}
	if s.Hosted != best.Hosted {
		return s.Hosted < best.Hosted
	}
	return s.ID < best.ID
}

// Send queues a command for a server. A server whose mailbox is full is one
// that is not reading its link, so its assignment is refused rather than
// silently dropped.
func (p *Pool) Send(id string, cmd Command) error {
	p.mu.Lock()
	s, ok := p.servers[id]
	p.mu.Unlock()
	if !ok {
		return ErrUnknownServer
	}
	cmd.IssuedAt = time.Now()
	select {
	case s.mailbox <- cmd:
		return nil
	default:
		return fmt.Errorf("pool: server %s is not reading its command channel", id)
	}
}

// Release puts a server back in the idle set. hosted records whether the battle
// it was holding actually ran, which is the machine's only reputation.
func (p *Pool) Release(id string, hosted bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.servers[id]
	if !ok {
		return
	}
	if hosted {
		s.Hosted++
	} else {
		s.Failed++
	}
	s.MatchID = ""
	if s.Status != StatusOffline && s.Status != StatusDraining {
		s.Status = StatusIdle
	}
}

// Drain stops a server taking new battles without kicking anyone off it.
func (p *Pool) Drain(id string, drain bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.servers[id]
	if !ok {
		return ErrUnknownServer
	}
	if drain {
		s.Status = StatusDraining
		return nil
	}
	if s.Status == StatusDraining {
		s.Status = StatusIdle
	}
	return nil
}

// Deregister removes a server from the pool.
func (p *Pool) Deregister(id, token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.servers[id]
	if !ok {
		return ErrUnknownServer
	}
	if s.token != token {
		return ErrBadToken
	}
	close(s.mailbox)
	delete(p.servers, id)
	return nil
}

// ExpireSilent marks servers that stopped heartbeating and returns the match
// IDs that were running on them, so the coordinator can end those battles
// instead of waiting on a result that will never come.
func (p *Pool) ExpireSilent(now time.Time) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var lost []string
	for _, s := range p.servers {
		if s.Status == StatusOffline || now.Sub(s.LastSeen) <= p.silenceBudgetFor(s) {
			continue
		}
		s.Status = StatusOffline
		if s.MatchID != "" {
			lost = append(lost, s.MatchID)
			s.MatchID = ""
		}
	}
	sort.Strings(lost)
	return lost
}

// Get returns one server.
func (p *Pool) Get(id string) (*Server, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.servers[id]
	return s, ok
}

// List returns every server, ID order, with tokens stripped.
func (p *Pool) List() []Server {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Server, 0, len(p.servers))
	for _, s := range p.servers {
		out = append(out, s.Public())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Counts reports pool health for the status endpoint.
func (p *Pool) Counts() (total, free, live int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.servers {
		total++
		switch s.Status {
		case StatusIdle:
			free++
		case StatusLive, StatusBooting, StatusReserved:
			live++
		}
	}
	return total, free, live
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("pool: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// NewPassword generates a per-battle server password.
func NewPassword() string { return randHex(5) }
