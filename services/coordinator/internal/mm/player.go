package mm

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/greyline-frontress/coordinator/internal/hostelect"
	"github.com/greyline-frontress/coordinator/internal/war"
)

// PlayerState is where a client is in the deploy loop.
type PlayerState string

const (
	// StateIdle is on the war map, not looking for a battle.
	StateIdle PlayerState = "idle"
	// StateQueued pressed DEPLOY and is waiting for a battle to form.
	StateQueued PlayerState = "queued"
	// StateAssigned has a battle and is being sent to a server.
	StateAssigned PlayerState = "assigned"
	// StatePlaying is in a live battle.
	StatePlaying PlayerState = "playing"
)

// Player is one connected client. It is the coordinator's whole model of a
// person: who they are, which side they fight for, and what they are waiting
// for right now.
//
// The mutable half is guarded by the player's own mutex, not the matchmaker's,
// so a long-poll parked on wake never holds up matchmaking. Anything handed
// outside the package goes through Public as a PlayerView, which is the same
// data without the lock.
type Player struct {
	SteamID uint64
	Name    string
	Side    war.Side
	State   PlayerState

	FrontID  string
	MatchID  string
	QueuedAt time.Time
	LastSeen time.Time
	Region   string
	// PartyID groups players who want to land in the same battle together. It
	// is client-chosen (shared out of game by whoever is partying up) and only
	// ever compared for equality — the coordinator never allocates or tracks
	// party membership beyond "these queued players share a string".
	PartyID string

	// HostCaps is what this client reported about its ability to run a battle
	// itself, from hello. Only meaningful when electing a P2P host from a
	// formed roster; a dedicated-server battle never looks at it.
	HostCaps hostelect.Capabilities
	// AcceptContract means the player is willing to fight one battle for the
	// other side. Everyone here is a mercenary, so this is a preference, not a
	// betrayal — and it is what keeps a lopsided population playable.
	AcceptContract bool
	// Battles and Contracts are this account's small running record.
	Battles   int
	Contracts int

	token  string
	mu     sync.Mutex
	seq    uint64
	events []Event
	wake   chan struct{}
}

// PlayerView is a player as the API serves them.
type PlayerView struct {
	SteamID        uint64      `json:"steam_id"`
	Name           string      `json:"name"`
	Side           war.Side    `json:"side"`
	State          PlayerState `json:"state"`
	FrontID        string      `json:"front_id,omitempty"`
	MatchID        string      `json:"match_id,omitempty"`
	QueuedAt       time.Time   `json:"queued_at,omitzero"`
	LastSeen       time.Time   `json:"last_seen"`
	Region         string      `json:"region,omitempty"`
	AcceptContract bool        `json:"accept_contract"`
	Battles        int         `json:"battles"`
	Contracts      int         `json:"contracts"`
	PartyID        string      `json:"party_id,omitempty"`
}

// maxBuffered is how many undelivered events a client may fall behind by. A
// client that is not polling is either loading a map or gone; either way the
// newest events matter and the oldest do not.
const maxBuffered = 64

func newPlayer(steamID uint64, name string, now time.Time) *Player {
	return &Player{
		SteamID:  steamID,
		Name:     name,
		State:    StateIdle,
		LastSeen: now,
		token:    randHex(24),
		wake:     make(chan struct{}, 1),
	}
}

// push queues an event for the client's next poll and wakes any poll that is
// already waiting.
func (p *Player) push(ev Event) {
	p.mu.Lock()
	p.seq++
	ev.Seq = p.seq
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	p.events = append(p.events, ev)
	if len(p.events) > maxBuffered {
		p.events = p.events[len(p.events)-maxBuffered:]
	}
	p.mu.Unlock()

	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// drain returns events newer than since, without removing anything: a client
// that missed a reply retries with the same cursor and gets the same answer.
func (p *Player) drain(since uint64) []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Event
	for _, ev := range p.events {
		if ev.Seq > since {
			out = append(out, ev)
		}
	}
	return out
}

// Seq is the newest event sequence number this player has been given.
func (p *Player) Seq() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seq
}

// Token is the bearer token this client authenticates with.
func (p *Player) Token() string { return p.token }

// Public is the player without the token or the lock, safe to serialize.
func (p *Player) Public() PlayerView {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PlayerView{
		SteamID: p.SteamID, Name: p.Name, Side: p.Side, State: p.State,
		FrontID: p.FrontID, MatchID: p.MatchID, QueuedAt: p.QueuedAt,
		LastSeen: p.LastSeen, Region: p.Region, AcceptContract: p.AcceptContract,
		Battles: p.Battles, Contracts: p.Contracts, PartyID: p.PartyID,
	}
}

// EventType names what a client is being told.
type EventType string

const (
	// EventQueue is a change in the DEPLOY queue.
	EventQueue EventType = "queue"
	// EventMatchReady carries where to connect and what the battle is.
	EventMatchReady EventType = "match_ready"
	// EventMatchState is a battle's progress before it goes live.
	EventMatchState EventType = "match_state"
	// EventMatchOver is the result and what it did to the war.
	EventMatchOver EventType = "match_over"
	// EventWorld says the war moved, so the map screen should refetch.
	EventWorld EventType = "world"
	// EventContract offers a battle for the side that is short of mercenaries.
	EventContract EventType = "contract_offer"
	// EventHostOffer asks a client to run the battle itself, because nothing
	// dedicated was free. Accepting means calling client/host-register within
	// AcceptDeadlineS; doing nothing lets the coordinator fall back to waiting
	// for a server the ordinary way.
	EventHostOffer EventType = "host_offer"
	// EventResultPending asks non-host roster members to corroborate what an
	// untrusted P2P host reported. The values are the raw in-game scoreboard
	// values, not war-side values: ConfirmResult performs the same side/team
	// translation as ServerResult before it compares them.
	EventResultPending EventType = "result_pending"
	// EventNotice is a plain message.
	EventNotice EventType = "notice"
)

// Event is one entry in a client's poll stream.
type Event struct {
	Seq  uint64    `json:"seq"`
	At   time.Time `json:"at"`
	Type EventType `json:"type"`

	Queue    *QueueStatus         `json:"queue,omitempty"`
	Match    *MatchInfo           `json:"match,omitempty"`
	Over     *MatchOver           `json:"over,omitempty"`
	World    *WorldNotice         `json:"world,omitempty"`
	Contract *ContractOffer       `json:"contract,omitempty"`
	Host     *HostOffer           `json:"host,omitempty"`
	Pending  *ResultPendingNotice `json:"pending,omitempty"`
	Message  string               `json:"message,omitempty"`
}

// QueueStatus is what the DEPLOY screen shows while waiting.
type QueueStatus struct {
	Queued         bool     `json:"queued"`
	FrontID        string   `json:"front_id,omitempty"`
	FrontName      string   `json:"front_name,omitempty"`
	Side           war.Side `json:"side"`
	PlayersInQueue int      `json:"players_in_queue"`
	PlayersNeeded  int      `json:"players_needed"`
	Position       int      `json:"position"`
	WaitedS        int      `json:"waited_s"`
	Message        string   `json:"message"`
}

// MatchInfo is everything a client needs to join a battle and to understand
// which part of the war it is.
type MatchInfo struct {
	MatchID  string `json:"match_id"`
	State    string `json:"state"`
	Server   string `json:"server_id"`
	Connect  string `json:"connect,omitempty"`
	Password string `json:"password,omitempty"`

	Map  string `json:"map"`
	Mode string `json:"mode"`
	// Side is the player's war allegiance for this battle, Team the in-game
	// team they will be put on. They differ when a directional map forces the
	// attacking side to play as BLU.
	Side     war.Side `json:"side"`
	Team     war.Side `json:"team"`
	Contract bool     `json:"contract"`
	// IsHost means this client's own machine is running the battle — see the
	// coordinator README's "when nothing dedicated is free" section. A host
	// must call servers/result once the battle ends; everyone else on the
	// roster only ever calls client/confirm-result.
	IsHost bool `json:"is_host,omitempty"`
	// IsP2P means *some* roster member is running this battle on their own
	// machine — true for every slot in a P2P match, not just the host's own.
	// A client uses this to know whether it is worth watching for a result to
	// corroborate at all: a dedicated server's battle never needs a client-side
	// vote, since the dedicated agent reports the result on its own.
	IsP2P bool `json:"is_p2p,omitempty"`

	FrontID    string        `json:"front_id"`
	FrontName  string        `json:"front_name"`
	NodeID     string        `json:"node_id"`
	NodeName   string        `json:"node_name"`
	Attacker   war.Side      `json:"attacker"`
	Defender   war.Side      `json:"defender"`
	Stage      int           `json:"stage"`
	StageCount int           `json:"stage_count"`
	StageKind  war.StageKind `json:"stage_kind"`
	Headline   string        `json:"headline"`
	Reason     string        `json:"reason"`

	RedPlayers int `json:"red_players"`
	BluPlayers int `json:"blu_players"`
	DeadlineS  int `json:"deadline_s,omitempty"`
}

// MatchOver is the post-match screen: the result, and what it did to the war.
type MatchOver struct {
	MatchID  string      `json:"match_id"`
	Outcome  war.Outcome `json:"outcome"`
	Won      bool        `json:"won"`
	RedScore uint32      `json:"red_score"`
	BluScore uint32      `json:"blu_score"`
	Counted  bool        `json:"counted_towards_war"`
	Update   *war.Update `json:"war_update,omitempty"`
	Message  string      `json:"message"`
}

// WorldNotice tells a client the map changed under it.
type WorldNotice struct {
	Revision uint64 `json:"revision"`
	Headline string `json:"headline,omitempty"`
}

// ContractOffer asks a player to fight one battle for the side that needs
// bodies.
type ContractOffer struct {
	Side     war.Side `json:"side"`
	FrontID  string   `json:"front_id"`
	Message  string   `json:"message"`
	ExpiresS int      `json:"expires_s"`
}

// HostOffer asks a client to run this battle on its own machine, because
// nothing dedicated was free. The map and briefing are already decided —
// the only thing missing is where the battle physically runs — so a client
// that accepts can start loading the map the moment it gets back a server
// token from client/host-register.
type HostOffer struct {
	MatchID         string `json:"match_id"`
	Map             string `json:"map"`
	Mode            string `json:"mode"`
	MaxPlayers      int    `json:"max_players"`
	FrontName       string `json:"front_name"`
	AcceptDeadlineS int    `json:"accept_deadline_s"`
}

// ResultPendingNotice is what non-host players see after a P2P host reports a
// finished battle. It deliberately carries the scoreboard as the game saw it,
// so the player can compare it with the scoreboard before confirming it. A
// client must never auto-confirm this merely because the host said it.
type ResultPendingNotice struct {
	MatchID  string      `json:"match_id"`
	Outcome  war.Outcome `json:"outcome"`
	RedScore uint32      `json:"red_score"`
	BluScore uint32      `json:"blu_score"`
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("mm: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
