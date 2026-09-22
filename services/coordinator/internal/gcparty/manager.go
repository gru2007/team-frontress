// Package gcparty is the party/lobby state manager that sits between the
// GC-protocol HTTP transport (internal/api's gc.go) and the existing
// matchmaker (internal/mm).
//
// There is no party or lobby concept in internal/mm: a party is purely a
// GC-layer construct in the real game, built and torn down entirely
// client-side and mirrored to the GC only so the GC can matchmake it. This
// package is that mirror. It owns:
//
//   - party membership, leadership, invites and join requests
//   - translating "queue for match" into the flat wire.QueueRequest-shaped
//     mm.Ticket the existing Matchmaker already knows how to serve
//   - the one place a "lobby" (CSOTFGameServerLobby) is synthesized, by
//     watching a party's ticket and turning an mm assignment into the
//     lobby's State=RUN / Connect fields -- the real protocol's only
//     match-found delivery mechanism
//   - a per-player outbound push queue, drained by the long-poll HTTP
//     handler, carrying the SO cache updates that keep all of the above in
//     sync on the client
package gcparty

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// Matchmaker is the slice of mm.Matchmaker this package needs. Narrowing it
// keeps gcparty testable with a fake, the same way internal/api does.
type Matchmaker interface {
	Enqueue(*mm.Ticket) (*mm.Ticket, error)
	Cancel(id string) error
	Status(id string) (wire.QueueStatus, error)
}

// Push is one queued outbound GC message for one player: an EMsg and an
// already-marshaled protobuf body. The HTTP transport layer (gc.go) wraps
// each one in ProtoBufMsgHeader_t framing and batches them; this package
// does not know about that framing, only about what needs to be said.
type Push struct {
	EMsg EMsg
	Body []byte
}

// EMsg is re-exported so callers of Drain do not need a second import for
// just this one type.
type EMsg = gcproto.EMsg

// partyState is the server's view of one party. member 0 is not
// distinguished as leader by position -- LeaderID is authoritative and
// members can be reordered by kicks/leaves without disturbing it.
type partyState struct {
	id      uint64
	leader  uint64
	members []uint64
	names   map[uint64]string
	// pending mirrors CSOTFParty.pending_members: invites this party has
	// sent, and join requests it has received, that have not resolved yet.
	pending []*gcproto.PendingPartyMember
	options *gcproto.PartyOptions

	// ticket is this party's current matchmaking ticket, or nil when not
	// queued and not in standby for one.
	ticket     *mm.Ticket
	matchGroup int32
	queuedAt   time.Time

	// lobbyID is assigned the first time this party's ticket is assigned a
	// match, and kept for the ticket's lifetime so repeated pushes update
	// the same SO rather than creating a new one every tick.
	lobbyID uint64
	// lastTicketState is compared each tick to notice state transitions,
	// since mm.Matchmaker.Status has no "changed since last time" signal.
	lastTicketState wire.QueueState

	version uint64
}

// inviteRecord is one CSOTFPartyInvite the target player can currently see:
// either an invite this party sent them, or a join request they sent this
// party, in flight.
type inviteRecord struct {
	partyID uint64
	inviter uint64
	target  uint64
	typ     int32 // gcproto.InvitePending or gcproto.InvitePendingJoin
}

// Manager owns every party, every in-flight invite, and the outbound push
// queues that carry state to clients.
type Manager struct {
	mmk Matchmaker
	log *slog.Logger
	now func() time.Time

	mu          sync.Mutex
	nextPartyID uint64
	nextLobbyID uint64
	parties     map[uint64]*partyState
	partyOf     map[uint64]uint64          // steamID -> partyID
	invitesFor  map[uint64][]*inviteRecord // target steamID -> invites they can see
	// lobbyInvitesFor mirrors invitesFor but for CTFLobbyInviteProto
	// (SOTypeTFLobbyInvite): target steamID -> lobby IDs they have a
	// pending "join my active match" invite for. There is no separate
	// decline EMsg in the real protocol for these -- they clear only when
	// accepted (this one or, per AcceptLobbyInvite's
	// abandoning_invite_lobby_ids field, another one instead) or when the
	// lobby itself ends.
	lobbyInvitesFor map[uint64][]uint64
	// serverLobbies holds the last CSOTFGameServerLobby pushed to a
	// dedicated server's own GC session (keyed by that server's SteamID),
	// so a server session that reconnects mid-match gets it replayed in its
	// OnConnect snapshot instead of only in a push it may have missed.
	serverLobbies map[uint64]*gcproto.GameServerLobby

	outbox map[uint64][]Push
	signal map[uint64]chan struct{}
}

// New builds a party manager backed by mmk.
func New(mmk Matchmaker, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		mmk:             mmk,
		log:             log,
		now:             time.Now,
		nextPartyID:     1,
		nextLobbyID:     1,
		parties:         map[uint64]*partyState{},
		partyOf:         map[uint64]uint64{},
		invitesFor:      map[uint64][]*inviteRecord{},
		lobbyInvitesFor: map[uint64][]uint64{},
		serverLobbies:   map[uint64]*gcproto.GameServerLobby{},
		outbox:          map[uint64][]Push{},
		signal:          map[uint64]chan struct{}{},
	}
}

// Run drives the background tick that watches every active ticket for a
// state change (match formed, cancelled, expired, kicked) and pushes the
// result to the party. Call it once from main in its own goroutine.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.tick()
		}
	}
}

// steamIDStr converts a SteamID64 to the decimal wire.SteamID the rest of
// the coordinator uses.
func steamIDStr(id uint64) wire.SteamID { return wire.SteamID(strconv.FormatUint(id, 10)) }

// ---------------------------------------------------------------------------
// Session lifecycle
// ---------------------------------------------------------------------------

// OnConnect ensures steamID has a party (creating a solo one if this is
// their first time being seen) and records their display name. It returns
// the initial shared-object cache snapshot to push on this session's
// handshake: every object this player currently owns, per
// CMsgSOCacheSubscribed's contract.
func (m *Manager) OnConnect(steamID uint64, name string) []Push {
	m.mu.Lock()
	defer m.mu.Unlock()

	p := m.ensurePartyLocked(steamID, name)
	subscribed := &gcproto.SOCacheSubscribed{
		Owner: gcproto.U64(steamID),
		Objects: []*gcproto.SOCacheSubscribedType{
			gcproto.NewSubscribedType(gcproto.SOTypeTFParty, m.buildPartySOLocked(p).Marshal()),
		},
		Version: gcproto.U64(p.version),
	}
	if invites := m.invitesFor[steamID]; len(invites) > 0 {
		var data [][]byte
		for _, inv := range invites {
			data = append(data, m.buildInviteSO(inv).Marshal())
		}
		subscribed.Objects = append(subscribed.Objects, gcproto.NewSubscribedType(gcproto.SOTypeTFPartyInvite, data...))
	}
	if p.ticket != nil && p.lobbyID != 0 {
		if lobby := m.buildLobbySOLocked(p); lobby != nil {
			subscribed.Objects = append(subscribed.Objects, gcproto.NewSubscribedType(gcproto.SOTypeTFGameServerLobby, lobby.Marshal()))
		}
	}
	// A dedicated server reconnecting mid-match (its own long-poll session
	// dropped and came back) gets its current match lobby replayed here,
	// same as a client party would -- otherwise a push it missed while
	// disconnected is simply gone, and CTFGCServerSystem never sees a roster.
	if lobby, ok := m.serverLobbies[steamID]; ok {
		subscribed.Objects = append(subscribed.Objects, gcproto.NewSubscribedType(gcproto.SOTypeTFGameServerLobby, lobby.Marshal()))
	}
	return []Push{
		{EMsg: gcproto.EMsgSOCacheSubscribed, Body: subscribed.Marshal()},
		{EMsg: gcproto.EMsgSOCacheSubscribedUpToDate, Body: (&gcproto.SOCacheSubscribedUpToDate{Version: gcproto.U64(p.version)}).Marshal()},
	}
}

func (m *Manager) ensurePartyLocked(steamID uint64, name string) *partyState {
	if pid, ok := m.partyOf[steamID]; ok {
		p := m.parties[pid]
		if name != "" {
			p.names[steamID] = name
		}
		return p
	}
	pid := m.nextPartyID
	m.nextPartyID++
	p := &partyState{
		id:      pid,
		leader:  steamID,
		members: []uint64{steamID},
		names:   map[uint64]string{steamID: name},
	}
	m.parties[pid] = p
	m.partyOf[steamID] = pid
	return p
}

// partyOfLocked resolves the caller's party, or nil if somehow unknown (it
// should not be, once OnConnect has run once for them).
func (m *Manager) partyOfLocked(steamID uint64) *partyState {
	pid, ok := m.partyOf[steamID]
	if !ok {
		return nil
	}
	return m.parties[pid]
}

// ---------------------------------------------------------------------------
// Party SO construction
// ---------------------------------------------------------------------------

func (m *Manager) buildPartySOLocked(p *partyState) *gcproto.Party {
	out := &gcproto.Party{
		PartyID:  gcproto.U64(p.id),
		LeaderID: gcproto.U64(p.leader),
	}
	for _, id := range p.members {
		out.MemberIDs = append(out.MemberIDs, id)
		out.Members = append(out.Members, &gcproto.PartyMember{SteamID: gcproto.U64(id)})
	}
	out.PendingMembers = p.pending
	out.GroupCriteria = nil
	if p.options != nil {
		out.GroupCriteria = p.options.GroupCriteria
	}
	if p.ticket != nil {
		out.MatchmakingQueues = []*gcproto.PartyQueueEntry{{
			MatchGroup: gcproto.I32(p.matchGroup),
			QueuedTime: gcproto.U32(uint32(p.queuedAt.Unix())),
		}}
	}
	if p.lobbyID != 0 {
		out.AssociatedLobbyID = gcproto.U64(p.lobbyID)
		out.AssociatedLobbyMatchGrp = gcproto.I32(p.matchGroup)
	}
	return out
}

func (m *Manager) buildInviteSO(inv *inviteRecord) *gcproto.PartyInvite {
	return &gcproto.PartyInvite{
		GroupID: gcproto.U64(inv.partyID),
		Inviter: gcproto.U64(inv.inviter),
		Members: []*gcproto.PartyInviteMember{{SteamID: gcproto.U64(inv.target)}},
		Type:    gcproto.I32(inv.typ),
	}
}

// buildLobbySOLocked builds the current CSOTFGameServerLobby for p's ticket,
// or nil if the ticket has no assignment yet (a lobby SO with no connect
// info is not worth pushing -- the party update alone already told the
// client it is queued).
func (m *Manager) buildLobbySOLocked(p *partyState) *gcproto.GameServerLobby {
	if p.ticket == nil || p.lobbyID == 0 {
		return nil
	}
	status, err := m.mmk.Status(p.ticket.ID)
	if err != nil || status.Assignment == nil {
		return nil
	}
	a := status.Assignment
	lobby := &gcproto.GameServerLobby{
		LobbyID:    gcproto.U64(p.lobbyID),
		State:      gcproto.I32(int32(LobbyRun)),
		Connect:    gcproto.Str(a.Connect),
		MapName:    gcproto.Str(a.Map),
		MatchID:    strToUint64Ptr(a.MatchID),
		MatchGroup: gcproto.U32(uint32(a.MatchGroup)),
	}
	for _, rp := range a.Roster {
		id, _ := strconv.ParseUint(string(rp.SteamID), 10, 64)
		lobby.Members = append(lobby.Members, &gcproto.LobbyPlayer{
			ID:   gcproto.U64(id),
			Name: gcproto.Str(rp.Name),
			Team: gcproto.I32(int32(rp.Team)),
		})
	}
	return lobby
}

// LobbyRun mirrors gcproto.LobbyRun without importing the const under a
// different name -- kept local for readability at call sites above.
const LobbyRun = gcproto.LobbyRun

// PushMatchRoster implements mm.GCServerPusher: it gives a dedicated
// server's own GC session the CSOTFGameServerLobby for the match it was just
// assigned or had a seat added to, addressed by the server's own SteamID
// instead of a party's. CTFGCServerSystem -- real, untouched Valve code on
// the server -- is what turns this shared object into a CMatchInfo and the
// tf_mm_strict roster gate; there is no bespoke RCON command and no
// server-side plugin involved anywhere in this path.
func (m *Manager) PushMatchRoster(serverSteamID uint64, spec mm.ServerRosterSpec) {
	lobby := &gcproto.GameServerLobby{
		// The real lobby_id is a Steam concept this coordinator does not
		// have; hashing the opaque match id gives the server the same
		// stable identifier the client-side copy of this lobby carries in
		// its MatchID field (see strToUint64Ptr below), so both sides at
		// least agree on which match this object is.
		LobbyID:    strToUint64Ptr(spec.MatchID),
		State:      gcproto.I32(int32(LobbyRun)),
		Connect:    gcproto.Str(spec.Connect),
		MapName:    gcproto.Str(spec.Map),
		MatchID:    strToUint64Ptr(spec.MatchID),
		MatchGroup: gcproto.U32(uint32(spec.MatchGroup)),
		ServerID:   gcproto.U64(serverSteamID),
	}
	for _, rp := range spec.Roster {
		id, _ := strconv.ParseUint(string(rp.SteamID), 10, 64)
		lobby.Members = append(lobby.Members, &gcproto.LobbyPlayer{
			ID:   gcproto.U64(id),
			Name: gcproto.Str(rp.Name),
			Team: gcproto.I32(int32(rp.Team)),
		})
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.serverLobbies[serverSteamID] = lobby
	m.enqueuePushLocked(serverSteamID, gcproto.EMsgSOUpdate, (&gcproto.SOSingleObject{
		Owner:      gcproto.U64(serverSteamID),
		TypeID:     gcproto.I32(gcproto.SOTypeTFGameServerLobby),
		ObjectData: lobby.Marshal(),
	}).Marshal())
}

// ClearMatchRoster implements mm.GCServerPusher: it tears down the lobby
// object for a server being returned to the pool, mirroring
// destroyLobbyInviteLocked's SODestroy pattern above. A server with no
// tracked lobby (GC disabled for it, or never matched) is a silent no-op.
func (m *Manager) ClearMatchRoster(serverSteamID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.serverLobbies[serverSteamID]; !ok {
		return
	}
	delete(m.serverLobbies, serverSteamID)
	m.enqueuePushLocked(serverSteamID, gcproto.EMsgSODestroy, (&gcproto.SOSingleObject{
		Owner:  gcproto.U64(serverSteamID),
		TypeID: gcproto.I32(gcproto.SOTypeTFGameServerLobby),
	}).Marshal())
}

func strToUint64Ptr(s string) *uint64 {
	// Match IDs in this coordinator are opaque hex strings (mm.randomID),
	// not numeric -- the real CSOTFGameServerLobby.match_id is a Steam
	// concept this coordinator does not have. Hashing it into a stable
	// uint64 gives the field a value that is at least consistent for one
	// match's lifetime, rather than sending a fabricated small integer.
	var h uint64 = 1469598103934665603 // FNV-1a offset basis
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return &h
}

func (m *Manager) bumpAndPushParty(p *partyState) {
	p.version++
	so := m.buildPartySOLocked(p)
	body := so.Marshal()
	for _, id := range p.members {
		m.enqueuePushLocked(id, gcproto.EMsgSOUpdate, (&gcproto.SOSingleObject{
			Owner:      gcproto.U64(id),
			TypeID:     gcproto.I32(gcproto.SOTypeTFParty),
			ObjectData: body,
			Version:    gcproto.U64(p.version),
		}).Marshal())
	}
}

func (m *Manager) enqueuePushLocked(steamID uint64, emsg EMsg, body []byte) {
	m.outbox[steamID] = append(m.outbox[steamID], Push{EMsg: emsg, Body: body})
	if ch, ok := m.signal[steamID]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Drain blocks until steamID has a queued push, ctx is cancelled, or timeout
// elapses, then returns and clears whatever is queued (possibly nothing).
// This is what the long-poll HTTP handler calls once it has processed the
// inbound batch.
func (m *Manager) Drain(ctx context.Context, steamID uint64, timeout time.Duration) []Push {
	m.mu.Lock()
	if len(m.outbox[steamID]) > 0 {
		out := m.outbox[steamID]
		m.outbox[steamID] = nil
		m.mu.Unlock()
		return out
	}
	ch, ok := m.signal[steamID]
	if !ok {
		ch = make(chan struct{}, 1)
		m.signal[steamID] = ch
	}
	m.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
	case <-ctx.Done():
	}

	m.mu.Lock()
	out := m.outbox[steamID]
	m.outbox[steamID] = nil
	m.mu.Unlock()
	return out
}

// ---------------------------------------------------------------------------
// Party control message handlers, one per EMsgGCParty_*/EMsgGC* this
// coordinator implements. Each takes the caller's already-authenticated
// SteamID (established once per HTTP session by the transport layer, never
// trusted from message bodies) plus the decoded request.
// ---------------------------------------------------------------------------

func (m *Manager) SetOptions(steamID uint64, req *gcproto.PartySetOptions) (*gcproto.PartySetOptionsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.partyOfLocked(steamID)
	if p == nil {
		return nil, errNoParty
	}
	if req.Options != nil {
		if p.options == nil || (req.Options.OverwriteExisting != nil && *req.Options.OverwriteExisting) {
			p.options = req.Options
		} else {
			mergePartyOptions(p.options, req.Options)
		}
	}
	m.bumpAndPushParty(p)
	return &gcproto.PartySetOptionsResponse{}, nil
}

func mergePartyOptions(dst, src *gcproto.PartyOptions) {
	if src.GroupCriteria != nil {
		dst.GroupCriteria = src.GroupCriteria
	}
	if src.PlayerCriteria != nil {
		dst.PlayerCriteria = src.PlayerCriteria
	}
	if src.PlayerUIState != nil {
		dst.PlayerUIState = src.PlayerUIState
	}
}

func (m *Manager) QueueForMatch(steamID uint64, req *gcproto.PartyQueueForMatch) (*gcproto.PartyQueueForMatchResponse, error) {
	m.mu.Lock()
	p := m.partyOfLocked(steamID)
	if p == nil {
		m.mu.Unlock()
		return nil, errNoParty
	}
	if p.leader != steamID {
		m.mu.Unlock()
		return nil, errNotLeader
	}
	group := int32(gcproto.MatchGroupInvalid)
	if req.MatchGroup != nil {
		group = *req.MatchGroup
	}
	if req.FinalOptions != nil {
		if p.options == nil {
			p.options = req.FinalOptions
		} else {
			mergePartyOptions(p.options, req.FinalOptions)
		}
	}
	m.mu.Unlock()

	if err := m.enqueueTicketForParty(p, group); err != nil {
		return nil, err
	}
	return &gcproto.PartyQueueForMatchResponse{}, nil
}

// enqueueTicketForParty builds a fresh mm.Ticket from p's current roster and
// enqueues it under matchGroup, updating p's queue bookkeeping on success.
// This is the one place a party's roster turns into a Matchmaker ticket, so
// both the client-initiated QueueForMatch above and the server-initiated
// NewMatchForLobby rematch path below go through it and stay consistent.
func (m *Manager) enqueueTicketForParty(p *partyState, group int32) error {
	m.mu.Lock()
	players := make([]wire.AssignedPlayer, 0, len(p.members))
	for _, id := range p.members {
		players = append(players, wire.AssignedPlayer{SteamID: steamIDStr(id), Name: p.names[id]})
	}
	ticket := &mm.Ticket{
		MatchGroup: wire.MatchGroup(group),
		Leader:     steamIDStr(p.leader),
		Players:    players,
	}
	m.mu.Unlock()

	got, err := m.mmk.Enqueue(ticket)
	if err != nil {
		return err
	}

	m.mu.Lock()
	p.ticket = got
	p.matchGroup = group
	p.queuedAt = m.now()
	p.lastTicketState = wire.QueueStateSearching
	m.bumpAndPushParty(p)
	m.mu.Unlock()
	return nil
}

func (m *Manager) RemoveFromQueue(steamID uint64, req *gcproto.PartyRemoveFromQueue) (*gcproto.PartyRemoveFromQueueResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.partyOfLocked(steamID)
	if p == nil {
		return nil, errNoParty
	}
	m.clearTicketLocked(p)
	m.bumpAndPushParty(p)
	return &gcproto.PartyRemoveFromQueueResponse{}, nil
}

func (m *Manager) clearTicketLocked(p *partyState) {
	if p.ticket == nil {
		return
	}
	_ = m.mmk.Cancel(p.ticket.ID)
	p.ticket = nil
	p.lobbyID = 0
	p.lastTicketState = ""
}

func (m *Manager) ExitMatchmaking(steamID uint64, req *gcproto.ExitMatchmaking) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.partyOfLocked(steamID)
	if p == nil {
		return errNoParty
	}
	m.clearTicketLocked(p)
	m.bumpAndPushParty(p)
	return nil
}

func (m *Manager) InvitePlayer(steamID uint64, req *gcproto.PartyInvitePlayer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.partyOfLocked(steamID)
	if p == nil || req.PlayerID == nil {
		return errNoParty
	}
	target := *req.PlayerID
	// Mutual consent: if target already asked to join this exact party, the
	// leader inviting them back is an acceptance, not a second pending
	// entry -- merge immediately, the same as the real client's UI treats
	// a mutual invite/join-request as an instant join.
	if m.hasPendingLocked(p, target, gcproto.PendingRequestedToJoin) {
		m.mergeIntoPartyLocked(target, p)
		return nil
	}
	p.pending = append(p.pending, &gcproto.PendingPartyMember{
		SteamID: gcproto.U64(target),
		Type:    gcproto.I32(int32(gcproto.PendingInvited)),
		Inviter: gcproto.U64(steamID),
	})
	rec := &inviteRecord{partyID: p.id, inviter: steamID, target: target, typ: int32(gcproto.InvitePending)}
	m.invitesFor[target] = append(m.invitesFor[target], rec)
	m.enqueuePushLocked(target, gcproto.EMsgSOCreate, (&gcproto.SOSingleObject{
		Owner:      gcproto.U64(target),
		TypeID:     gcproto.I32(gcproto.SOTypeTFPartyInvite),
		ObjectData: m.buildInviteSO(rec).Marshal(),
	}).Marshal())
	// If the inviter's party is currently in a live match, also push a
	// CTFLobbyInviteProto (SOTypeTFLobbyInvite) so the invitee's client can
	// offer "join this match" directly, not just "join this party" -- the
	// real protocol's own mechanism for letting a friend land on the same
	// server. There is no separate client request that creates this SO;
	// the real client relies on it riding along with an ordinary party
	// invite whenever the inviting party already has a match running.
	if p.lobbyID != 0 && p.ticket != nil {
		m.pushLobbyInviteLocked(target, p.lobbyID, p.matchGroup)
	}
	m.bumpAndPushParty(p)
	return nil
}

// pushLobbyInviteLocked sends target a CTFLobbyInviteProto for lobbyID and
// records it so AcceptLobbyInvite (or a later lobby teardown) can clean the
// SO back up.
func (m *Manager) pushLobbyInviteLocked(target, lobbyID uint64, matchGroup int32) {
	m.lobbyInvitesFor[target] = append(m.lobbyInvitesFor[target], lobbyID)
	m.enqueuePushLocked(target, gcproto.EMsgSOCreate, (&gcproto.SOSingleObject{
		Owner:      gcproto.U64(target),
		TypeID:     gcproto.I32(gcproto.SOTypeTFLobbyInvite),
		ObjectData: (&gcproto.LobbyInvite{LobbyID: gcproto.U64(lobbyID), MatchGroup: gcproto.I32(matchGroup)}).Marshal(),
	}).Marshal())
}

// destroyLobbyInviteLocked clears one tracked lobby invite (if any) and
// pushes the matching SODestroy so the client stops offering to join it.
func (m *Manager) destroyLobbyInviteLocked(target, lobbyID uint64) {
	ids := m.lobbyInvitesFor[target]
	out := ids[:0]
	found := false
	for _, id := range ids {
		if id == lobbyID {
			found = true
			continue
		}
		out = append(out, id)
	}
	m.lobbyInvitesFor[target] = out
	if !found {
		return
	}
	m.enqueuePushLocked(target, gcproto.EMsgSODestroy, (&gcproto.SOSingleObject{
		Owner:  gcproto.U64(target),
		TypeID: gcproto.I32(gcproto.SOTypeTFLobbyInvite),
	}).Marshal())
}

// hasPendingLocked reports whether p's pending list already carries an
// entry for steamID of the given type.
func (m *Manager) hasPendingLocked(p *partyState, steamID uint64, typ gcproto.PendingPartyMemberType) bool {
	for _, pm := range p.pending {
		if pm.SteamID != nil && *pm.SteamID == steamID && pm.Type != nil && gcproto.PendingPartyMemberType(*pm.Type) == typ {
			return true
		}
	}
	return false
}

func (m *Manager) RequestJoinPlayer(steamID uint64, req *gcproto.PartyRequestJoinPlayer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.JoinPartyID == nil {
		return errBadRequest
	}
	target := m.parties[*req.JoinPartyID]
	if target == nil {
		return errNoSuchParty
	}
	// Mutual consent, the mirror image of InvitePlayer's check: the leader
	// already invited steamID, so this request is that invite's acceptance.
	if m.hasPendingLocked(target, steamID, gcproto.PendingInvited) {
		m.mergeIntoPartyLocked(steamID, target)
		return nil
	}
	target.pending = append(target.pending, &gcproto.PendingPartyMember{
		SteamID: gcproto.U64(steamID),
		Type:    gcproto.I32(int32(gcproto.PendingRequestedToJoin)),
		Inviter: gcproto.U64(steamID),
	})
	rec := &inviteRecord{partyID: target.id, inviter: steamID, target: target.leader, typ: int32(gcproto.InvitePendingJoin)}
	m.invitesFor[target.leader] = append(m.invitesFor[target.leader], rec)
	m.enqueuePushLocked(target.leader, gcproto.EMsgSOCreate, (&gcproto.SOSingleObject{
		Owner:      gcproto.U64(target.leader),
		TypeID:     gcproto.I32(gcproto.SOTypeTFPartyInvite),
		ObjectData: m.buildInviteSO(rec).Marshal(),
	}).Marshal())
	m.bumpAndPushParty(target)
	return nil
}

// mergeIntoPartyLocked moves steamID into target, leaving whatever party it
// was in before (kicking off a fresh solo party for anyone left behind as
// leader would already do via removeMemberLocked's own reassignment). This
// is the one place membership actually changes hands, used by every
// accept-style transition: mutual invite/join-request consent above, and
// PartyClearPendingPlayer below when the leader's pending entry was a join
// request they are admitting.
func (m *Manager) mergeIntoPartyLocked(steamID uint64, target *partyState) {
	old := m.partyOfLocked(steamID)
	if old != nil && old.id != target.id {
		m.removeMemberLocked(old, steamID)
	}
	if !containsUint64(target.members, steamID) {
		target.members = append(target.members, steamID)
	}
	if old != nil {
		target.names[steamID] = old.names[steamID]
	}
	m.partyOf[steamID] = target.id
	m.removePendingLocked(target, steamID)
	m.removeInviteRecordLocked(steamID, target.id)
	m.bumpAndPushParty(target)
	if old != nil && old.id != target.id {
		m.bumpAndPushParty(old)
	}
}

// ClearPendingPlayer removes a pending invite or join-request from the
// caller's own party. This is the decline/withdraw path -- rejecting an
// invite the leader sent, or the leader turning down (or simply
// dismissing) a join request, since a join request that should be admitted
// is instead accepted via RequestJoinPlayer's mutual-consent path above,
// matching the real client where the leader's UI action is "invite" (in
// effect accepting a pending join request) and never a separate accept.
func (m *Manager) ClearPendingPlayer(steamID uint64, req *gcproto.PartyClearPendingPlayer) (*gcproto.PartyClearPendingPlayerResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.partyOfLocked(steamID)
	if p == nil || p.leader != steamID || req.PendingPlayerID == nil {
		return nil, errNotLeader
	}
	target := *req.PendingPlayerID
	m.removePendingLocked(p, target)
	m.removeInviteRecordLocked(target, p.id)
	m.enqueuePushLocked(target, gcproto.EMsgSODestroy, (&gcproto.SOSingleObject{
		Owner:  gcproto.U64(target),
		TypeID: gcproto.I32(gcproto.SOTypeTFPartyInvite),
	}).Marshal())
	m.bumpAndPushParty(p)
	return &gcproto.PartyClearPendingPlayerResponse{}, nil
}

// PartyIDOf answers which party steamID currently belongs to, or 0 if they
// are not known yet (before their first OnConnect). Used by the transport
// layer only for logging/diagnostics -- every state change goes through the
// methods above, never through a caller poking a party ID directly.
func (m *Manager) PartyIDOf(steamID uint64) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.partyOf[steamID]
}

func (m *Manager) removePendingLocked(p *partyState, steamID uint64) {
	out := p.pending[:0]
	for _, pm := range p.pending {
		if pm.SteamID == nil || *pm.SteamID != steamID {
			out = append(out, pm)
		}
	}
	p.pending = out
}

func (m *Manager) removeInviteRecordLocked(target, partyID uint64) {
	recs := m.invitesFor[target]
	out := recs[:0]
	for _, r := range recs {
		if r.partyID != partyID {
			out = append(out, r)
		}
	}
	m.invitesFor[target] = out
}

func (m *Manager) ClearOtherPartyRequest(steamID uint64, req *gcproto.PartyClearOtherPartyRequest) (*gcproto.PartyClearOtherPartyRequestResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.OtherPartyID == nil {
		return nil, errBadRequest
	}
	other := m.parties[*req.OtherPartyID]
	if other == nil {
		return &gcproto.PartyClearOtherPartyRequestResponse{}, nil
	}
	m.removePendingLocked(other, steamID)
	m.removeInviteRecordLocked(other.leader, other.id)
	m.bumpAndPushParty(other)
	return &gcproto.PartyClearOtherPartyRequestResponse{}, nil
}

func (m *Manager) PromoteToLeader(steamID uint64, req *gcproto.PartyPromoteToLeader) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.partyOfLocked(steamID)
	if p == nil || p.leader != steamID || req.NewLeaderID == nil {
		return errNotLeader
	}
	if !containsUint64(p.members, *req.NewLeaderID) {
		return errBadRequest
	}
	p.leader = *req.NewLeaderID
	m.bumpAndPushParty(p)
	return nil
}

func (m *Manager) KickMember(steamID uint64, req *gcproto.PartyKickMember) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.partyOfLocked(steamID)
	if p == nil || p.leader != steamID || req.TargetID == nil {
		return errNotLeader
	}
	target := *req.TargetID
	if target == p.leader {
		return errBadRequest
	}
	m.removeMemberLocked(p, target)
	newParty := m.ensurePartyLocked(target, p.names[target])
	m.bumpAndPushParty(p)
	m.bumpAndPushParty(newParty)
	return nil
}

// removeMemberLocked removes steamID from p.members. It does not give them a
// new party -- callers that need one call ensurePartyLocked afterwards.
func (m *Manager) removeMemberLocked(p *partyState, steamID uint64) {
	out := p.members[:0]
	for _, id := range p.members {
		if id != steamID {
			out = append(out, id)
		}
	}
	p.members = out
	delete(m.partyOf, steamID)
	if p.leader == steamID && len(p.members) > 0 {
		p.leader = p.members[0]
	}
}

func containsUint64(s []uint64, v uint64) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (m *Manager) SendChat(steamID uint64, req *gcproto.PartySendChat) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.partyOfLocked(steamID)
	if p == nil || req.Msg == nil {
		return errNoParty
	}
	chat := &gcproto.PartyChatMsg{
		Type:    gcproto.I32(int32(gcproto.ChatMemberChat)),
		ActorID: gcproto.U64(steamID),
		Msg:     req.Msg,
	}
	body := chat.Marshal()
	for _, id := range p.members {
		if id == steamID {
			continue
		}
		m.enqueuePushLocked(id, gcproto.EMsgGCPartyChatMsg, body)
	}
	return nil
}

func (m *Manager) AcceptLobbyInvite(steamID uint64, req *gcproto.AcceptLobbyInvite) (*gcproto.AcceptLobbyInviteReply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.InvitedLobbyID == nil {
		return nil, errBadRequest
	}
	for _, p := range m.parties {
		if p.lobbyID == *req.InvitedLobbyID {
			// Standby-join the party's active match: the same seat-selling
			// path a fresh QueueForMatch with StandbyMatchID would take.
			if p.ticket != nil {
				_, _ = m.mmk.Enqueue(&mm.Ticket{
					MatchGroup:     wire.MatchGroup(p.matchGroup),
					Leader:         steamIDStr(steamID),
					Players:        []wire.AssignedPlayer{{SteamID: steamIDStr(steamID)}},
					StandbyMatchID: p.ticket.ID,
				})
			}
			break
		}
	}
	// Per CMsgAcceptLobbyInvite's own contract: accepting this invite
	// implicitly rejects every other invite named in
	// abandoning_invite_lobby_ids, plus the one just accepted -- clear
	// their SOs so the client's UI stops offering them.
	m.destroyLobbyInviteLocked(steamID, *req.InvitedLobbyID)
	for _, other := range req.AbandoningInviteLobbyIDs {
		m.destroyLobbyInviteLocked(steamID, other)
	}
	return &gcproto.AcceptLobbyInviteReply{}, nil
}

// KickLobby is the game-server-facing counterpart of
// k_EMsgGCGameServerKickingLobby: a server telling the GC it is done with a
// lobby (map change refused it, the match ended abnormally, etc). Whichever
// party currently holds this lobby ID has its ticket cancelled and its
// members notified, the same as any other queue-ending event.
func (m *Manager) KickLobby(lobbyID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.parties {
		if p.lobbyID != lobbyID {
			continue
		}
		m.clearTicketLocked(p)
		// The server is done RUNNING a match, not rejecting the queue --
		// k_EMsgGCKickedFromMatchmakingQueue's own comment scopes it to
		// "removed for not readying up" (the pre-assignment queue phase).
		// The post-match/in-game equivalent is LeaveGameAndPrepareToJoinParty,
		// which also hands the client the party it should land back in.
		for _, id := range p.members {
			m.enqueuePushLocked(id, gcproto.EMsgGCLeaveGameAndPrepareToJoinParty, (&gcproto.LeaveGameAndPrepareToJoinParty{PartyID: gcproto.U64(p.id)}).Marshal())
		}
		m.bumpAndPushParty(p)
		m.sweepLobbyInvitesLocked(lobbyID)
	}
}

// sweepLobbyInvitesLocked destroys every outstanding CTFLobbyInviteProto
// pointing at lobbyID, across every target -- called once a lobby has
// ended, so no one is left holding a "join this match" invite to a match
// that no longer exists.
func (m *Manager) sweepLobbyInvitesLocked(lobbyID uint64) {
	for target := range m.lobbyInvitesFor {
		m.destroyLobbyInviteLocked(target, lobbyID)
	}
}

// partiesForLobbyLocked returns every party currently associated with
// lobbyID. Normally there is exactly one, but a merged match (multiple
// parties queued into the same lobby) can have several.
func (m *Manager) partiesForLobbyLocked(lobbyID uint64) []*partyState {
	var out []*partyState
	for _, p := range m.parties {
		if p.lobbyID == lobbyID {
			out = append(out, p)
		}
	}
	return out
}

// NewMatchForLobby is the game-server-facing counterpart of
// k_EMsgGC_NewMatchForLobbyRequest: the server that was running lobbyID's
// match is asking for a fresh match for the same roster -- "let this group
// keep playing together" -- rather than sending everyone back to a cold
// queue. It re-enqueues every party still holding that lobby ID with an
// unchanged party roster, keeping the same lobby ID so the client-visible
// CSOTFGameServerLobby just updates in place instead of vanishing and
// reappearing. Reports success (at least one party was found and
// requeued) for the caller to build the NewMatchForLobbyResponse from.
func (m *Manager) NewMatchForLobby(req *gcproto.NewMatchForLobbyRequest) bool {
	if req.LobbyID == nil {
		return false
	}
	m.mu.Lock()
	parties := m.partiesForLobbyLocked(*req.LobbyID)
	m.mu.Unlock()
	if len(parties) == 0 {
		return false
	}
	ok := false
	for _, p := range parties {
		m.mu.Lock()
		group := p.matchGroup
		m.mu.Unlock()
		if err := m.enqueueTicketForParty(p, group); err != nil {
			m.log.Warn("gc: NewMatchForLobby requeue failed", "lobby_id", *req.LobbyID, "party_id", p.id, "err", err)
			continue
		}
		ok = true
	}
	return ok
}

// ProcessVoteKick is the game-server-facing counterpart of
// k_EMsgGC_ProcessMatchVoteKick: the server tallied an in-game vote to kick
// a player and is asking the GC for a ruling. This coordinator defers to
// the server's own tally (default_pass) rather than re-deriving a
// decision from the individual votes -- the server already enforces who
// may vote and how many votes are needed, so second-guessing that here
// would just be a second, less informed implementation of the same rule.
// If the kick is approved, the target is pulled out of their party (into
// a fresh solo one, the same reassignment KickMember gives anyone it
// removes) and pushed LeaveGameAndPrepareToJoinParty so their client
// returns to the party/lobby UI instead of sitting on a server that is
// about to eject them. It reports rip for the caller to build both the
// ProcessMatchVoteKickResponse and, if true, the accompanying
// KickPlayerFromLobby push to the server.
func (m *Manager) ProcessVoteKick(req *gcproto.ProcessMatchVoteKick) bool {
	rip := req.DefaultPass != nil && *req.DefaultPass
	if !rip || req.TargetSteamID == nil {
		return rip
	}
	target := *req.TargetSteamID

	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.partyOfLocked(target)
	if old == nil {
		return rip
	}
	name := old.names[target]
	m.removeMemberLocked(old, target)
	newParty := m.ensurePartyLocked(target, name)
	m.bumpAndPushParty(old)
	m.bumpAndPushParty(newParty)
	m.enqueuePushLocked(target, gcproto.EMsgGCLeaveGameAndPrepareToJoinParty, (&gcproto.LeaveGameAndPrepareToJoinParty{PartyID: gcproto.U64(newParty.id)}).Marshal())
	return rip
}

// ---------------------------------------------------------------------------
// Background tick: watch active tickets, deliver match-found / kicks.
// ---------------------------------------------------------------------------

func (m *Manager) tick() {
	m.mu.Lock()
	type watch struct {
		p      *partyState
		ticket string
	}
	var toCheck []watch
	for _, p := range m.parties {
		if p.ticket != nil {
			toCheck = append(toCheck, watch{p, p.ticket.ID})
		}
	}
	m.mu.Unlock()

	for _, w := range toCheck {
		status, err := m.mmk.Status(w.ticket)
		m.mu.Lock()
		p := w.p
		if p.ticket == nil || p.ticket.ID != w.ticket {
			m.mu.Unlock()
			continue // ticket already replaced/cleared since we sampled it
		}
		if err != nil {
			m.mu.Unlock()
			continue
		}
		if status.State != p.lastTicketState {
			m.handleTicketTransitionLocked(p, status)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) handleTicketTransitionLocked(p *partyState, status wire.QueueStatus) {
	p.lastTicketState = status.State
	switch status.State {
	case wire.QueueStateAssigned:
		if p.lobbyID == 0 {
			p.lobbyID = m.nextLobbyID
			m.nextLobbyID++
		}
		lobby := m.buildLobbySOLocked(p)
		if lobby == nil {
			return
		}
		body := lobby.Marshal()
		for _, id := range p.members {
			m.enqueuePushLocked(id, gcproto.EMsgSOUpdate, (&gcproto.SOSingleObject{
				Owner:      gcproto.U64(id),
				TypeID:     gcproto.I32(gcproto.SOTypeTFGameServerLobby),
				ObjectData: body,
			}).Marshal())
		}
		m.bumpAndPushParty(p)
	case wire.QueueStateCancelled, wire.QueueStateExpired, wire.QueueStateFailed:
		for _, id := range p.members {
			m.enqueuePushLocked(id, gcproto.EMsgGCKickedFromMatchmakingQueue, (&gcproto.KickedFromMatchmakingQueue{}).Marshal())
		}
		p.ticket = nil
		p.lobbyID = 0
		m.bumpAndPushParty(p)
	}
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

type gcError string

func (e gcError) Error() string { return string(e) }

const (
	errNoParty     gcError = "not in a party"
	errNoSuchParty gcError = "no such party"
	errNotLeader   gcError = "only the party leader can do that"
	errBadRequest  gcError = "malformed request"
)
