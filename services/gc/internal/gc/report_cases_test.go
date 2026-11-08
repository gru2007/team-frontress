package gc

import (
	"bytes"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/anticheat"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/reports"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// Reports and Easy Anti-Cheat's verdicts add up to one case; a party counts
// once; an EAC ban closes the case and tells the reporters.
func TestReportCaseCombinesReportsWithEasyAntiCheat(t *testing.T) {
	rs, err := reports.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ac, err := anticheat.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Deps{Reports: rs, AntiCheat: ac})
	if err != nil {
		t.Fatal(err)
	}
	official := g.sessions.Open(session.Server, 90071992547409921, func(s *session.Session) {
		s.ServerName = "official"
		s.AntiCheatAuthority = true
	})
	g.ServerConnected(official)

	const cheater = steamauth.MinIndividual + 100
	a := g.sessions.Open(session.Client, steamauth.MinIndividual+1, nil)
	b := g.sessions.Open(session.Client, steamauth.MinIndividual+2, nil)
	c := g.sessions.Open(session.Client, steamauth.MinIndividual+3, nil)
	for _, s := range []*session.Session{a, b, c} {
		g.ClientConnected(s, true)
	}
	party := g.parties[g.player(a.SteamID).PartyID]
	if err := g.joinParty(party, b.SteamID); err != nil {
		t.Fatal(err)
	}

	report := func(s *session.Session, detail uint32, killed uint32) {
		t.Helper()
		raw, err := wire.Encode(uint32(gcpb.ETFGCMsg_k_EMsgGC_ReportPlayer), &gcpb.CMsgGC_ReportPlayer{
			AccountIdTarget: proto.Uint32(uint32(cheater - steamauth.MinIndividual)),
			Reason:          gcpb.CMsgGC_ReportPlayer_kReason_CHEATING.Enum(),
			Detail:          proto.Uint32(detail),
			Comment:         proto.String("  snaps   to heads\nthrough walls "),
			KilledByTarget:  proto.Uint32(4),
			ServerAddress:   proto.String("203.0.113.5:27015"),
			Map:             proto.String("cp_granary"),
			EacForwarded:    proto.Bool(true),
		}, wire.NoJob)
		if err != nil {
			t.Fatal(err)
		}
		g.Handle(s, raw)
	}
	report(a, reports.DetailAimbot|reports.DetailChat, 4) // chat is not a cheat: dropped
	report(b, reports.DetailWallhack, 2)
	report(c, reports.DetailAimbot, 0)

	cs := g.ReportCases("open", 0)
	if len(cs) != 1 {
		t.Fatalf("cases: %+v", cs)
	}
	before := cs[0]
	if before.Open != 3 || before.Reporters != 2 || before.Details["aimbot"] != 2 || before.Details["chat"] != 0 {
		t.Fatalf("a party did not count once, or details leaked: %+v", before)
	}
	if !hasFlag(before, "never_protected") || !hasFlag(before, "killed_reporters") {
		t.Fatalf("flags: %v", before.Flags)
	}

	acEvent := func(ev gcpb.CMsgGC_Frontress_AntiCheatEvent_EEvent) {
		t.Helper()
		raw, err := wire.Encode(uint32(gcpb.ETFGCMsg_k_EMsgGC_Frontress_AntiCheatEvent), &gcpb.CMsgGC_Frontress_AntiCheatEvent{
			SteamId: proto.Uint64(cheater), ProductUserId: proto.String("0002feed"), Event: ev.Enum(), Enforced: proto.Bool(true),
		}, wire.NoJob)
		if err != nil {
			t.Fatal(err)
		}
		g.Handle(official, raw)
	}
	acEvent(gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_VERIFIED)
	acEvent(gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_VOTE_KICKED)
	acEvent(gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_VIOLATION)
	mid := g.ReportCases("open", 0)[0]
	if mid.Score < before.Score+9 || !hasFlag(mid, "eac_violation") || !hasFlag(mid, "vote_kicked") || hasFlag(mid, "never_protected") {
		t.Fatalf("EAC evidence did not raise the case: %.1f -> %.1f %v", before.Score, mid.Score, mid.Flags)
	}
	if mid.Severity != "critical" || mid.EAC.ProductUserID != "0002feed" {
		t.Fatalf("severity %s, eac %+v", mid.Severity, mid.EAC)
	}

	detail, ok := g.ReportCaseDetail(cheater)
	if !ok || len(detail.Reports) != 3 || detail.Reports[0].Comment != "snaps to heads through walls" || !detail.Reports[0].EAC {
		t.Fatalf("detail: %+v", detail.Reports)
	}

	drain(t, a)
	acEvent(gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_PERM_BANNED)
	if len(g.ReportCases("open", 0)) != 0 {
		t.Fatal("an EAC ban did not close the case")
	}
	closed := g.ReportCases("closed", 0)
	if len(closed) != 1 || closed[0].Status != reports.OutcomeActioned || closed[0].Resolution.By != "eac:official" {
		t.Fatalf("closed: %+v", closed)
	}
	if !sawEvent(t, a, "report_feedback") {
		t.Fatal("the reporter was not told")
	}
	if rs.Standing(lobbyKey(a.SteamID)).Actioned != 1 {
		t.Fatal("reporter got no credit")
	}

	// Dismissing a later case.
	g.now = func() time.Time { return time.Now().Add(time.Hour) }
	report(c, reports.DetailMovement, 0)
	if err := g.DismissReportCase(cheater, "admin:1", "demo shows nothing"); err != nil {
		t.Fatal(err)
	}
	if err := g.DismissReportCase(cheater, "admin:1", "again"); err == nil {
		t.Fatal("dismissed a case with nothing open")
	}
}

func hasFlag(c ReportCase, f string) bool {
	for _, x := range c.Flags {
		if x == f {
			return true
		}
	}
	return false
}

func drain(t *testing.T, s *session.Session) uint64 {
	t.Helper()
	frames, err := s.Poll(0, 0, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) == 0 {
		return 0
	}
	last := frames[len(frames)-1].Seq
	if _, err := s.Poll(last, 0, time.Now); err != nil {
		t.Fatal(err)
	}
	return last
}

func sawEvent(t *testing.T, s *session.Session, kind string) bool {
	t.Helper()
	frames, err := s.Poll(0, 0, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frames {
		if bytes.Contains(f.Data, []byte(`"t":"`+kind+`"`)) {
			return true
		}
	}
	return false
}
