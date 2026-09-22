package gcparty

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// fakeMM is the smallest Matchmaker double that lets these tests drive
// tick() deterministically: the test sets a ticket's status directly
// instead of waiting on a real queue.
type fakeMM struct {
	mu        sync.Mutex
	nextID    int
	statuses  map[string]wire.QueueStatus
	cancelled map[string]bool
}

func newFakeMM() *fakeMM {
	return &fakeMM{statuses: map[string]wire.QueueStatus{}, cancelled: map[string]bool{}}
}

func (f *fakeMM) Enqueue(t *mm.Ticket) (*mm.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := "t" + string(rune('0'+f.nextID))
	t.ID = id
	f.statuses[id] = wire.QueueStatus{TicketID: id, State: wire.QueueStateSearching}
	return t, nil
}

func (f *fakeMM) Cancel(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled[id] = true
	delete(f.statuses, id)
	return nil
}

func (f *fakeMM) Status(id string) (wire.QueueStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statuses[id], nil
}

func (f *fakeMM) setStatus(id string, st wire.QueueStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[id] = st
}

// drainNow is a zero-timeout Drain: it returns whatever is already queued
// without blocking, which is all these tests need since they push
// synchronously from the same goroutine.
func drainNow(m *Manager, steamID uint64) []Push {
	return m.Drain(context.Background(), steamID, time.Millisecond)
}

// countSubscribedTypeIDs reads every embedded SOCacheSubscribedType (field 2)
// out of a marshaled SOCacheSubscribed and returns each one's TypeID (its
// own field 1), without needing an unmarshaler in gcproto -- that message
// is GC -> client only and is never parsed on this side in production.
func countSubscribedTypeIDs(t *testing.T, body []byte) []int32 {
	t.Helper()
	var ids []int32
	r := gcproto.NewReader(body)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			t.Fatalf("bad SOCacheSubscribed: %v", err)
		}
		if field != 2 {
			if err := r.Skip(wt); err != nil {
				t.Fatalf("skip: %v", err)
			}
			continue
		}
		sub, err := r.Bytes()
		if err != nil {
			t.Fatalf("bad SubscribedType bytes: %v", err)
		}
		sr := gcproto.NewReader(sub)
		for sr.Len() > 0 {
			sf, swt, err := sr.Tag()
			if err != nil {
				t.Fatalf("bad SubscribedType: %v", err)
			}
			if sf == 1 {
				v, err := sr.Varint()
				if err != nil {
					t.Fatalf("bad TypeID: %v", err)
				}
				ids = append(ids, int32(v))
				continue
			}
			if err := sr.Skip(swt); err != nil {
				t.Fatalf("skip: %v", err)
			}
		}
	}
	return ids
}

func TestOnConnectCreatesSoloParty(t *testing.T) {
	m := New(newFakeMM(), nil)
	pushes := m.OnConnect(1, "Alice")
	if len(pushes) != 2 {
		t.Fatalf("want 2 pushes (subscribed + up-to-date), got %d", len(pushes))
	}
	if pushes[0].EMsg != gcproto.EMsgSOCacheSubscribed {
		t.Errorf("first push = %v, want EMsgSOCacheSubscribed", pushes[0].EMsg)
	}
	if pushes[1].EMsg != gcproto.EMsgSOCacheSubscribedUpToDate {
		t.Errorf("second push = %v, want EMsgSOCacheSubscribedUpToDate", pushes[1].EMsg)
	}
	ids := countSubscribedTypeIDs(t, pushes[0].Body)
	if len(ids) != 1 || ids[0] != gcproto.SOTypeTFParty {
		t.Fatalf("solo-party snapshot should carry exactly one TFParty object, got %v", ids)
	}
}

func TestQueueForMatchOnlyLeaderCanQueue(t *testing.T) {
	mmk := newFakeMM()
	m := New(mmk, nil)
	m.OnConnect(1, "Leader")
	m.OnConnect(2, "Member")
	drainNow(m, 1)
	drainNow(m, 2)

	// 2 invites 1... no, simpler: merge via InvitePlayer + RequestJoinPlayer
	// mutual consent (2 asks to join 1's party, 1 invites 2 back).
	p1 := m.PartyIDOf(1)
	if err := m.RequestJoinPlayer(2, &gcproto.PartyRequestJoinPlayer{JoinPartyID: gcproto.U64(p1)}); err != nil {
		t.Fatalf("RequestJoinPlayer: %v", err)
	}
	if err := m.InvitePlayer(1, &gcproto.PartyInvitePlayer{PlayerID: gcproto.U64(2)}); err != nil {
		t.Fatalf("InvitePlayer: %v", err)
	}
	if m.PartyIDOf(2) != p1 {
		t.Fatalf("mutual consent should have merged player 2 into party %d, got %d", p1, m.PartyIDOf(2))
	}

	group := int32(1)
	if _, err := m.QueueForMatch(2, &gcproto.PartyQueueForMatch{MatchGroup: &group}); err == nil {
		t.Fatalf("member (non-leader) should not be able to queue")
	}
	if _, err := m.QueueForMatch(1, &gcproto.PartyQueueForMatch{MatchGroup: &group}); err != nil {
		t.Fatalf("leader QueueForMatch: %v", err)
	}
}

// soSingleObjectTypeID reads field 2 (TypeID) out of a marshaled
// SOSingleObject without needing an exported unmarshaler in gcproto (that
// type is only ever built server-side in production).
func soSingleObjectTypeID(t *testing.T, body []byte) int32 {
	t.Helper()
	r := gcproto.NewReader(body)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			t.Fatalf("bad SOSingleObject: %v", err)
		}
		if field == 2 {
			v, err := r.Varint()
			if err != nil {
				t.Fatalf("bad TypeID field: %v", err)
			}
			return int32(v)
		}
		if err := r.Skip(wt); err != nil {
			t.Fatalf("skip: %v", err)
		}
	}
	return 0
}

func TestTickPushesLobbyOnAssignedAndKicksOnCancelled(t *testing.T) {
	mmk := newFakeMM()
	m := New(mmk, nil)
	m.OnConnect(1, "Leader")
	drainNow(m, 1)

	group := int32(2)
	if _, err := m.QueueForMatch(1, &gcproto.PartyQueueForMatch{MatchGroup: &group}); err != nil {
		t.Fatalf("QueueForMatch: %v", err)
	}
	drainNow(m, 1) // clear the "queued" party-update push before asserting on the tick's own push

	p := m.parties[m.PartyIDOf(1)]
	ticketID := p.ticket.ID
	mmk.setStatus(ticketID, wire.QueueStatus{
		TicketID: ticketID,
		State:    wire.QueueStateAssigned,
		Assignment: &wire.Assignment{
			MatchID: "abc123", MatchGroup: wire.MatchGroup(2), Map: "cp_process_final",
			Connect: "203.0.113.5:27015",
			Roster:  []wire.AssignedPlayer{{SteamID: "1", Name: "Leader"}},
		},
	})
	m.tick()

	pushes := drainNow(m, 1)
	var sawLobby bool
	for _, pu := range pushes {
		if pu.EMsg != gcproto.EMsgSOUpdate {
			continue
		}
		if soSingleObjectTypeID(t, pu.Body) != gcproto.SOTypeTFGameServerLobby {
			continue
		}
		sawLobby = true
	}
	if !sawLobby {
		t.Fatalf("expected an SOUpdate carrying SOTypeTFGameServerLobby among %d pushes", len(pushes))
	}
	if p.lobbyID == 0 {
		t.Fatalf("expected a lobby ID to be assigned once the ticket was Assigned")
	}

	// Now drive it to Cancelled and confirm the party is kicked and cleared.
	mmk.setStatus(ticketID, wire.QueueStatus{TicketID: ticketID, State: wire.QueueStateCancelled})
	m.tick()
	pushes = drainNow(m, 1)
	sawKick := false
	for _, pu := range pushes {
		if pu.EMsg == gcproto.EMsgGCKickedFromMatchmakingQueue {
			sawKick = true
		}
	}
	if !sawKick {
		t.Fatalf("expected a KickedFromMatchmakingQueue push on cancellation, got %d pushes", len(pushes))
	}
	if p.ticket != nil {
		t.Fatalf("ticket should be cleared after cancellation")
	}
}

func TestKickMemberMovesPlayerToNewSoloParty(t *testing.T) {
	mmk := newFakeMM()
	m := New(mmk, nil)
	m.OnConnect(1, "Leader")
	m.OnConnect(2, "Member")
	drainNow(m, 1)
	drainNow(m, 2)

	p1 := m.PartyIDOf(1)
	_ = m.RequestJoinPlayer(2, &gcproto.PartyRequestJoinPlayer{JoinPartyID: gcproto.U64(p1)})
	_ = m.InvitePlayer(1, &gcproto.PartyInvitePlayer{PlayerID: gcproto.U64(2)})
	if m.PartyIDOf(2) != p1 {
		t.Fatalf("setup: expected merge before kick test")
	}

	if err := m.KickMember(1, &gcproto.PartyKickMember{TargetID: gcproto.U64(2)}); err != nil {
		t.Fatalf("KickMember: %v", err)
	}
	if m.PartyIDOf(2) == p1 {
		t.Fatalf("kicked member should no longer be in the leader's party")
	}
	if m.PartyIDOf(2) == 0 {
		t.Fatalf("kicked member should have a party of their own")
	}
}

// assignLobby drives steamID's solo-leader party through QueueForMatch and
// a fake Assigned status so it ends up holding both an active ticket and a
// lobby ID, mirroring what a real match-found does. Returns the lobby ID.
func assignLobby(t *testing.T, m *Manager, mmk *fakeMM, steamID uint64) uint64 {
	t.Helper()
	group := int32(2)
	if _, err := m.QueueForMatch(steamID, &gcproto.PartyQueueForMatch{MatchGroup: &group}); err != nil {
		t.Fatalf("QueueForMatch: %v", err)
	}
	drainNow(m, steamID)
	p := m.parties[m.PartyIDOf(steamID)]
	ticketID := p.ticket.ID
	mmk.setStatus(ticketID, wire.QueueStatus{
		TicketID: ticketID,
		State:    wire.QueueStateAssigned,
		Assignment: &wire.Assignment{
			MatchID: "abc123", MatchGroup: wire.MatchGroup(2), Map: "cp_process_final",
			Connect: "203.0.113.5:27015",
			Roster:  []wire.AssignedPlayer{{SteamID: steamIDStr(steamID)}},
		},
	})
	m.tick()
	drainNow(m, steamID)
	if p.lobbyID == 0 {
		t.Fatalf("setup: expected a lobby ID after Assigned")
	}
	return p.lobbyID
}

func TestNewMatchForLobbyKeepsSameLobbyIDWithFreshTicket(t *testing.T) {
	mmk := newFakeMM()
	m := New(mmk, nil)
	m.OnConnect(1, "Leader")
	drainNow(m, 1)

	lobbyID := assignLobby(t, m, mmk, 1)
	p := m.parties[m.PartyIDOf(1)]
	oldTicketID := p.ticket.ID

	ok := m.NewMatchForLobby(&gcproto.NewMatchForLobbyRequest{LobbyID: gcproto.U64(lobbyID)})
	if !ok {
		t.Fatalf("NewMatchForLobby should succeed for a lobby that still has a party")
	}
	if p.lobbyID != lobbyID {
		t.Fatalf("NewMatchForLobby should keep the same lobby ID, got %d want %d", p.lobbyID, lobbyID)
	}
	if p.ticket == nil || p.ticket.ID == oldTicketID {
		t.Fatalf("NewMatchForLobby should issue a fresh ticket for the same roster")
	}

	if ok := m.NewMatchForLobby(&gcproto.NewMatchForLobbyRequest{LobbyID: gcproto.U64(lobbyID + 999)}); ok {
		t.Fatalf("NewMatchForLobby should fail for an unknown lobby ID")
	}
}

func TestProcessVoteKickRemovesTargetAndPushesLeaveGame(t *testing.T) {
	mmk := newFakeMM()
	m := New(mmk, nil)
	m.OnConnect(1, "Leader")
	m.OnConnect(2, "Member")
	drainNow(m, 1)
	drainNow(m, 2)

	p1 := m.PartyIDOf(1)
	_ = m.RequestJoinPlayer(2, &gcproto.PartyRequestJoinPlayer{JoinPartyID: gcproto.U64(p1)})
	_ = m.InvitePlayer(1, &gcproto.PartyInvitePlayer{PlayerID: gcproto.U64(2)})
	drainNow(m, 1)
	drainNow(m, 2)

	rip := m.ProcessVoteKick(&gcproto.ProcessMatchVoteKick{
		TargetSteamID: gcproto.U64(2),
		DefaultPass:   gcproto.Bl(true),
	})
	if !rip {
		t.Fatalf("ProcessVoteKick should honor DefaultPass=true and report rip=true")
	}
	if m.PartyIDOf(2) == p1 {
		t.Fatalf("kicked-by-vote target should no longer be in the old party")
	}

	pushes := drainNow(m, 2)
	var sawLeave bool
	for _, pu := range pushes {
		if pu.EMsg == gcproto.EMsgGCLeaveGameAndPrepareToJoinParty {
			sawLeave = true
		}
	}
	if !sawLeave {
		t.Fatalf("expected a LeaveGameAndPrepareToJoinParty push to the vote-kicked target, got %d pushes", len(pushes))
	}

	// A no-pass tally should not touch the roster at all.
	rip2 := m.ProcessVoteKick(&gcproto.ProcessMatchVoteKick{TargetSteamID: gcproto.U64(1), DefaultPass: gcproto.Bl(false)})
	if rip2 {
		t.Fatalf("ProcessVoteKick should report rip=false when DefaultPass is false")
	}
	if m.PartyIDOf(1) != p1 {
		t.Fatalf("a failed vote-kick must not move the target out of their party")
	}
}

func TestKickLobbyPushesLeaveGameNotQueueKick(t *testing.T) {
	mmk := newFakeMM()
	m := New(mmk, nil)
	m.OnConnect(1, "Leader")
	drainNow(m, 1)

	lobbyID := assignLobby(t, m, mmk, 1)
	m.KickLobby(lobbyID)

	pushes := drainNow(m, 1)
	var sawLeave, sawQueueKick bool
	for _, pu := range pushes {
		switch pu.EMsg {
		case gcproto.EMsgGCLeaveGameAndPrepareToJoinParty:
			sawLeave = true
		case gcproto.EMsgGCKickedFromMatchmakingQueue:
			sawQueueKick = true
		}
	}
	if !sawLeave {
		t.Fatalf("KickLobby (post-match) should push LeaveGameAndPrepareToJoinParty, got %d pushes", len(pushes))
	}
	if sawQueueKick {
		t.Fatalf("KickLobby (post-match) must not push the queue-phase KickedFromMatchmakingQueue")
	}
}

func TestInvitePlayerPushesLobbyInviteWhenPartyHasActiveMatch(t *testing.T) {
	mmk := newFakeMM()
	m := New(mmk, nil)
	m.OnConnect(1, "Leader")
	m.OnConnect(2, "Friend")
	drainNow(m, 1)
	drainNow(m, 2)

	_ = assignLobby(t, m, mmk, 1)

	if err := m.InvitePlayer(1, &gcproto.PartyInvitePlayer{PlayerID: gcproto.U64(2)}); err != nil {
		t.Fatalf("InvitePlayer: %v", err)
	}

	pushes := drainNow(m, 2)
	var sawLobbyInvite bool
	for _, pu := range pushes {
		if pu.EMsg == gcproto.EMsgSOCreate && soSingleObjectTypeID(t, pu.Body) == gcproto.SOTypeTFLobbyInvite {
			sawLobbyInvite = true
		}
	}
	if !sawLobbyInvite {
		t.Fatalf("inviting a friend while in an active match should also push a TFLobbyInvite SO, got %d pushes", len(pushes))
	}
}
