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
		PoolKey:     testPoolKey,
		MaxPollWait: time.Second,
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
		"steam_id": steamID, "name": fmt.Sprintf("merc%d", steamID), "side": side,
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
	out := server.result(as.MatchID, "RED_WIN", 3, 1)
	if out["war_update"] == nil {
		t.Fatalf("the result did not move the war: %v", out)
	}

	// The front is where the war continues, and it moved.
	after, stillOpen := h.engine.Front(front.ID)
	if stillOpen && after.Stage == stageBefore && after.Attacker == war.SideRed {
		t.Fatalf("RED won but the front is still at stage %d", after.Stage)
	}

	// And the players are told, on the same screen they will press DEPLOY from.
	for _, c := range clients {
		ev := c.waitFor(mm.EventMatchOver)
		if ev.Over == nil || !ev.Over.Counted {
			t.Fatalf("client %d was not told the battle counted: %+v", c.steamID, ev.Over)
		}
		if ev.Over.Update == nil || ev.Over.Update.Headline == "" {
			t.Fatalf("client %d got no war headline: %+v", c.steamID, ev.Over)
		}
		if want := c.steamID <= 2; ev.Over.Won != want {
			t.Errorf("client %d Won=%v, want %v", c.steamID, ev.Over.Won, want)
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
	winner := "RED_WIN"
	if f.Attacker == war.SideBlu {
		winner = "BLU_WIN"
	}
	server.result(cmd.Assignment.MatchID, winner, 3, 0)
	for i := 0; i < len(f.Plan); i++ {
		cur, open := h.engine.Front(front)
		if !open {
			break
		}
		outcome := war.OutcomeRedWin
		if cur.Attacker == war.SideBlu {
			outcome = war.OutcomeBluWin
		}
		if _, err := h.engine.RecordBattle(war.BattleResult{
			BattleID: "forced", FrontID: front, Outcome: outcome,
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

func mustPlayer(t *testing.T, h *harness, token string) *mm.Player {
	t.Helper()
	p, ok := h.maker.Authenticate(token)
	if !ok {
		t.Fatal("session vanished")
	}
	return p
}
