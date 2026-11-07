package gc

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/reports"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func TestPartyKickAndLeaveRespectLeaderAndMatch(t *testing.T) {
	g, err := New(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	leader := g.sessions.Open(session.Client, steamauth.MinIndividual+1, nil)
	member := g.sessions.Open(session.Client, steamauth.MinIndividual+2, nil)
	g.ClientConnected(leader, true)
	g.ClientConnected(member, true)
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
	send(member, gcpb.ETFGCMsg_k_EMsgGCParty_KickMember, &gcpb.CMsgPartyKickMember{
		PartyId: proto.Uint64(p.ID), TargetId: proto.Uint64(leader.SteamID),
	})
	if !p.has(leader.SteamID) || p.Leader != leader.SteamID {
		t.Fatal("member could kick leader")
	}
	p.Queues[casualGroup] = g.now()
	send(leader, gcpb.ETFGCMsg_k_EMsgGCParty_KickMember, &gcpb.CMsgPartyKickMember{
		PartyId: proto.Uint64(p.ID), TargetId: proto.Uint64(member.SteamID),
	})
	if p.has(member.SteamID) || len(p.Queues) != 0 || g.player(member.SteamID).PartyID == p.ID {
		t.Fatal("leader kick did not split the group and cancel its queue")
	}
	if err := g.joinParty(p, member.SteamID); err != nil {
		t.Fatal(err)
	}
	l := &Lobby{ID: 50, MatchID: 51}
	l.addMember(leader.SteamID, 2, p.ID, g.now())
	l.addMember(member.SteamID, 3, p.ID, g.now())
	g.lobbies[l.ID] = l
	send(leader, gcpb.ETFGCMsg_k_EMsgGCExitMatchmaking, &gcpb.CMsgExitMatchmaking{PartyId: proto.Uint64(p.ID)})
	if p.has(leader.SteamID) || p.Leader != member.SteamID || l.member(leader.SteamID) == nil {
		t.Fatal("party exit failed to transfer leadership or abandoned the match")
	}
}

func TestVanillaReportValidationAndPersistence(t *testing.T) {
	dir := t.TempDir()
	store, err := reports.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Deps{Reports: store})
	if err != nil {
		t.Fatal(err)
	}
	s := g.sessions.Open(session.Client, steamauth.MinIndividual+1, nil)
	g.ClientConnected(s, true)
	send := func(target uint32, reason gcpb.CMsgGC_ReportPlayer_EReason) {
		t.Helper()
		raw, err := wire.Encode(uint32(gcpb.ETFGCMsg_k_EMsgGC_ReportPlayer),
			&gcpb.CMsgGC_ReportPlayer{AccountIdTarget: proto.Uint32(target), Reason: reason.Enum()}, wire.NoJob)
		if err != nil {
			t.Fatal(err)
		}
		g.Handle(s, raw)
	}
	send(1, gcpb.CMsgGC_ReportPlayer_kReason_CHEATING)
	send(2, gcpb.CMsgGC_ReportPlayer_EReason(999))
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 0 {
		t.Fatal("accepted self-report or invalid reason")
	}
	send(2, gcpb.CMsgGC_ReportPlayer_kReason_CHEATING)
	send(2, gcpb.CMsgGC_ReportPlayer_kReason_CHEATING)
	files, _ = filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("wanted one report, got %d", len(files))
	}
	raw, _ := os.ReadFile(files[0])
	if len(raw) == 0 {
		t.Fatal("report file is empty")
	}
}
