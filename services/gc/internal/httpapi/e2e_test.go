package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gc"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/httpapi"
	"github.com/gru2007/team-frontress/services/gc/internal/socache"
	"github.com/gru2007/team-frontress/services/gc/internal/state"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

const serverKey = "test-server-key-0123456789"

// peer is a fake game process speaking the transport, the way
// frontress_gc.cpp does: it keeps a shared object cache and answers nothing
// on its own.
type peer struct {
	t       *testing.T
	base    string
	steamID uint64
	session string
	outSeq  uint64
	ack     uint64

	mu    sync.Mutex
	cache map[int32]map[string][]byte // type -> key -> data
	inbox []*wire.Msg
}

func newPeer(t *testing.T, base string, steamID uint64) *peer {
	return &peer{t: t, base: base, steamID: steamID, cache: map[int32]map[string][]byte{}}
}

func (p *peer) connect(form url.Values) {
	p.t.Helper()
	form.Set("protocol", strconv.Itoa(httpapi.ProtocolVersion))
	form.Set("steam_id", strconv.FormatUint(p.steamID, 10))
	resp, err := http.PostForm(p.base+"/gc/v1/session", form)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		p.t.Fatalf("session: HTTP %d: %s", resp.StatusCode, body)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(line, "session="); ok {
			p.session = v
		}
	}
	if p.session == "" {
		p.t.Fatalf("no session in %q", body)
	}
}

func (p *peer) client() { p.connect(url.Values{"kind": {"client"}}) }

func (p *peer) server() {
	p.connect(url.Values{"kind": {"server"}, "key": {serverKey}, "connect": {"127.0.0.1:27015"}})
}

// send sends one message, with a job id so a reply can find it.
func (p *peer) send(msgType uint32, body proto.Message) {
	p.t.Helper()
	data, err := wire.Encode(msgType, body, wire.NoJob)
	if err != nil {
		p.t.Fatal(err)
	}
	// Give it a source job, as a reliable message would have.
	m, _ := wire.Decode(data)
	m.Header.JobIdSource = proto.Uint64(4242)
	hdr, _ := proto.Marshal(m.Header)
	raw := append([]byte{}, data[:4]...)
	raw = append(raw, byte(len(hdr)), byte(len(hdr)>>8), byte(len(hdr)>>16), byte(len(hdr)>>24))
	raw = append(raw, hdr...)
	raw = append(raw, m.Body...)

	p.outSeq++
	batch := wire.AppendFrame(nil, wire.Frame{Seq: p.outSeq, Type: msgType | wire.ProtoFlag, Data: raw})
	req, _ := http.NewRequest(http.MethodPost, p.base+"/gc/v1/send", bytes.NewReader(batch))
	req.Header.Set("X-GC-Session", p.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		p.t.Fatalf("send: HTTP %d", resp.StatusCode)
	}
}

// pump receives once, applying shared object messages to the fake cache.
func (p *peer) pump(wait time.Duration) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		p.base+"/gc/v1/recv?ack="+strconv.FormatUint(p.ack, 10), nil)
	req.Header.Set("X-GC-Session", p.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return // timed out: nothing arrived
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return
	}
	body, _ := io.ReadAll(resp.Body)
	frames, err := wire.ParseBatch(body)
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range frames {
		if f.Seq <= p.ack {
			continue
		}
		p.ack = f.Seq
		m, err := wire.Decode(f.Data)
		if err != nil {
			p.t.Fatal(err)
		}
		switch gcpb.ESOMsg(m.Type) {
		case gcpb.ESOMsg_k_ESOMsg_Create, gcpb.ESOMsg_k_ESOMsg_Update, gcpb.ESOMsg_k_ESOMsg_Destroy:
			var so gcpb.CMsgSOSingleObject
			if err := proto.Unmarshal(m.Body, &so); err != nil {
				p.t.Fatal(err)
			}
			key := soKey(so.GetTypeId(), so.GetObjectData())
			if p.cache[so.GetTypeId()] == nil {
				p.cache[so.GetTypeId()] = map[string][]byte{}
			}
			if gcpb.ESOMsg(m.Type) == gcpb.ESOMsg_k_ESOMsg_Destroy {
				delete(p.cache[so.GetTypeId()], key)
			} else {
				p.cache[so.GetTypeId()][key] = so.GetObjectData()
			}
		default:
			p.inbox = append(p.inbox, m)
		}
	}
}

func soKey(typ int32, data []byte) string {
	switch typ {
	case socache.TypeParty:
		var x gcpb.CSOTFParty
		_ = proto.Unmarshal(data, &x)
		return strconv.FormatUint(x.GetPartyId(), 10)
	case socache.TypeLobby:
		var x gcpb.CSOTFGameServerLobby
		_ = proto.Unmarshal(data, &x)
		return strconv.FormatUint(x.GetLobbyId(), 10)
	case socache.TypePartyInvite:
		var x gcpb.CSOTFPartyInvite
		_ = proto.Unmarshal(data, &x)
		return strconv.FormatUint(x.GetGroupId(), 10)
	case socache.TypeRatingData:
		var x gcpb.CSOTFRatingData
		_ = proto.Unmarshal(data, &x)
		return strconv.Itoa(int(x.GetRatingType()))
	}
	return ""
}

func (p *peer) party() *gcpb.CSOTFParty {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, data := range p.cache[socache.TypeParty] {
		var x gcpb.CSOTFParty
		_ = proto.Unmarshal(data, &x)
		return &x
	}
	return nil
}

func (p *peer) lobby() *gcpb.CSOTFGameServerLobby {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, data := range p.cache[socache.TypeLobby] {
		var x gcpb.CSOTFGameServerLobby
		_ = proto.Unmarshal(data, &x)
		return &x
	}
	return nil
}

func (p *peer) invites() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cache[socache.TypePartyInvite])
}

// replied reports whether a reply of a type arrived for our job.
func (p *peer) replied(msgType uint32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.inbox {
		if m.Type == msgType && m.Header.GetJobIdTarget() == 4242 {
			return true
		}
	}
	return false
}

// until pumps until cond holds or the deadline passes.
func (p *peer) until(what string, cond func() bool) {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			p.t.Fatalf("timed out waiting for %s", what)
		}
		p.pump(200 * time.Millisecond)
	}
}

func testConfig() config.Config {
	cfg := config.Config{
		Name:       "test",
		ServerKeys: []config.ServerKey{{Name: "test", Key: serverKey, Community: "frontress"}},
		MatchGroups: []config.MatchGroupConfig{{
			MatchGroup: 7, Name: "Casual", Enabled: true, Mode: config.ModeFrontline,
			MinPlayers: 2, IdealPlayers: 2, MaxPlayers: 4, Maps: []string{"koth_viaduct"},
		}},
		Timing: config.TimingConfig{TickMillis: 20, PollSecs: 1},
	}
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return cfg
}

func startGC(t *testing.T, store *state.Store) (*gc.GC, string, func()) {
	return startGCWith(t, testConfig(), store)
}

func startGCWith(t *testing.T, cfg config.Config, store *state.Store) (*gc.GC, string, func()) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	g, err := gc.New(gc.Deps{Config: cfg, Log: log, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx); close(done) }()
	srv := httptest.NewServer(httpapi.New(g, steamauth.Dev{}, log).Handler())
	return g, srv.URL, func() {
		srv.Close()
		cancel()
		<-done
	}
}

const (
	alice = steamauth.MinIndividual + 1
	bob   = steamauth.MinIndividual + 2
	carol = steamauth.MinIndividual + 3
	gsID  = uint64(90000000000000001)
)

func msg[T ~int32](v T) uint32 { return uint32(v) }

func TestMatchEndToEnd(t *testing.T) {
	_, base, stop := startGC(t, nil)
	defer stop()

	// A dedicated server joins the pool and says it is empty.
	gs := newPeer(t, base, gsID)
	gs.server()
	gs.send(msg(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus), &gcpb.CMsgGameServerMatchmakingStatus{
		MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_EMPTY.Enum(),
		Map:              proto.String("itemtest"),
	})

	a, b := newPeer(t, base, alice), newPeer(t, base, bob)
	a.client()
	b.client()
	a.until("alice's party", func() bool { return a.party() != nil })
	b.until("bob's party", func() bool { return b.party() != nil })

	// Alice invites Bob; Bob accepts by asking to join.
	a.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_InvitePlayer), &gcpb.CMsgPartyInvitePlayer{PlayerId: proto.Uint64(bob)})
	b.until("an invite", func() bool { return b.invites() == 1 })
	b.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_RequestJoinPlayer), &gcpb.CMsgPartyRequestJoinPlayer{JoinPlayerId: proto.Uint64(alice)})
	a.until("a party of two", func() bool { return len(a.party().GetMemberIds()) == 2 })
	b.until("being in alice's party", func() bool {
		p := b.party()
		return p != nil && p.GetLeaderId() == alice && b.invites() == 0
	})

	// Alice queues the party: two players is a whole 1v1 here... but a party
	// is never split, so a party of two cannot make a match on its own.
	a.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch), &gcpb.CMsgPartyQueueForMatch{
		MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
	})
	a.until("the queue reply", func() bool { return a.replied(msg(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatchResponse)) })
	a.until("being queued", func() bool { return len(a.party().GetMatchmakingQueues()) == 1 })

	// Carol queues alone; three seats, so the GC makes a 1v1 out of... no:
	// the party of two must stay together, so it is two against one is not
	// even. It waits for a fourth.
	c := newPeer(t, base, carol)
	c.client()
	c.until("carol's party", func() bool { return c.party() != nil })
	c.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch), &gcpb.CMsgPartyQueueForMatch{
		MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
	})
	time.Sleep(200 * time.Millisecond)
	c.pump(100 * time.Millisecond)
	if c.lobby() != nil {
		t.Fatal("an uneven match was formed")
	}

	// Carol leaves the queue; Alice's party leaves; Bob goes solo and the two
	// solos make a 1v1.
	c.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_RemoveFromQueue), &gcpb.CMsgPartyRemoveFromQueue{
		MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
	})
	b.send(msg(gcpb.ETFGCMsg_k_EMsgGCExitMatchmaking), &gcpb.CMsgExitMatchmaking{PartyId: proto.Uint64(a.party().GetPartyId())})
	b.until("a party of his own", func() bool { p := b.party(); return p != nil && p.GetLeaderId() == bob })
	a.until("alice alone and out of the queue", func() bool {
		p := a.party()
		return len(p.GetMemberIds()) == 1 && len(p.GetMatchmakingQueues()) == 0
	})

	for _, p := range []*peer{a, b} {
		p.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch), &gcpb.CMsgPartyQueueForMatch{
			MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
		})
	}

	// The match forms and the server gets it in setup.
	gs.until("the server's lobby", func() bool { return gs.lobby() != nil })
	l := gs.lobby()
	if l.GetState() != gcpb.CSOTFGameServerLobby_SERVERSETUP || l.GetMapName() != "koth_viaduct" || len(l.GetMembers()) != 2 {
		t.Fatalf("server got %v", l)
	}
	a.until("alice's lobby", func() bool { return a.lobby() != nil })
	if a.lobby().GetConnect() != "" {
		t.Fatal("players were told where to connect before the server was ready")
	}

	// The server loads the map and acknowledges the players: the match goes
	// live and the players are told where to connect.
	gs.send(msg(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus), &gcpb.CMsgGameServerMatchmakingStatus{
		MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_ACTIVE_MATCH.Enum(),
		Map:              proto.String("koth_viaduct"),
		Players: []*gcpb.CMsgGameServerMatchmakingStatus_Player{
			{SteamId: proto.Uint64(alice), ConnectState: gcpb.CMsgGameServerMatchmakingStatus_RESERVED.Enum()},
			{SteamId: proto.Uint64(bob), ConnectState: gcpb.CMsgGameServerMatchmakingStatus_CONNECTED.Enum()},
		},
	})
	a.until("the match going live", func() bool {
		l := a.lobby()
		return l != nil && l.GetState() == gcpb.CSOTFGameServerLobby_RUN && l.GetConnect() == "127.0.0.1:27015"
	})

	// The server reports the result and closes the lobby.
	matchID := gs.lobby().GetMatchId()
	lobbyID := gs.lobby().GetLobbyId()
	var aliceGameTeam, bobGameTeam uint32 = 2, 3
	for _, m := range a.lobby().GetMembers() {
		if m.GetId() == alice && m.GetTeam() == gcpb.TF_GC_TEAM_TF_GC_TEAM_INVADERS {
			aliceGameTeam, bobGameTeam = 3, 2
		}
	}
	gs.send(msg(gcpb.ETFGCMsg_k_EMsgGC_Match_Result), &gcpb.CMsgGC_Match_Result{
		MatchId:     proto.Uint64(matchID),
		WinningTeam: proto.Uint32(aliceGameTeam),
		Players: []*gcpb.CMsgGC_Match_Result_Player{
			{SteamId: proto.Uint64(alice), Team: proto.Uint32(aliceGameTeam)},
			{SteamId: proto.Uint64(bob), Team: proto.Uint32(bobGameTeam)},
		},
	})
	gs.until("the result reply", func() bool { return gs.replied(msg(gcpb.ETFGCMsg_k_EMsgGC_Match_ResultResponse)) })
	gs.send(msg(gcpb.ETFGCMsg_k_EMsgGCGameServerKickingLobby), &gcpb.CMsgGameServerKickingLobby{
		LobbyId: proto.Uint64(lobbyID), MatchId: proto.Uint64(matchID),
	})
	gs.until("the lobby gone from the server", func() bool { return gs.lobby() == nil })
	a.until("the lobby gone from alice", func() bool { return a.lobby() == nil })

	resp, err := http.Get(base + "/v1/player/" + strconv.FormatUint(alice, 10))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"wins":1`) || !strings.Contains(string(body), `"rating":1516`) {
		t.Fatalf("alice's record: %s", body)
	}
}

// Without a free server nobody is matched: the party stays queued, and the
// match forms the moment a server joins the pool.
func TestMatchWaitsForAFreeServer(t *testing.T) {
	_, base, stop := startGC(t, nil)
	defer stop()

	a, b := newPeer(t, base, alice), newPeer(t, base, bob)
	a.client()
	b.client()
	for _, p := range []*peer{a, b} {
		p.until("a party", func() bool { return p.party() != nil })
		p.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch), &gcpb.CMsgPartyQueueForMatch{
			MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
		})
	}
	a.until("queued", func() bool { return len(a.party().GetMatchmakingQueues()) == 1 })
	time.Sleep(300 * time.Millisecond)
	a.pump(100 * time.Millisecond)
	if a.lobby() != nil {
		t.Fatal("a match formed with no server to play it")
	}

	gs := newPeer(t, base, gsID)
	gs.server()
	gs.send(msg(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus), &gcpb.CMsgGameServerMatchmakingStatus{
		MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_EMPTY.Enum(),
	})
	gs.until("the server's lobby", func() bool { return gs.lobby() != nil })
	a.until("alice's lobby", func() bool { return a.lobby() != nil })
}

// A restarted GC picks running matches back up, and a reconnecting server is
// handed its lobby again.
func TestRestartKeepsRunningMatch(t *testing.T) {
	store := state.Memory()
	_, base, stop := startGC(t, store)

	gs := newPeer(t, base, gsID)
	gs.server()
	gs.send(msg(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus), &gcpb.CMsgGameServerMatchmakingStatus{
		MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_EMPTY.Enum(),
	})
	a, b := newPeer(t, base, alice), newPeer(t, base, bob)
	a.client()
	b.client()
	for _, p := range []*peer{a, b} {
		p.until("a party", func() bool { return p.party() != nil })
		p.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch), &gcpb.CMsgPartyQueueForMatch{
			MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
		})
	}
	gs.until("the lobby", func() bool { return gs.lobby() != nil })
	gs.send(msg(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus), &gcpb.CMsgGameServerMatchmakingStatus{
		MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_ACTIVE_MATCH.Enum(),
		Map:              proto.String("koth_viaduct"),
	})
	a.until("live", func() bool { l := a.lobby(); return l != nil && l.GetState() == gcpb.CSOTFGameServerLobby_RUN })
	lobbyID := a.lobby().GetLobbyId()
	stop()

	_, base2, stop2 := startGC(t, store)
	defer stop2()
	gs2 := newPeer(t, base2, gsID)
	gs2.server()
	gs2.until("the lobby back", func() bool { l := gs2.lobby(); return l != nil && l.GetLobbyId() == lobbyID })
	a2 := newPeer(t, base2, alice)
	a2.client()
	a2.until("alice's lobby back", func() bool {
		l := a2.lobby()
		return l != nil && l.GetLobbyId() == lobbyID && l.GetState() == gcpb.CSOTFGameServerLobby_RUN
	})
}

// A batch that is sent twice is processed once.
func TestDuplicateBatchIsIgnored(t *testing.T) {
	_, base, stop := startGC(t, nil)
	defer stop()
	a := newPeer(t, base, alice)
	a.client()
	a.until("a party", func() bool { return a.party() != nil })
	a.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch), &gcpb.CMsgPartyQueueForMatch{
		MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
	})
	a.until("queued", func() bool { return len(a.party().GetMatchmakingQueues()) == 1 })
	a.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_RemoveFromQueue), &gcpb.CMsgPartyRemoveFromQueue{
		MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
	})
	a.until("unqueued", func() bool { return len(a.party().GetMatchmakingQueues()) == 0 })

	// Replay the queue request with its old sequence number: it must not
	// put the party back in the queue.
	a.outSeq = 0
	a.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch), &gcpb.CMsgPartyQueueForMatch{
		MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
	})
	time.Sleep(150 * time.Millisecond)
	a.pump(100 * time.Millisecond)
	if n := len(a.party().GetMatchmakingQueues()); n != 0 {
		t.Fatalf("a replayed request was processed again (%d queues)", n)
	}
}

// A match whose server never brings it up is abandoned and its players go
// back to the queue, instead of being held forever.
func TestSetupTimesOutAndRequeues(t *testing.T) {
	cfg := testConfig()
	cfg.Timing.ServerBootSecs = 1
	_, base, stop := startGCWith(t, cfg, nil)
	defer stop()

	// A server that takes the match and then never starts it.
	gs := newPeer(t, base, gsID)
	gs.server()
	gs.send(msg(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus), &gcpb.CMsgGameServerMatchmakingStatus{
		MatchmakingState: gcpb.ServerMatchmakingState_ServerMatchmakingState_EMPTY.Enum(),
	})
	a, b := newPeer(t, base, alice), newPeer(t, base, bob)
	a.client()
	b.client()
	for _, p := range []*peer{a, b} {
		p.until("a party", func() bool { return p.party() != nil })
		p.send(msg(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch), &gcpb.CMsgPartyQueueForMatch{
			MatchGroup: gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12.Enum(),
		})
	}
	a.until("a lobby", func() bool { return a.lobby() != nil })
	first := a.lobby().GetLobbyId()
	a.until("the lobby to time out", func() bool { return a.lobby() == nil || a.lobby().GetLobbyId() != first })
	a.until("being queued again or rematched", func() bool {
		return len(a.party().GetMatchmakingQueues()) == 1 || (a.lobby() != nil && a.lobby().GetLobbyId() != first)
	})
}
