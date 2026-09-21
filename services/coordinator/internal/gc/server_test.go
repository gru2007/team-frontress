package gc

import (
	"context"
	"errors"
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/gcwire"
	gamemaps "github.com/gru2007/team-frontress/services/coordinator/internal/maps"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/steamauth"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
	"google.golang.org/protobuf/proto"
)

type fakeMM struct {
	enqueued     *mm.Ticket
	enqueueCalls int
	enqueueErr   error
	status       wire.QueueStatus
	match        *wire.Assignment
}

func (f *fakeMM) Enqueue(ticket *mm.Ticket) (*mm.Ticket, error) {
	f.enqueueCalls++
	f.enqueued = ticket
	if f.enqueueErr != nil {
		return nil, f.enqueueErr
	}
	return &mm.Ticket{ID: "ticket"}, nil
}

func TestProtocolTwoRetriesAreIdempotent(t *testing.T) {
	f := new(fakeMM)
	s := New("secret", f, steamauth.DevVerifier{}, nil)
	steamID := "76561198000000001"
	instanceID := "client-process-1"
	hello := gcwire.ExchangeRequest{
		Protocol: 2, InstanceID: instanceID, ClientSequence: 1,
		Role: "client", SteamID: steamID,
		Messages: []gcwire.Message{packet(t, msgClientHello, &gcproto.CMsgClientHello{})},
	}
	first, err := s.Exchange(t.Context(), hello)
	if err != nil {
		t.Fatal(err)
	}
	if first.ServerSequence != 1 || len(first.Messages) != 2 {
		t.Fatalf("initial response = %+v", first)
	}

	// The initial response may disappear before the client learns the session
	// ID. Its process identity must recover the same response, not run Hello twice.
	retryHello, err := s.Exchange(t.Context(), hello)
	if err != nil {
		t.Fatal(err)
	}
	if retryHello.SessionID != first.SessionID || retryHello.ServerSequence != first.ServerSequence || len(retryHello.Messages) != len(first.Messages) {
		t.Fatalf("retried hello changed response: first=%+v retry=%+v", first, retryHello)
	}

	id := uint64(76561198000000001)
	group := gcproto.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12
	queue := gcwire.ExchangeRequest{
		Protocol: 2, SessionID: first.SessionID, InstanceID: instanceID,
		ClientSequence: 2, AckServerSequence: first.ServerSequence,
		Role: "client", SteamID: steamID,
		Messages: []gcwire.Message{packet(t, msgQueue, &gcproto.CMsgPartyQueueForMatch{PartyId: &id, MatchGroup: &group})},
	}
	queued, err := s.Exchange(t.Context(), queue)
	if err != nil {
		t.Fatal(err)
	}
	if f.enqueueCalls != 1 || queued.ServerSequence != 2 || len(queued.Messages) != 2 {
		t.Fatalf("queue response=%+v enqueue calls=%d", queued, f.enqueueCalls)
	}

	retriedQueue, err := s.Exchange(t.Context(), queue)
	if err != nil {
		t.Fatal(err)
	}
	if f.enqueueCalls != 1 || retriedQueue.ServerSequence != queued.ServerSequence || len(retriedQueue.Messages) != len(queued.Messages) {
		t.Fatalf("retried queue was not idempotent: response=%+v enqueue calls=%d", retriedQueue, f.enqueueCalls)
	}

	acked, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 2, SessionID: first.SessionID, InstanceID: instanceID,
		ClientSequence: 3, AckServerSequence: queued.ServerSequence,
		Role: "client", SteamID: steamID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(acked.Messages) != 0 {
		t.Fatalf("acknowledged messages were sent again: %+v", acked.Messages)
	}
}

func TestQueueRefusalCompletesReliableJobWithoutResettingGCSession(t *testing.T) {
	f := &fakeMM{enqueueErr: errors.New("party is not eligible")}
	s := New("secret", f, steamauth.DevVerifier{}, nil)
	steamID := "76561198000000001"
	resp, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, Role: "client", SteamID: steamID,
		Messages: []gcwire.Message{packet(t, msgClientHello, &gcproto.CMsgClientHello{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := uint64(76561198000000001)
	group := gcproto.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12
	resp, err = s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, SessionID: resp.SessionID, Role: "client", SteamID: steamID,
		Messages: []gcwire.Message{packet(t, msgQueue, &gcproto.CMsgPartyQueueForMatch{PartyId: &id, MatchGroup: &group})},
	})
	if err != nil {
		t.Fatalf("semantic refusal broke the GC transport: %v", err)
	}
	got := types(t, resp.Messages)
	if len(got) != 2 || got[0] != msgQueueReply || got[1] != msgSOUpdate {
		t.Fatalf("queue refusal replies = %v", got)
	}
}
func (f *fakeMM) Cancel(string) error                                  { return nil }
func (f *fakeMM) Status(string) (wire.QueueStatus, error)              { return f.status, nil }
func (f *fakeMM) MatchAssignment(string) (*wire.Assignment, bool)      { return f.match, f.match != nil }
func (f *fakeMM) ReportResult(context.Context, wire.MatchResult) error { return nil }

func packet(t *testing.T, typ uint32, body proto.Message) gcwire.Message {
	t.Helper()
	m, err := gcwire.Encode(typ, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func types(t *testing.T, messages []gcwire.Message) []uint32 {
	t.Helper()
	out := make([]uint32, 0, len(messages))
	for _, message := range messages {
		p, err := gcwire.Decode(message)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Type)
	}
	return out
}

func TestClientHelloAndQueueUseValveMessages(t *testing.T) {
	f := new(fakeMM)
	s := New("secret", f, steamauth.DevVerifier{}, nil)
	steamID := "76561198000000001"
	resp, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, Role: "client", SteamID: steamID,
		Messages: []gcwire.Message{packet(t, msgClientHello, &gcproto.CMsgClientHello{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := types(t, resp.Messages)
	if len(got) != 2 || got[0] != msgClientWelcome || got[1] != msgCacheSubscribed {
		t.Fatalf("hello replies = %v", got)
	}

	id := uint64(76561198000000001)
	group := gcproto.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12
	criteria := &gcproto.CTFGroupMatchCriteriaProto{CasualCriteria: &gcproto.CTFCasualMatchCriteria{SelectedMapsBits: []uint32{1}}}
	resp, err = s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, SessionID: resp.SessionID, Role: "client", SteamID: steamID,
		Messages: []gcwire.Message{packet(t, msgQueue, &gcproto.CMsgPartyQueueForMatch{
			PartyId: &id, MatchGroup: &group, FinalOptions: &gcproto.CTFPartyOptions{GroupCriteria: criteria},
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.enqueued == nil || f.enqueued.Leader != wire.SteamID(steamID) || f.enqueued.MatchGroup != wire.MatchGroupCasual12v12 {
		t.Fatalf("enqueue = %+v", f.enqueued)
	}
	if len(f.enqueued.Maps) != 1 || f.enqueued.Maps[0] != gamemaps.All[0].Name {
		t.Fatalf("map preferences = %v", f.enqueued.Maps)
	}
	got = types(t, resp.Messages)
	if len(got) != 2 || got[0] != msgQueueReply || got[1] != msgSOUpdate {
		t.Fatalf("queue replies = %v", got)
	}
}

func TestServerMustAuthenticateAndGetsLobby(t *testing.T) {
	f := &fakeMM{match: &wire.Assignment{
		MatchID: "2a", MatchGroup: wire.MatchGroupCasual12v12,
		Map: "cp_process_final", Connect: "127.0.0.1:27015",
		Roster: []wire.AssignedPlayer{{SteamID: "76561198000000001", Team: wire.TeamRed}},
	}}
	s := New("secret", f, steamauth.DevVerifier{}, nil)
	_, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{Protocol: 1, Role: "server", MatchID: "2a", ServerToken: "wrong"})
	if err == nil {
		t.Fatal("server with wrong token was accepted")
	}
	resp, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, Role: "server", MatchID: "2a", ServerToken: "secret",
		Messages: []gcwire.Message{packet(t, msgServerHello, &gcproto.CMsgServerHello{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := types(t, resp.Messages)
	if len(got) != 2 || got[0] != msgServerWelcome || got[1] != msgCacheSubscribed {
		t.Fatalf("server hello replies = %v", got)
	}
}

func TestStandbyQueueIsPublishedInPartySharedObject(t *testing.T) {
	f := new(fakeMM)
	s := New("secret", f, steamauth.DevVerifier{}, nil)
	steamID := "76561198000000001"
	resp, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, Role: "client", SteamID: steamID,
		Messages: []gcwire.Message{packet(t, msgClientHello, &gcproto.CMsgClientHello{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := uint64(76561198000000001)
	p := s.parties[wire.SteamID(steamID)]
	p.lobbyID = 42
	p.lobbyMatchID = "match-42"
	p.lobbyGroup = wire.MatchGroupCasual12v12

	resp, err = s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, SessionID: resp.SessionID, Role: "client", SteamID: steamID,
		Messages: []gcwire.Message{packet(t, msgQueueStandby, &gcproto.CMsgPartyQueueForStandby{
			PartyId: &id, PartyLobbyId: proto.Uint64(42),
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.enqueued == nil || f.enqueued.StandbyMatchID != "match-42" || !p.standby[wire.SteamID(steamID)] {
		t.Fatalf("standby enqueue was not retained: ticket=%+v party=%+v", f.enqueued, p.standby)
	}
	partySO, err := s.partyProto(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(partySO.Members) != 1 || !partySO.Members[0].GetLobbyStandby() {
		t.Fatalf("party SO does not expose standby: %+v", partySO.Members)
	}
	got := types(t, resp.Messages)
	if len(got) != 2 || got[0] != msgQueueStandbyReply || got[1] != msgSOUpdate {
		t.Fatalf("standby replies = %v", got)
	}

	if _, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, SessionID: resp.SessionID, Role: "client", SteamID: steamID,
		Messages: []gcwire.Message{packet(t, msgRemoveStandby, &gcproto.CMsgPartyRemoveFromStandbyQueue{PartyId: &id})},
	}); err != nil {
		t.Fatal(err)
	}
	if p.standby[wire.SteamID(steamID)] {
		t.Fatal("standby flag survived removal")
	}
}

func TestPartyInviteJoinAndChatAreCoordinatorOwned(t *testing.T) {
	f := new(fakeMM)
	s := New("secret", f, steamauth.DevVerifier{}, nil)
	leader := "76561198000000001"
	member := "76561198000000002"

	open := func(id string) string {
		resp, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
			Protocol: 1, Role: "client", SteamID: id,
			Messages: []gcwire.Message{packet(t, msgClientHello, &gcproto.CMsgClientHello{})},
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp.SessionID
	}
	leaderSession := open(leader)
	memberSession := open(member)

	leaderID := uint64(76561198000000001)
	memberID := uint64(76561198000000002)
	if _, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, SessionID: leaderSession, Role: "client", SteamID: leader,
		Messages: []gcwire.Message{packet(t, msgInvitePlayer, &gcproto.CMsgPartyInvitePlayer{
			PartyId: &leaderID, PlayerId: &memberID,
		})},
	}); err != nil {
		t.Fatal(err)
	}
	if s.parties[wire.SteamID(leader)].pending[wire.SteamID(member)] != gcproto.TFPendingPartyMember_Invited {
		t.Fatal("invite did not become authoritative party state")
	}

	joinPlayer := leaderID
	expecting := true
	resp, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, SessionID: memberSession, Role: "client", SteamID: member,
		Messages: []gcwire.Message{packet(t, msgRequestJoin, &gcproto.CMsgPartyRequestJoinPlayer{
			JoinPlayerId: &joinPlayer, ExpectingInvite: &expecting,
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := s.parties[wire.SteamID(leader)]
	if s.parties[wire.SteamID(member)] != p || !partyContains(p, wire.SteamID(member)) {
		t.Fatalf("joined member is not in leader party: %+v", p)
	}
	got := types(t, resp.Messages)
	if len(got) == 0 {
		t.Fatal("join produced no shared-object updates")
	}

	chat := "hello"
	if _, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, SessionID: memberSession, Role: "client", SteamID: member,
		Messages: []gcwire.Message{packet(t, msgSendChat, &gcproto.CMsgPartySendChat{PartyId: &leaderID, Msg: &chat})},
	}); err != nil {
		t.Fatal(err)
	}
	leaderPoll, err := s.Exchange(t.Context(), gcwire.ExchangeRequest{
		Protocol: 1, SessionID: leaderSession, Role: "client", SteamID: leader,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundChat := false
	for _, typ := range types(t, leaderPoll.Messages) {
		foundChat = foundChat || typ == msgChat
	}
	if !foundChat {
		t.Fatal("party chat was not delivered to the leader")
	}
}
