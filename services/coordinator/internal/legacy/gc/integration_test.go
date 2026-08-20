package gc

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/greyline-frontress/coordinator/internal/config"
	"github.com/greyline-frontress/coordinator/internal/legacy/war"
	"github.com/greyline-frontress/coordinator/internal/security"
	"github.com/greyline-frontress/coordinator/internal/steam"
	"github.com/greyline-frontress/coordinator/internal/wire"
	"github.com/greyline-frontress/coordinator/internal/wire/pb"
)

const testWorld = `{
  "revision": 1,
  "nodes": [
    {"id": "hub", "name": "GREYLINE HUB", "owner": 0, "state": "CONTESTED", "adjacent": []}
  ],
  "fronts": [
    {"id": "f1", "node_id": "hub", "name": "GREYLINE HUB", "attacker": 1,
     "red_points": 0, "blu_points": 0, "points_to_win": 2, "active": true,
     "map_pool": [
       {"map": "arena_badlands", "mode": "arena", "min_players": 2, "ideal_players": 4, "max_players": 8}
     ]}
  ]
}`

// harness is a running coordinator plus the plumbing to talk to it.
type harness struct {
	t     *testing.T
	srv   *Server
	world *war.FileProvider
	addr  string
	stop  context.CancelFunc
	dir   string
}

func newHarness(t *testing.T, tune func(*config.Config)) *harness {
	t.Helper()
	return newHarnessWithWorld(t, testWorld, tune)
}

func newHarnessWithWorld(t *testing.T, worldJSON string, tune func(*config.Config)) *harness {
	t.Helper()
	dir := t.TempDir()

	worldPath := filepath.Join(dir, "world.json")
	if err := os.WriteFile(worldPath, []byte(worldJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	world, err := war.LoadFile(worldPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.AdminListen = ""
	cfg.Security.Secret = "test-secret"
	// The retired P2P coordinator has no server pool, but it shares the config
	// type with the live one, whose validation requires a pool key.
	cfg.Pool.Key = "test-pool-key"
	cfg.Match.TeamSizes = []int{1}
	cfg.Match.MinTeamSize = 1
	cfg.Match.FormWait = 0
	cfg.Timing.TickInterval = config.Duration(20 * time.Millisecond)
	cfg.Timing.HostBootDeadline = config.Duration(2 * time.Second)
	cfg.Timing.ClientConnectDeadline = config.Duration(2 * time.Second)
	cfg.Timing.MigrationHold = config.Duration(2 * time.Second)
	cfg.Election.MinUploadKbps = 0
	cfg.Election.MinCPUScore = 0
	cfg.Election.MinMemoryMB = 0
	if tune != nil {
		tune(cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	// The live coordinator has no state file — the war log is its persistence.
	// This retired package still keeps a player store, so the test names one.
	store, err := LoadPlayerStore(filepath.Join(dir, "players.json"))
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, log, steam.DevAuthenticator{}, world, security.DefaultPolicy(), store)

	// Bind before Run so the test knows the port; Run reuses cfg.Listen, so
	// resolve the ephemeral port by listening once and handing back the address.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	cfg.Listen = addr

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Run(ctx) }()

	h := &harness{t: t, srv: srv, world: world, addr: addr, stop: cancel, dir: dir}
	h.waitListening()
	t.Cleanup(func() { cancel(); time.Sleep(50 * time.Millisecond) })
	return h
}

func (h *harness) waitListening() {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", h.addr)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatal("coordinator never started listening")
}

// client is a fake game client speaking the coordinator protocol.
type client struct {
	t       *testing.T
	steamID uint64
	conn    *wire.Conn
	inbox   []*pb.CMsgEnvelope
}

func (h *harness) connect(steamID uint64) *client {
	h.t.Helper()
	c, err := net.Dial("tcp", h.addr)
	if err != nil {
		h.t.Fatal(err)
	}
	cl := &client{t: h.t, steamID: steamID, conn: wire.NewConn(c, 5*time.Second)}
	cl.send(&pb.CMsgEnvelope{
		JobId: proto.Uint64(1),
		Body: &pb.CMsgEnvelope_Hello{Hello: &pb.CMsgHello{
			ProtocolVersion: proto.Uint32(h.srv.cfg.ProtocolVersion),
			SteamId:         proto.Uint64(steamID),
			ClientVersion:   proto.String("test"),
			Platform:        proto.String("test"),
			Capabilities: &pb.CMsgHostCapabilities{
				CanHost:    proto.Bool(true),
				UploadKbps: proto.Uint32(10000),
				CpuScore:   proto.Uint32(80),
				MemoryMb:   proto.Uint32(16384),
				SdrPopId:   proto.Uint32(1),
			},
		}},
	})
	w := cl.expect(func(e *pb.CMsgEnvelope) bool { return e.GetWelcome() != nil }, 3*time.Second)
	if got := w.GetWelcome().GetResult(); got != pb.EResult_RESULT_OK {
		h.t.Fatalf("client %d rejected: %s (%s)", steamID, got, w.GetWelcome().GetMessage())
	}
	h.t.Cleanup(func() { _ = cl.conn.Close() })
	return cl
}

func (c *client) send(env *pb.CMsgEnvelope) {
	c.t.Helper()
	if err := c.conn.Write(env); err != nil {
		c.t.Fatalf("client %d write: %v", c.steamID, err)
	}
}

// expect reads until a message satisfying pred arrives, buffering the rest so a
// later expect can still find them. Failing to receive it fails the test.
func (c *client) expect(pred func(*pb.CMsgEnvelope) bool, within time.Duration) *pb.CMsgEnvelope {
	c.t.Helper()
	env := c.tryExpect(pred, within)
	if env == nil {
		c.t.Fatalf("client %d: expected message never arrived", c.steamID)
	}
	return env
}

// tryExpect is expect without the assertion: it returns nil if nothing matching
// turns up in time. Used where the test does not yet know which client the
// coordinator picked.
func (c *client) tryExpect(pred func(*pb.CMsgEnvelope) bool, within time.Duration) *pb.CMsgEnvelope {
	c.t.Helper()
	for i, e := range c.inbox {
		if pred(e) {
			c.inbox = append(c.inbox[:i], c.inbox[i+1:]...)
			return e
		}
	}
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		env, err := c.conn.Read(deadline)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return nil
			}
			c.t.Fatalf("client %d read: %v", c.steamID, err)
		}
		if pred(env) {
			return env
		}
		c.inbox = append(c.inbox, env)
	}
	return nil
}

func (c *client) deploy(side pb.ESide) {
	c.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_DeployRequest{DeployRequest: &pb.CMsgDeployRequest{
		PreferredSide: side.Enum(),
	}}})
}

// testConnectAddress stands in for the address the host's engine advertises —
// a Steam FakeIP, which is what a real listen server reports under Steam
// Networking.
const testConnectAddress = "169.254.13.37:27015"

func hasAssign(e *pb.CMsgEnvelope) bool { return e.GetAssignHost() != nil }
func hasJoin(e *pb.CMsgEnvelope) bool   { return e.GetJoinBattle() != nil }
func hasOver(e *pb.CMsgEnvelope) bool   { return e.GetMatchOver() != nil }

// bringUpMatch runs the flow up to a live match with two players and returns
// (host, guest) in whichever order the coordinator elected.
func bringUpMatch(t *testing.T, h *harness, a, b *client) (host, guest *client, matchID string, secret []byte) {
	t.Helper()
	host, guests, matchID, secret := bringUpMatchN(t, h, a, b)
	return host, guests[0], matchID, secret
}

// bringUpMatchN is the same for any roster size. Returns the elected host and
// everyone else.
func bringUpMatchN(t *testing.T, h *harness, clients ...*client) (host *client, guests []*client, matchID string, secret []byte) {
	t.Helper()
	for i, c := range clients {
		if i%2 == 0 {
			c.deploy(pb.ESide_SIDE_RED)
		} else {
			c.deploy(pb.ESide_SIDE_BLU)
		}
	}

	// Exactly one of them is told to host. Poll each in turn rather than racing
	// two readers on the same connection.
	var assignEnv *pb.CMsgEnvelope
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && assignEnv == nil {
		for _, c := range clients {
			if env := c.tryExpect(hasAssign, 200*time.Millisecond); env != nil {
				host, assignEnv = c, env
				break
			}
		}
	}
	if host == nil {
		t.Fatal("no client was assigned as host")
	}
	for _, c := range clients {
		if c != host {
			guests = append(guests, c)
		}
	}

	assign := assignEnv.GetAssignHost()
	matchID = assign.GetMatchId()
	secret = assign.GetMatchSecret()
	if len(secret) == 0 {
		t.Fatal("host was not given a match secret")
	}

	// The host's listen server is up and it reports where the engine put it.
	host.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_HostReady{HostReady: &pb.CMsgHostReady{
		MatchId:           proto.String(matchID),
		ConnectAddress:    proto.String(testConnectAddress),
		GameServerSteamId: proto.Uint64(host.steamID),
		MapName:           proto.String(assign.GetMapName()),
	}}})

	for _, g := range guests {
		join := g.expect(hasJoin, 5*time.Second).GetJoinBattle()
		if join.GetMatchId() != matchID {
			t.Fatalf("join points at match %s, expected %s", join.GetMatchId(), matchID)
		}
		if join.GetConnectAddress() != testConnectAddress {
			t.Fatalf("guest was sent to %q, host published %q",
				join.GetConnectAddress(), testConnectAddress)
		}
		if join.GetHostSteamId() != host.steamID {
			t.Fatalf("join names host %d, the elected host is %d",
				join.GetHostSteamId(), host.steamID)
		}
		if join.GetMapName() != assign.GetMapName() {
			t.Fatalf("guest was told map %q, host loaded %q", join.GetMapName(), assign.GetMapName())
		}
		g.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_ClientJoinState{ClientJoinState: &pb.CMsgClientJoinState{
			MatchId: proto.String(matchID), Connected: proto.Bool(true), RttMs: proto.Uint32(30),
		}}})
	}
	return host, guests, matchID, secret
}

// TestFullMatchAdvancesTheWar is the MVP acceptance path: two players deploy,
// the coordinator forms a battle, elects a host, the host reports a signed
// result, the client corroborates it, and the front moves.
func TestFullMatchAdvancesTheWar(t *testing.T) {
	h := newHarness(t, nil)
	a := h.connect(76561198000000001)
	b := h.connect(76561198000000002)

	host, guest, matchID, secret := bringUpMatch(t, h, a, b)

	before, _ := h.world.Front("f1")
	if before.RedPoints != 0 {
		t.Fatalf("front should start at 0 RED points, got %d", before.RedPoints)
	}

	const red, blu, dur = 5, 3, 900
	host.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_MatchResult{MatchResult: &pb.CMsgMatchResult{
		MatchId:   proto.String(matchID),
		Outcome:   pb.EMatchOutcome_OUTCOME_RED_WIN.Enum(),
		RedScore:  proto.Uint32(red),
		BluScore:  proto.Uint32(blu),
		DurationS: proto.Uint32(dur),
		Signature: security.SignResult(secret, matchID, int32(pb.EMatchOutcome_OUTCOME_RED_WIN), red, blu, dur),
	}}})
	guest.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_ResultAttestation{ResultAttestation: &pb.CMsgResultAttestation{
		MatchId:  proto.String(matchID),
		Outcome:  pb.EMatchOutcome_OUTCOME_RED_WIN.Enum(),
		RedScore: proto.Uint32(red),
		BluScore: proto.Uint32(blu),
	}}})

	over := guest.expect(hasOver, 5*time.Second).GetMatchOver()
	if over.GetDisputed() {
		t.Fatal("a corroborated result must not be marked disputed")
	}
	if !over.GetCountedTowardsWar() {
		t.Fatalf("result should have counted: %s", over.GetMessage())
	}
	if got := over.GetWarUpdate().GetRedPoints(); got != 1 {
		t.Fatalf("front should be at 1 RED point, got %d", got)
	}

	after, _ := h.world.Front("f1")
	if after.RedPoints != 1 {
		t.Fatalf("war layer not advanced: %d RED points", after.RedPoints)
	}
}

// A host that reports a result its own players contradict must not move the war.
func TestContradictedResultIsDisputed(t *testing.T) {
	h := newHarness(t, nil)
	a := h.connect(76561198000000011)
	b := h.connect(76561198000000012)
	host, guest, matchID, secret := bringUpMatch(t, h, a, b)

	const red, blu, dur = 5, 0, 600
	host.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_MatchResult{MatchResult: &pb.CMsgMatchResult{
		MatchId:   proto.String(matchID),
		Outcome:   pb.EMatchOutcome_OUTCOME_RED_WIN.Enum(),
		RedScore:  proto.Uint32(red),
		BluScore:  proto.Uint32(blu),
		DurationS: proto.Uint32(dur),
		Signature: security.SignResult(secret, matchID, int32(pb.EMatchOutcome_OUTCOME_RED_WIN), red, blu, dur),
	}}})
	// The guest saw BLU win.
	guest.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_ResultAttestation{ResultAttestation: &pb.CMsgResultAttestation{
		MatchId:  proto.String(matchID),
		Outcome:  pb.EMatchOutcome_OUTCOME_BLU_WIN.Enum(),
		RedScore: proto.Uint32(0),
		BluScore: proto.Uint32(5),
	}}})

	over := guest.expect(hasOver, 5*time.Second).GetMatchOver()
	if !over.GetDisputed() {
		t.Fatal("a contradicted result must be marked disputed")
	}
	if over.GetCountedTowardsWar() {
		t.Fatal("a disputed result must not advance the war")
	}
	if f, _ := h.world.Front("f1"); f.RedPoints != 0 {
		t.Fatalf("disputed result still moved the front to %d", f.RedPoints)
	}
}

// A forged result signature means the host is not the party the coordinator
// issued the secret to, so the whole match is void.
func TestForgedResultSignatureAbortsTheMatch(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Security.RequireSignedResults = true })
	a := h.connect(76561198000000021)
	b := h.connect(76561198000000022)
	host, guest, matchID, _ := bringUpMatch(t, h, a, b)

	host.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_MatchResult{MatchResult: &pb.CMsgMatchResult{
		MatchId:   proto.String(matchID),
		Outcome:   pb.EMatchOutcome_OUTCOME_RED_WIN.Enum(),
		RedScore:  proto.Uint32(5),
		BluScore:  proto.Uint32(0),
		DurationS: proto.Uint32(600),
		Signature: []byte("not-a-real-signature"),
	}}})

	over := guest.expect(hasOver, 5*time.Second).GetMatchOver()
	if over.GetCountedTowardsWar() {
		t.Fatal("an unsigned result must never count")
	}
	if f, _ := h.world.Front("f1"); f.RedPoints != 0 {
		t.Fatal("a forged result moved the front")
	}
}

// A host whose declared winner contradicts its own scoreline is lying about one
// of the two, so the match is void.
func TestSelfContradictoryResultIsRejected(t *testing.T) {
	h := newHarness(t, nil)
	a := h.connect(76561198000000031)
	b := h.connect(76561198000000032)
	host, guest, matchID, secret := bringUpMatch(t, h, a, b)

	// Declares RED won while reporting BLU ahead on points.
	const red, blu, dur = 1, 5, 600
	host.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_MatchResult{MatchResult: &pb.CMsgMatchResult{
		MatchId:   proto.String(matchID),
		Outcome:   pb.EMatchOutcome_OUTCOME_RED_WIN.Enum(),
		RedScore:  proto.Uint32(red),
		BluScore:  proto.Uint32(blu),
		DurationS: proto.Uint32(dur),
		Signature: security.SignResult(secret, matchID, int32(pb.EMatchOutcome_OUTCOME_RED_WIN), red, blu, dur),
	}}})

	over := guest.expect(hasOver, 5*time.Second).GetMatchOver()
	if over.GetCountedTowardsWar() {
		t.Fatal("a self-contradictory result must not count")
	}
}

// Losing the host mid-match must move the battle to another player rather than
// destroy it.
func TestHostMigrationOnHostDisconnect(t *testing.T) {
	h := newHarness(t, nil)
	a := h.connect(76561198000000041)
	b := h.connect(76561198000000042)
	host, guest, matchID, _ := bringUpMatch(t, h, a, b)

	// The host records progress, then vanishes.
	host.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_HostHeartbeat{HostHeartbeat: &pb.CMsgHostHeartbeat{
		MatchId: proto.String(matchID),
		State:   pb.EMatchState_MATCH_LIVE.Enum(),
		Snapshot: &pb.CMsgMatchSnapshot{
			RedScore: proto.Uint32(2), BluScore: proto.Uint32(1),
			Round: proto.Uint32(3), MapName: proto.String("arena_badlands"),
		},
	}}})
	time.Sleep(150 * time.Millisecond)
	_ = host.conn.Close()

	// The surviving player is promoted and handed the snapshot back.
	assign := guest.expect(hasAssign, 5*time.Second).GetAssignHost()
	if !assign.GetIsMigration() {
		t.Fatal("the takeover assignment should be flagged as a migration")
	}
	if assign.GetMatchId() != matchID {
		t.Fatalf("migration created a new match %s instead of continuing %s", assign.GetMatchId(), matchID)
	}
	restore := assign.GetRestore()
	if restore == nil {
		t.Fatal("the new host was not given the snapshot, so the battle would restart from scratch")
	}
	if restore.GetRedScore() != 2 || restore.GetBluScore() != 1 || restore.GetRound() != 3 {
		t.Fatalf("snapshot was not preserved across migration: %v", restore)
	}
	if assign.GetMapName() != "arena_badlands" {
		t.Fatalf("migration changed the map to %q", assign.GetMapName())
	}
}

// A migrated battle has a new server at a new address, and the survivors are
// only useful to it if they are actually sent there. The dead host's address
// must be gone from the coordinator by then, so nobody reconnecting mid-
// migration is pointed at a corpse.
func TestMigrationSendsSurvivorsToTheNewHostAddress(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Match.TeamSizes = []int{2}
		c.Match.MinTeamSize = 2
	})
	var clients []*client
	for i := 0; i < 4; i++ {
		clients = append(clients, h.connect(uint64(76561198000000131+i)))
	}
	host, guests, matchID, _ := bringUpMatchN(t, h, clients...)

	time.Sleep(100 * time.Millisecond)
	_ = host.conn.Close()

	// Find the promoted survivor and stand its server up somewhere else.
	var newHost *client
	for _, g := range guests {
		if env := g.tryExpect(hasAssign, 3*time.Second); env != nil {
			newHost = g
			break
		}
	}
	if newHost == nil {
		t.Fatal("no survivor was promoted to host")
	}

	const movedTo = "169.254.99.1:27015"
	h.srv.Inspect(func(s *Server) {
		if m := s.matches[matchID]; m != nil && m.hosted() {
			t.Errorf("the dead host's endpoint survived the migration: %q / %d",
				m.ConnectAddress, m.GameServerSteamID)
		}
	})

	newHost.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_HostReady{HostReady: &pb.CMsgHostReady{
		MatchId:           proto.String(matchID),
		ConnectAddress:    proto.String(movedTo),
		GameServerSteamId: proto.Uint64(newHost.steamID),
		MapName:           proto.String("arena_badlands"),
	}}})

	for _, g := range guests {
		if g == newHost {
			continue
		}
		join := g.expect(hasJoin, 5*time.Second).GetJoinBattle()
		if join.GetConnectAddress() != movedTo {
			t.Fatalf("survivor was sent to %q, the new host is at %q",
				join.GetConnectAddress(), movedTo)
		}
		if join.GetHostSteamId() != newHost.steamID {
			t.Fatalf("join names host %d, the new host is %d",
				join.GetHostSteamId(), newHost.steamID)
		}
	}
}

// Players who are not the replacement host must be told to hold, and for
// exactly as long as the coordinator itself waits for that host. Holding longer
// leaves them staring at a battle that has already been abandoned.
func TestMigrationTellsBystandersToHoldForTheBootDeadline(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Match.TeamSizes = []int{2}
		c.Match.MinTeamSize = 2
	})
	var clients []*client
	for i := 0; i < 4; i++ {
		clients = append(clients, h.connect(uint64(76561198000000091+i)))
	}
	host, guests, matchID, _ := bringUpMatchN(t, h, clients...)

	time.Sleep(100 * time.Millisecond)
	_ = host.conn.Close()

	// One survivor is promoted; the other two must hear about it.
	want := uint32(h.srv.cfg.Timing.HostBootDeadline.D().Seconds())
	told := 0
	for _, g := range guests {
		env := g.tryExpect(func(e *pb.CMsgEnvelope) bool { return e.GetMigrateHost() != nil }, 3*time.Second)
		if env == nil {
			continue // this one is the new host and got AssignHost instead
		}
		mig := env.GetMigrateHost()
		told++
		if mig.GetMatchId() != matchID {
			t.Fatalf("migration notice for the wrong match: %s", mig.GetMatchId())
		}
		if mig.GetHoldSeconds() != want {
			t.Fatalf("clients told to hold %ds, coordinator waits %ds", mig.GetHoldSeconds(), want)
		}
		if mig.GetNewHostSteamId() == host.steamID {
			t.Fatal("the lost host was named as its own replacement")
		}
	}
	if told != len(guests)-1 {
		t.Fatalf("%d of %d bystanders were told to hold", told, len(guests)-1)
	}
}

// A mode whose state is not the round scoreline must be abandoned rather than
// resumed on a new host with most of the game silently missing.
func TestNonMigratableModeAbortsInsteadOfMigrating(t *testing.T) {
	const mvmWorld = `{
  "revision": 1,
  "nodes": [{"id": "hub", "name": "HUB", "owner": 0, "state": "CONTESTED", "adjacent": []}],
  "fronts": [
    {"id": "f1", "node_id": "hub", "name": "HUB", "attacker": 1,
     "red_points": 0, "blu_points": 0, "points_to_win": 2, "active": true,
     "map_pool": [
       {"map": "mvm_coaltown", "mode": "mvm", "min_players": 2, "ideal_players": 4,
        "max_players": 6, "migratable": false}
     ]}
  ]
}`
	h := newHarnessWithWorld(t, mvmWorld, nil)
	a := h.connect(76561198000000101)
	b := h.connect(76561198000000102)
	host, guest, matchID, _ := bringUpMatch(t, h, a, b)

	time.Sleep(100 * time.Millisecond)
	_ = host.conn.Close()

	over := guest.expect(hasOver, 5*time.Second).GetMatchOver()
	if over.GetMatchId() != matchID {
		t.Fatalf("match over for the wrong match: %s", over.GetMatchId())
	}
	if over.GetCountedTowardsWar() {
		t.Fatal("an abandoned battle must not advance the war")
	}
	// It must not have tried to hand the battle to the survivor.
	if env := guest.tryExpect(hasAssign, 500*time.Millisecond); env != nil {
		t.Fatal("a non-migratable battle was handed to a new host anyway")
	}
}

// With nobody left to take over, the match ends rather than hanging forever.
func TestMigrationWithNoCandidatesAborts(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Election.RequireCanHostBit = true
	})
	a := h.connect(76561198000000051)
	b := h.connect(76561198000000052)
	host, guest, matchID, _ := bringUpMatch(t, h, a, b)

	// The remaining player declares it cannot host.
	guest.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_HostCapabilities{HostCapabilities: &pb.CMsgHostCapabilities{
		CanHost: proto.Bool(false),
	}}})
	time.Sleep(150 * time.Millisecond)
	_ = host.conn.Close()

	over := guest.expect(hasOver, 5*time.Second).GetMatchOver()
	if over.GetMatchId() != matchID {
		t.Fatalf("match over for the wrong match: %s", over.GetMatchId())
	}
	if over.GetCountedTowardsWar() {
		t.Fatal("an aborted match must not count towards the war")
	}
	if _, ok := waitFor(t, func() bool {
		f, _ := h.world.Front("f1")
		return f.RedPoints == 0 && f.BluPoints == 0
	}); !ok {
		t.Fatal("aborted match moved the front")
	}
}

// A client reporting a fatal cheat convar is removed from the match and banned.
func TestFatalClientViolationKicksAndBans(t *testing.T) {
	h := newHarness(t, nil)
	a := h.connect(76561198000000061)
	b := h.connect(76561198000000062)
	host, guest, matchID, secret := bringUpMatch(t, h, a, b)

	violations := 1
	digest := security.DefaultPolicy().Digest()
	host.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_IntegrityReport{IntegrityReport: &pb.CMsgIntegrityReport{
		MatchId:      proto.String(matchID),
		PolicyDigest: digest,
		Observations: []*pb.CMsgConVarObservation{{
			SteamId:     proto.Uint64(guest.steamID),
			Name:        proto.String("r_drawothermodels"),
			Value:       proto.String("2"),
			QueryStatus: proto.String("ok"),
		}},
		Signature: security.SignIntegrityReport(secret, matchID, digest, violations-1),
	}}})

	verdict := guest.expect(func(e *pb.CMsgEnvelope) bool {
		return e.GetSecurityVerdict() != nil && e.GetSecurityVerdict().GetAction() == "kick"
	}, 5*time.Second).GetSecurityVerdict()
	if verdict.GetBanSeconds() == 0 {
		t.Fatal("a fatal violation should carry a matchmaking ban")
	}
}

// A host that claims a policy digest other than the one it was issued is
// running a different rule set; the match cannot be trusted.
func TestWrongPolicyDigestAbortsTheMatch(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Security.RequireSignedResults = true })
	a := h.connect(76561198000000071)
	b := h.connect(76561198000000072)
	host, guest, matchID, secret := bringUpMatch(t, h, a, b)

	bogus := append([]byte(nil), security.DefaultPolicy().Digest()...)
	bogus[0] ^= 0xff
	host.send(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_IntegrityReport{IntegrityReport: &pb.CMsgIntegrityReport{
		MatchId:      proto.String(matchID),
		PolicyDigest: bogus,
		Signature:    security.SignIntegrityReport(secret, matchID, bogus, 0),
	}}})

	over := guest.expect(hasOver, 5*time.Second).GetMatchOver()
	if over.GetCountedTowardsWar() {
		t.Fatal("a match run under an unknown policy must not count")
	}
}

// A stale build must be refused at the door rather than allowed to write war
// state with different rules.
func TestClientVersionGate(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.AcceptedClientVersions = []string{"1.2.3"}
	})
	c, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn := wire.NewConn(c, 5*time.Second)
	if err := conn.Write(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_Hello{Hello: &pb.CMsgHello{
		ProtocolVersion: proto.Uint32(h.srv.cfg.ProtocolVersion),
		SteamId:         proto.Uint64(76561198000000081),
		ClientVersion:   proto.String("0.0.1"),
	}}}); err != nil {
		t.Fatal(err)
	}
	env, err := conn.Read(time.Now().Add(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got := env.GetWelcome().GetResult(); got != pb.EResult_RESULT_INVALID_PROTOCOL {
		t.Fatalf("expected the build to be refused, got %s", got)
	}
}

// Nothing but a hello may be processed before the handshake completes.
func TestMessagesBeforeHelloAreRefused(t *testing.T) {
	h := newHarness(t, nil)
	c, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn := wire.NewConn(c, 5*time.Second)
	if err := conn.Write(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_DeployRequest{
		DeployRequest: &pb.CMsgDeployRequest{},
	}}); err != nil {
		t.Fatal(err)
	}
	env, err := conn.Read(time.Now().Add(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got := env.GetWelcome().GetResult(); got != pb.EResult_RESULT_AUTH_FAILED {
		t.Fatalf("expected an auth failure, got %s", got)
	}
}

func waitFor(t *testing.T, pred func() bool) (time.Duration, bool) {
	t.Helper()
	start := time.Now()
	for time.Since(start) < 3*time.Second {
		if pred() {
			return time.Since(start), true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return time.Since(start), false
}
