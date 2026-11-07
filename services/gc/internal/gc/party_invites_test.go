package gc

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func TestInvitationSurvivesInviterLeavingAndRejectsCancelledInvite(t *testing.T) {
	g, err := New(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	var clients []*session.Session
	for i := uint64(1); i <= 4; i++ {
		s := g.sessions.Open(session.Client, steamauth.MinIndividual+i, nil)
		g.ClientConnected(s, true)
		clients = append(clients, s)
	}
	leader, member, invited, cancelled := clients[0], clients[1], clients[2], clients[3]
	p := g.parties[g.player(leader.SteamID).PartyID]
	if err := g.joinParty(p, member.SteamID); err != nil {
		t.Fatal(err)
	}
	send := func(s *session.Session, kind gcpb.ETFGCMsg, body proto.Message) {
		t.Helper()
		raw, err := wire.Encode(uint32(kind), body, wire.NoJob)
		if err != nil {
			t.Fatal(err)
		}
		g.Handle(s, raw)
	}
	for _, s := range []*session.Session{invited, cancelled} {
		send(leader, gcpb.ETFGCMsg_k_EMsgGCParty_InvitePlayer, &gcpb.CMsgPartyInvitePlayer{
			PartyId: proto.Uint64(p.ID), PlayerId: proto.Uint64(s.SteamID),
		})
	}
	send(leader, gcpb.ETFGCMsg_k_EMsgGCParty_ClearPendingPlayer, &gcpb.CMsgPartyClearPendingPlayer{
		PartyId: proto.Uint64(p.ID), PendingPlayerId: proto.Uint64(cancelled.SteamID),
	})
	g.newParty(leader.SteamID)
	g.player(leader.SteamID).Online = false
	for _, s := range []*session.Session{invited, cancelled} {
		send(s, gcpb.ETFGCMsg_k_EMsgGCParty_RequestJoinPlayer, &gcpb.CMsgPartyRequestJoinPlayer{
			JoinPlayerId: proto.Uint64(leader.SteamID), JoinPartyId: proto.Uint64(p.ID),
			CurrentPartyId: proto.Uint64(g.player(s.SteamID).PartyID), ExpectingInvite: proto.Bool(true),
		})
	}
	if !p.has(invited.SteamID) || p.has(cancelled.SteamID) || p.Leader != member.SteamID {
		t.Fatal("invitation did not survive the inviter leaving, or a cancelled one was accepted")
	}
	if _, ok := p.pendingFor(cancelled.SteamID); ok {
		t.Fatal("accepting a cancelled invitation created a new join request")
	}
}
