package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/greyline-frontress/coordinator/internal/mm"
	"github.com/greyline-frontress/coordinator/internal/pool"
	"github.com/greyline-frontress/coordinator/internal/steam"
	"github.com/greyline-frontress/coordinator/internal/war"
)

const testPoolKey = "test-pool-key"

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	maker  *mm.Matchmaker
	engine *war.Engine
	pool   *pool.Pool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	theater, err := war.LoadTheater(filepath.Join("..", "..", "theater.industrial.json"))
	if err != nil {
		t.Fatalf("theater: %v", err)
	}
	log, err := war.OpenLog(filepath.Join(t.TempDir(), "war.jsonl"))
	if err != nil {
		t.Fatalf("war log: %v", err)
	}
	engine, err := war.NewEngine(theater, log, war.DefaultRules())
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	servers := pool.New(45 * time.Second)
	cfg := mm.DefaultConfig()
	cfg.MinTeamSize = 2
	cfg.TeamSizes = []int{2}
	cfg.FormWait = 0
	maker := mm.New(cfg, slog.New(slog.DiscardHandler), engine, servers)

	api := New(slog.New(slog.DiscardHandler), steam.DevAuthenticator{}, engine, maker, servers, Options{
		PoolKey:         testPoolKey,
		MaxPollWait:     time.Second,
		ProtocolVersion: 3,
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv, maker: maker, engine: engine, pool: servers}
}

func (h *harness) do(method, path, token string, body any, into any) int {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rdr)
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if into != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil && err != io.EOF {
			h.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

// client is a fake game client.
type client struct {
	h       *harness
	steamID uint64
	token   string
	seq     uint64
}

func (h *harness) newClient(steamID uint64, side string) *client {
	h.t.Helper()
	var resp helloResponse
	code := h.do("POST", "/api/v1/client/hello", "", map[string]any{
		"steam_id": fmt.Sprintf("%d", steamID), "name": fmt.Sprintf("merc%d", steamID), "side": side,
		"protocol_version": 3,
	}, &resp)
	if code != http.StatusOK {
		h.t.Fatalf("hello for %d: status %d", steamID, code)
	}
	if resp.Token == "" {
		h.t.Fatal("hello returned no token")
	}
	return &client{h: h, steamID: steamID, token: resp.Token}
}

func (c *client) deploy() {
	c.h.t.Helper()
	var out map[string]any
	if code := c.h.do("POST", "/api/v1/client/deploy", c.token,
		map[string]any{"accept_contract": true}, &out); code != http.StatusOK {
		c.h.t.Fatalf("deploy for %d: status %d (%v)", c.steamID, code, out)
	}
}

func (c *client) deployWithParty(partyID string) {
	c.h.t.Helper()
	var out map[string]any
	if code := c.h.do("POST", "/api/v1/client/deploy", c.token,
		map[string]any{"accept_contract": true, "party_id": partyID}, &out); code != http.StatusOK {
		c.h.t.Fatalf("deploy for %d: status %d (%v)", c.steamID, code, out)
	}
}

// poll drains whatever the client has been told since last time.
func (c *client) poll() []mm.Event {
	c.h.t.Helper()
	var out struct {
		Events []mm.Event `json:"events"`
		Seq    uint64     `json:"seq"`
	}
	path := fmt.Sprintf("/api/v1/client/poll?since=%d&wait=0", c.seq)
	if code := c.h.do("GET", path, c.token, nil, &out); code != http.StatusOK {
		c.h.t.Fatalf("poll for %d: status %d", c.steamID, code)
	}
	c.seq = out.Seq
	return out.Events
}

func (c *client) waitFor(kind mm.EventType) mm.Event {
	c.h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range c.poll() {
			if ev.Type == kind {
				return ev
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.h.t.Fatalf("client %d never received a %s event", c.steamID, kind)
	return mm.Event{}
}

// agent is a fake dedicated server agent.
type agent struct {
	h     *harness
	id    string
	token string
}

// newHostCapableClient is a client whose hello reports enough upload, CPU and
// memory to clear every floor in mm.DefaultConfig's HostRequirements, so it is
// a deterministic, eligible host-election candidate.
func (h *harness) newHostCapableClient(steamID uint64, side string) *client {
	h.t.Helper()
	var resp helloResponse
	code := h.do("POST", "/api/v1/client/hello", "", map[string]any{
		"steam_id": fmt.Sprintf("%d", steamID), "name": fmt.Sprintf("merc%d", steamID), "side": side,
		"protocol_version": 3,
		"host_capable":     true, "upload_kbps": 5000, "cpu_score": 80, "memory_mb": 8192,
	}, &resp)
	if code != http.StatusOK {
		h.t.Fatalf("hello for %d: status %d", steamID, code)
	}
	return &client{h: h, steamID: steamID, token: resp.Token}
}

func (c *client) hostRegister(matchID string) (serverID, serverToken string) {
	c.h.t.Helper()
	var out struct {
		ServerID    string `json:"server_id"`
		ServerToken string `json:"server_token"`
	}
	code := c.h.do("POST", "/api/v1/client/host-register", c.token,
		map[string]any{"match_id": matchID}, &out)
	if code != http.StatusOK {
		c.h.t.Fatalf("host-register for %d: status %d", c.steamID, code)
	}
	return out.ServerID, out.ServerToken
}

func (c *client) resultEvidence(matchID, outcome string, red, blu uint32, violation bool, reason string) map[string]any {
	c.h.t.Helper()
	var out map[string]any
	code := c.h.do("POST", "/api/v1/client/result-evidence", c.token, map[string]any{
		"match_id": matchID, "outcome": outcome, "red_score": red, "blu_score": blu,
		"policy_violation": violation, "violation_reason": reason,
	}, &out)
	if code != http.StatusOK {
		c.h.t.Fatalf("result-evidence for %d: status %d (%v)", c.steamID, code, out)
	}
	return out
}

// agentFromToken wraps an already-issued server id/token — what a P2P host
// gets back from host-register — in the same fake-agent helper a dedicated
// server test uses, since both speak the identical /api/v1/servers/* protocol
// from here on.
func (h *harness) agentFromToken(id, token string) *agent {
	return &agent{h: h, id: id, token: token}
}

func (a *agent) setAddress(addr string) {
	a.h.t.Helper()
	var out map[string]any
	if code := a.h.do("POST", "/api/v1/servers/address?server_id="+a.id, a.token,
		serverAddressRequest{ConnectAddress: addr}, &out); code != http.StatusOK {
		a.h.t.Fatalf("server address: status %d (%v)", code, out)
	}
}

func (h *harness) newAgent(name, connect string) *agent {
	h.t.Helper()
	var resp struct {
		ServerID    string `json:"server_id"`
		ServerToken string `json:"server_token"`
	}
	code := h.do("POST", "/api/v1/servers/register", testPoolKey, pool.Registration{
		Name: name, ConnectAddress: connect, Capacity: 24, AgentVersion: "test",
	}, &resp)
	if code != http.StatusOK {
		h.t.Fatalf("register: status %d", code)
	}
	return &agent{h: h, id: resp.ServerID, token: resp.ServerToken}
}

func (a *agent) poll() (pool.Command, bool) {
	a.h.t.Helper()
	var cmd pool.Command
	code := a.h.do("GET", "/api/v1/servers/poll?wait=1&server_id="+a.id, a.token, nil, &cmd)
	switch code {
	case http.StatusNoContent:
		return pool.Command{}, false
	case http.StatusOK:
		return cmd, true
	default:
		a.h.t.Fatalf("server poll: status %d", code)
		return pool.Command{}, false
	}
}

func (a *agent) state(matchID, state string) {
	a.h.t.Helper()
	var out map[string]any
	if code := a.h.do("POST", "/api/v1/servers/state?server_id="+a.id, a.token,
		serverStateRequest{MatchID: matchID, State: state}, &out); code != http.StatusOK {
		a.h.t.Fatalf("server state %s: status %d (%v)", state, code, out)
	}
}

func (a *agent) result(matchID, outcome string, red, blu uint32) map[string]any {
	a.h.t.Helper()
	var out map[string]any
	if code := a.h.do("POST", "/api/v1/servers/result?server_id="+a.id, a.token,
		serverResultRequest{MatchID: matchID, Outcome: outcome, RedScore: red, BluScore: blu},
		&out); code != http.StatusOK {
		a.h.t.Fatalf("server result: status %d (%v)", code, out)
	}
	return out
}

// TestOneBattleMovesTheWar is the MVP acceptance test, end to end over HTTP:
// four people deploy, a dedicated server runs the battle it is given, and the
// front they were fighting on has moved by the time they are back on the map.
func TestSteamID64RoundTripsAsAString(t *testing.T) {
	h := newHarness(t)
	const steamID uint64 = 76561198317961869
	c := h.newClient(steamID, "RED")
	var self struct {
		Player mm.PlayerView `json:"player"`
	}
	if code := h.do("GET", "/api/v1/client/self", c.token, nil, &self); code != http.StatusOK {
		t.Fatalf("self: status %d", code)
	}
	if self.Player.SteamID != steamID {
		t.Fatalf("SteamID64 changed in JSON round trip: got %d, want %d", self.Player.SteamID, steamID)
	}
}

func TestHelloRequiresProtocolVersion(t *testing.T) {
	h := newHarness(t)
	var out map[string]any
	code := h.do("POST", "/api/v1/client/hello", "", map[string]any{
		"steam_id": "76561198317961869", "name": "old client", "side": "RED",
	}, &out)
	if code != http.StatusBadRequest {
		t.Fatalf("hello without protocol_version returned %d, want %d", code, http.StatusBadRequest)
	}
}

func TestOneBattleMovesTheWar(t *testing.T) {
	h := newHarness(t)
	server := h.newAgent("test-node", "10.0.0.5:27015")

	front := h.engine.ActiveFronts()[0]
	stageBefore := front.Stage

	clients := []*client{
		h.newClient(1, "RED"), h.newClient(2, "RED"),
		h.newClient(3, "BLU"), h.newClient(4, "BLU"),
	}
	for _, c := range clients {
		c.deploy()
	}

	h.maker.Tick()

	cmd, ok := server.poll()
	if !ok {
		t.Fatal("the server pool was never handed the battle")
	}
	if cmd.Type != pool.CommandAssign || cmd.Assignment == nil {
		t.Fatalf("server got %q, want an assignment", cmd.Type)
	}
	as := cmd.Assignment
	if as.Map == "" || len(as.Roster) != 4 {
		t.Fatalf("assignment is not a runnable battle: %+v", as)
	}
	if as.Password == "" {
		t.Fatal("assignment has no password; anybody could walk into the battle")
	}
	// The briefing is what the game server tells players in chat, so it has to
	// arrive with the assignment, not be looked up later.
	if as.Briefing.FrontID != front.ID || as.Briefing.StageKind == "" || as.Briefing.Reason == "" {
		t.Fatalf("assignment briefing is incomplete: %+v", as.Briefing)
	}
	if as.Briefing.Stage != stageBefore+1 || as.Briefing.StageCount < 1 {
		t.Fatalf("briefing says stage %d/%d, want %d/%d",
			as.Briefing.Stage, as.Briefing.StageCount, stageBefore+1, as.Briefing.StageCount)
	}
	red, blu := 0, 0
	for _, e := range as.Roster {
		switch e.Side {
		case "RED":
			red++
		case "BLU":
			blu++
		}
		if e.Team != "RED" && e.Team != "BLU" {
			t.Fatalf("roster entry has no in-game team: %+v", e)
		}
	}
	if red != 2 || blu != 2 {
		t.Fatalf("battle formed %dv%d, want 2v2", red, blu)
	}

	server.state(as.MatchID, "ready")

	// Every player is told where to go, and which part of the war they are in.
	for _, c := range clients {
		ev := c.waitFor(mm.EventMatchReady)
		if ev.Match == nil || ev.Match.Connect != "10.0.0.5:27015" {
			t.Fatalf("client %d was not sent to the server: %+v", c.steamID, ev.Match)
		}
		if ev.Match.Password != as.Password {
			t.Fatalf("client %d got the wrong password", c.steamID)
		}
		if ev.Match.FrontName == "" || ev.Match.Reason == "" {
			t.Fatalf("client %d was not told why this battle exists: %+v", c.steamID, ev.Match)
		}
	}

	server.state(as.MatchID, "live")

	// The scoreboard said the RED team won. Which side of the war that is
	// depends on the roster, because the attacking side plays as BLU: reading
	// "RED_WIN" as "the war's RED side won" is exactly the mistake the
	// translation exists to prevent, so the test works it out the same way the
	// coordinator does.
	winner := war.SideNone
	for _, e := range as.Roster {
		if e.Team == "RED" {
			winner, _ = war.ParseSide(e.Side)
			break
		}
	}
	if winner == war.SideNone {
		t.Fatal("no roster entry plays as the in-game RED team")
	}

	out := server.result(as.MatchID, "RED_WIN", 3, 1)
	if out["war_update"] == nil {
		t.Fatalf("the result did not move the war: %v", out)
	}

	// The front is where the war continues, and it moved.
	after, stillOpen := h.engine.Front(front.ID)
	if stillOpen && after.Stage == stageBefore && after.Attacker == winner {
		t.Fatalf("%s won but the front is still at stage %d", winner, after.Stage)
	}

	// And the players are told, on the same screen they will press DEPLOY from.
	rosterSide := map[uint64]war.Side{}
	for _, e := range as.Roster {
		side, _ := war.ParseSide(e.Side)
		rosterSide[e.SteamID] = side
	}
	for _, c := range clients {
		ev := c.waitFor(mm.EventMatchOver)
		if ev.Over == nil || !ev.Over.Counted {
			t.Fatalf("client %d was not told the battle counted: %+v", c.steamID, ev.Over)
		}
		if ev.Over.Update == nil || ev.Over.Update.Headline == "" {
			t.Fatalf("client %d got no war headline: %+v", c.steamID, ev.Over)
		}
		if want := rosterSide[c.steamID] == winner; ev.Over.Won != want {
			t.Errorf("client %d fights for %s, winner was %s: Won=%v, want %v",
				c.steamID, rosterSide[c.steamID], winner, ev.Over.Won, want)
		}
	}

	// The server is free again for the next battle.
	if s, _ := h.pool.Get(server.id); !s.Free() {
		t.Fatalf("server is %s after reporting a result, want idle", s.Status)
	}
	if s, _ := h.pool.Get(server.id); s.Hosted != 1 {
		t.Fatalf("server hosted count = %d, want 1", s.Hosted)
	}
}

func TestServerPoolRequiresTheKey(t *testing.T) {
	h := newHarness(t)
	var out map[string]any
	code := h.do("POST", "/api/v1/servers/register", "wrong-key", pool.Registration{
		Name: "impostor", ConnectAddress: "1.2.3.4:27015",
	}, &out)
	if code != http.StatusUnauthorized {
		t.Fatalf("registration with a bad key returned %d, want 401", code)
	}
	code = h.do("POST", "/api/v1/servers/register", "", pool.Registration{
		Name: "impostor", ConnectAddress: "1.2.3.4:27015",
	}, &out)
	if code != http.StatusUnauthorized {
		t.Fatalf("registration with no key returned %d, want 401", code)
	}
}

func TestResultsFromTheWrongServerAreRejected(t *testing.T) {
	h := newHarness(t)
	hosting := h.newAgent("host", "10.0.0.5:27015")
	other := h.newAgent("other", "10.0.0.6:27015")

	for i := uint64(1); i <= 4; i++ {
		h.newClient(i, "").deploy()
	}
	h.maker.Tick()
	cmd, ok := hosting.poll()
	if !ok {
		// Either server may have been picked; find out which.
		cmd, ok = other.poll()
		if !ok {
			t.Fatal("neither server was given the battle")
		}
		hosting, other = other, hosting
	}
	matchID := cmd.Assignment.MatchID

	var out map[string]any
	code := h.do("POST", "/api/v1/servers/result?server_id="+other.id, other.token,
		serverResultRequest{MatchID: matchID, Outcome: "BLU_WIN"}, &out)
	if code != http.StatusConflict {
		t.Fatalf("a server reported a battle it was not running and got %d, want 409", code)
	}

	before := h.engine.Revision()
	hosting.state(matchID, "live")
	hosting.result(matchID, "RED_WIN", 3, 0)
	// A retry of the same report must not move the war a second time.
	h.do("POST", "/api/v1/servers/result?server_id="+hosting.id, hosting.token,
		serverResultRequest{MatchID: matchID, Outcome: "RED_WIN", RedScore: 3}, &out)
	after := h.engine.Revision()
	if after <= before {
		t.Fatal("the first result did not move the war")
	}
	rev := h.engine.Revision()
	h.do("POST", "/api/v1/servers/result?server_id="+hosting.id, hosting.token,
		serverResultRequest{MatchID: matchID, Outcome: "RED_WIN", RedScore: 3}, &out)
	if h.engine.Revision() != rev {
		t.Fatal("a duplicate result moved the war twice")
	}
}

func TestClientNeedsASession(t *testing.T) {
	h := newHarness(t)
	var out map[string]any
	if code := h.do("POST", "/api/v1/client/deploy", "not-a-token", nil, &out); code != http.StatusUnauthorized {
		t.Fatalf("deploy without a session returned %d, want 401", code)
	}
	if code := h.do("GET", "/api/v1/client/poll", "", nil, &out); code != http.StatusUnauthorized {
		t.Fatalf("poll without a session returned %d, want 401", code)
	}
}

// The war map is public: a launcher, a website or a bot showing the front does
// not need a game session.
func TestWorldIsReadableWithoutASession(t *testing.T) {
	h := newHarness(t)
	var world struct {
		World      war.Snapshot `json:"world"`
		Population int          `json:"population"`
	}
	if code := h.do("GET", "/api/v1/world", "", nil, &world); code != http.StatusOK {
		t.Fatalf("world: status %d", code)
	}
	if len(world.World.Nodes) != 7 {
		t.Fatalf("world has %d nodes, want the theater's 7", len(world.World.Nodes))
	}
	if world.World.Campaign.Name == "" {
		t.Fatal("world snapshot has no campaign")
	}

	var fronts struct {
		Fronts []map[string]any `json:"fronts"`
	}
	if code := h.do("GET", "/api/v1/world/fronts", "", nil, &fronts); code != http.StatusOK {
		t.Fatalf("fronts: status %d", code)
	}
	if len(fronts.Fronts) == 0 {
		t.Fatal("no fronts to deploy to")
	}

	var timeline struct {
		Events []war.Event `json:"events"`
	}
	if code := h.do("GET", "/api/v1/world/timeline?limit=10", "", nil, &timeline); code != http.StatusOK {
		t.Fatalf("timeline: status %d", code)
	}
	if len(timeline.Events) == 0 {
		t.Fatal("the war has no history at all, not even its own start")
	}
}

// A queue with no free machine has to wait, not silently drop people.
func TestFormationWaitsForAServer(t *testing.T) {
	h := newHarness(t)
	clients := []*client{h.newClient(1, "RED"), h.newClient(2, "BLU"),
		h.newClient(3, "RED"), h.newClient(4, "BLU")}
	for _, c := range clients {
		c.deploy()
	}
	h.maker.Tick()

	if got := len(h.maker.LiveBattles()); got != 0 {
		t.Fatalf("%d battles were formed with no server in the pool", got)
	}
	found := false
	for _, ev := range clients[0].poll() {
		if ev.Type == mm.EventQueue && ev.Queue != nil &&
			ev.Queue.Message == "waiting for a free battle server" {
			found = true
		}
	}
	if !found {
		t.Error("players were left waiting without being told why")
	}

	server := h.newAgent("late", "10.0.0.9:27015")
	h.maker.Tick()
	if _, ok := server.poll(); !ok {
		t.Fatal("the battle was not formed once a server joined the pool")
	}
}

// Somebody else's battle can decide the front you are queued on. Waiting for a
// battle on a front that no longer exists would be a queue that never ends.
func TestQueueSurvivesItsFrontBeingDecided(t *testing.T) {
	h := newHarness(t)
	server := h.newAgent("node", "10.0.0.5:27015")

	fighters := []*client{
		h.newClient(1, "RED"), h.newClient(2, "RED"),
		h.newClient(3, "BLU"), h.newClient(4, "BLU"),
	}
	for _, c := range fighters {
		c.deploy()
	}
	h.maker.Tick()
	cmd, ok := server.poll()
	if !ok {
		t.Fatal("no battle was formed")
	}
	front := cmd.Assignment.Briefing.FrontID
	f, _ := h.engine.Front(front)

	// A third player queues on that front, and then it is decided without them.
	waiting := h.newClient(5, "RED")
	waiting.deploy()

	server.state(cmd.Assignment.MatchID, "live")

	// A server reports its scoreboard, in in-game teams, and the attacking
	// side plays as BLU — so which outcome string means "the attacker won" is
	// read off the roster the way the coordinator reads it, not guessed from
	// the side's name. See TestOneBattleMovesTheWar for the same translation.
	winner := ""
	for _, e := range cmd.Assignment.Roster {
		if side, _ := war.ParseSide(e.Side); side == f.Attacker {
			winner = e.Team + "_WIN"
			break
		}
	}
	if winner == "" {
		t.Fatal("no roster entry fights for the attacking side")
	}
	server.result(cmd.Assignment.MatchID, winner, 3, 0)

	// Then push it the rest of the way at full strength. How many battles that
	// takes is not a fixed number — a battle is worth its headcount, and the
	// one above was a small one — so this runs until the front is decided,
	// bounded so a rule that stops deciding fronts at all fails the assertion
	// below instead of hanging here.
	for i := 0; i < 4*len(f.Plan)+4; i++ {
		cur, open := h.engine.Front(front)
		if !open {
			break
		}
		outcome := war.OutcomeRedWin
		if cur.Attacker == war.SideBlu {
			outcome = war.OutcomeBluWin
		}
		if _, err := h.engine.RecordBattle(war.BattleResult{
			BattleID: "forced", FrontID: front, Outcome: outcome, Players: 24,
		}); err != nil {
			break
		}
	}
	if _, open := h.engine.Front(front); open {
		t.Fatal("the front was never decided; the test cannot check what it means to")
	}

	h.maker.Tick()

	moved := false
	for _, ev := range waiting.poll() {
		if ev.Type == mm.EventQueue && ev.Queue != nil && ev.Queue.FrontID != "" && ev.Queue.FrontID != front {
			moved = true
		}
	}
	if !moved {
		t.Fatal("a player queued on a decided front was left waiting on it forever")
	}
}

// A client that presses DEPLOY twice — a retry, a reconnect, an impatient
// double-click — must not lose its place. The queue time decides who has waited
// longest and when the coordinator stops holding out for a bigger battle, so
// resetting it would let a repeating client wait forever.
func TestRepeatedDeployKeepsYourPlaceInTheQueue(t *testing.T) {
	h := newHarness(t)
	h.newAgent("node", "10.0.0.5:27015")

	first := h.newClient(1, "RED")
	first.deploy()
	before := h.maker.QueueStatusFor(mustPlayer(t, h, first.token))

	time.Sleep(20 * time.Millisecond)
	first.deploy()
	after := h.maker.QueueStatusFor(mustPlayer(t, h, first.token))

	if after.WaitedS < before.WaitedS {
		t.Fatal("pressing DEPLOY again reset the player's wait")
	}
	if after.Position != 1 {
		t.Fatalf("player is now position %d in a queue of one", after.Position)
	}
}

// TestPartyDeployLandsTogether is the matchmaking gap the quickplay-vs-
// matchmaking critique calls out by name: a per-player server search has no
// way to reserve a slot for a friend. Here two players queue as a party three
// slots apart — enough that plain FIFO batching would put them in different
// 2v2 battles — and must still land in the same one.
func TestPartyDeployLandsTogether(t *testing.T) {
	h := newHarness(t)
	serverA := h.newAgent("node-a", "10.0.0.5:27015")
	serverB := h.newAgent("node-b", "10.0.0.6:27015")

	sides := []string{"RED", "RED", "BLU", "BLU", "RED", "BLU", "RED", "BLU"}
	clients := make([]*client, 8)
	for i := 0; i < 8; i++ {
		clients[i] = h.newClient(uint64(i+1), sides[i])
	}
	for i, c := range clients {
		steamID := i + 1
		if steamID == 3 || steamID == 6 {
			c.deployWithParty("duo")
		} else {
			c.deploy()
		}
	}

	h.maker.Tick()

	rosters := [][]uint64{}
	for _, s := range []*agent{serverA, serverB} {
		cmd, ok := s.poll()
		if !ok || cmd.Assignment == nil {
			continue
		}
		var ids []uint64
		for _, e := range cmd.Assignment.Roster {
			ids = append(ids, e.SteamID)
		}
		rosters = append(rosters, ids)
	}

	contains := func(ids []uint64, want uint64) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}
	found := false
	for _, roster := range rosters {
		if contains(roster, 3) {
			found = true
			if !contains(roster, 6) {
				t.Fatalf("party members 3 and 6 were split across battles: this roster is %v", roster)
			}
		}
	}
	if !found {
		t.Fatalf("player 3 was not assigned to any battle; rosters formed: %v", rosters)
	}
}

// TestP2PHostElectedAndCorroborated is the whole point of this rework, end to
// end over HTTP with no dedicated server in the pool at all: a roster forms,
// nothing is free to host it, the coordinator elects a capable player instead,
// that player's own machine joins the pool for exactly this battle, and its
// reported result only counts once enough of the rest of the roster agrees —
// the corroboration an untrusted, player-run host needs that a dedicated
// server's word does not.
func TestP2PHostResultFinishesWithAutomaticEvidence(t *testing.T) {
	h := newHarness(t)

	host := h.newHostCapableClient(1, "RED")
	others := []*client{
		h.newClient(2, "RED"), h.newClient(3, "BLU"), h.newClient(4, "BLU"),
	}
	allClients := append([]*client{host}, others...)
	for _, c := range allClients {
		c.deploy()
	}

	h.maker.Tick()

	// Every player, including the elected host, was pulled out of the queue
	// the moment the offer was made — nobody is left double-queued while an
	// offer is pending.
	var offer mm.Event
	found := false
	for _, ev := range host.poll() {
		if ev.Type == mm.EventHostOffer {
			offer, found = ev, true
		}
	}
	if !found || offer.Host == nil {
		t.Fatal("the capable player was never offered host")
	}
	matchID := offer.Host.MatchID
	if offer.Host.Map == "" {
		t.Fatal("the host offer carried no map to load")
	}

	serverID, serverToken := host.hostRegister(matchID)
	if serverID == "" || serverToken == "" {
		t.Fatal("host-register returned no server identity")
	}
	selfAgent := h.agentFromToken(serverID, serverToken)

	cmd, ok := selfAgent.poll()
	if !ok || cmd.Type != pool.CommandAssign || cmd.Assignment == nil {
		t.Fatalf("the elected host's own machine never received the assignment: %+v", cmd)
	}
	if cmd.Assignment.MatchID != matchID {
		t.Fatalf("assignment is for match %q, want %q", cmd.Assignment.MatchID, matchID)
	}

	// Reporting ready before an address exists must not silently send anyone
	// to nowhere — this is exactly the historical "connection never happens"
	// failure mode, refused loudly instead of reproduced.
	var errOut map[string]any
	if code := h.do("POST", "/api/v1/servers/state?server_id="+serverID, serverToken,
		serverStateRequest{MatchID: matchID, State: "ready"}, &errOut); code == http.StatusOK {
		t.Fatal("the server was allowed to report ready with no connect address")
	}

	const fakeIP = "169.254.10.7:27015"
	selfAgent.setAddress(fakeIP)
	selfAgent.state(matchID, "ready")

	for _, c := range others {
		ev := c.waitFor(mm.EventMatchReady)
		if ev.Match == nil || ev.Match.Connect != fakeIP {
			t.Fatalf("client %d was not sent the host's published address: %+v", c.steamID, ev.Match)
		}
		if ev.Match.IsHost {
			t.Fatalf("client %d was told it is the host, but only steam id 1 is", c.steamID)
		}
	}
	hostEv := host.waitFor(mm.EventMatchReady)
	if hostEv.Match == nil || !hostEv.Match.IsHost {
		t.Fatalf("the elected host was not told it is the host: %+v", hostEv.Match)
	}

	selfAgent.state(matchID, "live")

	// The host report is held for automatic evidence, without a manual vote.
	out := selfAgent.result(matchID, "RED_WIN", 3, 1)
	if out["war_update"] != nil {
		t.Fatalf("unwitnessed P2P result moved the war: %v", out)
	}
	match, ok := h.maker.Match(matchID)
	if !ok || match.State != mm.MatchAwaitingResult {
		t.Fatalf("match did not wait for automatic evidence: %+v", match)
	}

	// One authenticated non-host independently reports the scoreboard it saw.
	evidenceOut := others[0].resultEvidence(matchID, "RED_WIN", 3, 1, false, "")
	if evidenceOut["war_update"] == nil {
		t.Fatalf("matching automatic evidence did not move the war: %v", evidenceOut)
	}
	match, ok = h.maker.Match(matchID)
	if !ok {
		t.Fatal("the match vanished after its result")
	}
	if match.State != mm.MatchFinished {
		t.Fatalf("match state = %s, want finished", match.State)
	}
	for _, c := range allClients {
		ev := c.waitFor(mm.EventMatchOver)
		if ev.Over == nil || !ev.Over.Counted {
			t.Fatalf("client %d was not told the battle counted: %+v", c.steamID, ev.Over)
		}
	}
}

func mustPlayer(t *testing.T, h *harness, token string) *mm.Player {
	t.Helper()
	p, ok := h.maker.Authenticate(token)
	if !ok {
		t.Fatal("session vanished")
	}
	return p
}
