package httpapi

import (
	"net/http"
	"testing"

	"github.com/greyline-frontress/coordinator/internal/bans"
	"github.com/greyline-frontress/coordinator/internal/pool"
)

// hello is the door. A banned account must not get a session at all.
func TestBannedAccountCannotSayHello(t *testing.T) {
	h := newHarness(t)
	if _, err := h.bans.Ban(bans.Ban{SteamID: 111, Reason: "cheating"}); err != nil {
		t.Fatalf("ban: %v", err)
	}

	var resp map[string]any
	code := h.do("POST", "/api/v1/client/hello", "", map[string]any{
		"steam_id": "111", "name": "merc", "side": "RED", "protocol_version": 3,
	}, &resp)
	if code != http.StatusForbidden {
		t.Fatalf("banned hello: status %d, want 403", code)
	}
	if resp["ban"] == nil {
		t.Fatalf("the reply should say what the ban is: %v", resp)
	}
	if msg, _ := resp["error"].(string); msg == "" {
		t.Fatal("the reply should carry a sentence the menu can show")
	}
}

func TestUnbannedAccountIsLetBackIn(t *testing.T) {
	h := newHarness(t)
	h.bans.Ban(bans.Ban{SteamID: 111, Reason: "cheating"})
	if _, err := h.bans.Lift(111, "admin", "appealed"); err != nil {
		t.Fatalf("lift: %v", err)
	}
	// newClient fails the test itself if hello does not come back 200.
	h.newClient(111, "RED")
}

// A ban that lands while the player is connected has to reach them, and has to
// stop the one thing that matters: getting into another battle.
func TestBanMidSessionEndsItAndBlocksDeploy(t *testing.T) {
	h := newHarness(t)
	c := h.newClient(111, "RED")

	var out map[string]any
	code := h.doAdmin("POST", "/bans", map[string]any{
		"steam_id": "111", "reason": "aimbot", "issued_by": "admin",
	}, &out)
	if code != http.StatusOK {
		t.Fatalf("admin ban: status %d (%v)", code, out)
	}
	if ended, _ := out["session_ended"].(bool); !ended {
		t.Fatal("the ban should have ended the connected session")
	}

	// The token is gone, so the client is told to say hello again — and hello
	// is where it learns it is banned.
	if code := h.do("GET", "/api/v1/client/self", c.token, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("self after ban: status %d, want 401", code)
	}
}

// The matchmaker's own check, reached without going through hello: a ban that
// lands on a session that is already authenticated still stops DEPLOY.
func TestDeployIsRefusedWhileBanned(t *testing.T) {
	h := newHarness(t)
	c := h.newClient(111, "RED")

	// Ban through the list directly, so the session is not kicked and the
	// client still holds a valid token — the exact race an operator ban has
	// with a client that is mid-poll.
	if _, err := h.bans.Ban(bans.Ban{SteamID: 111, Reason: "cheating"}); err != nil {
		t.Fatalf("ban: %v", err)
	}

	var out map[string]any
	code := h.do("POST", "/api/v1/client/deploy", c.token, map[string]any{"accept_contract": true}, &out)
	if code != http.StatusForbidden {
		t.Fatalf("deploy while banned: status %d, want 403 (%v)", code, out)
	}
}

func TestAdminBanRoundTrip(t *testing.T) {
	h := newHarness(t)

	var banned map[string]any
	if code := h.doAdmin("POST", "/bans", map[string]any{
		"steam_id": "111", "reason": "aimbot", "duration": "30m", "issued_by": "admin",
	}, &banned); code != http.StatusOK {
		t.Fatalf("ban: status %d (%v)", code, banned)
	}
	view, _ := banned["ban"].(map[string]any)
	if view == nil {
		t.Fatalf("no ban in the reply: %v", banned)
	}
	if perm, _ := view["permanent"].(bool); perm {
		t.Fatal("a 30m ban is not permanent")
	}
	if remaining, _ := view["remaining_s"].(float64); remaining <= 0 || remaining > 1800 {
		t.Fatalf("remaining_s is %v, want something inside 30 minutes", remaining)
	}

	var list struct {
		Bans []banView `json:"bans"`
	}
	if code := h.doAdmin("GET", "/bans", nil, &list); code != http.StatusOK {
		t.Fatalf("list: status %d", code)
	}
	if len(list.Bans) != 1 || list.Bans[0].SteamID != 111 {
		t.Fatalf("unexpected ban list: %+v", list.Bans)
	}
	if list.Bans[0].IssuedBy != "admin" {
		t.Fatalf("the operator was not recorded: %+v", list.Bans[0])
	}

	var lifted map[string]any
	if code := h.doAdmin("POST", "/bans/lift", map[string]any{"steam_id": "111", "by": "admin"}, &lifted); code != http.StatusOK {
		t.Fatalf("lift: status %d (%v)", code, lifted)
	}
	if ok, _ := lifted["lifted"].(bool); !ok {
		t.Fatalf("lift did not report a removal: %v", lifted)
	}

	list.Bans = nil
	h.doAdmin("GET", "/bans", nil, &list)
	if len(list.Bans) != 0 {
		t.Fatalf("ban survived the lift: %+v", list.Bans)
	}
}

func TestAdminBanRejectsBadInput(t *testing.T) {
	h := newHarness(t)

	if code := h.doAdmin("POST", "/bans", map[string]any{"reason": "nobody"}, nil); code != http.StatusBadRequest {
		t.Fatalf("ban with no steam_id: status %d, want 400", code)
	}
	if code := h.doAdmin("POST", "/bans", map[string]any{
		"steam_id": "111", "duration": "next tuesday",
	}, nil); code != http.StatusBadRequest {
		t.Fatalf("ban with an unparseable duration: status %d, want 400", code)
	}
	if code := h.doAdmin("POST", "/bans/lift", map[string]any{"steam_id": "0"}, nil); code != http.StatusBadRequest {
		t.Fatalf("lift with no steam_id: status %d, want 400", code)
	}
}

// Lifting a ban nobody has is what an operator does when they mistype a
// SteamID. It is not an error, but it must not claim to have done something.
func TestLiftingAnUnbannedAccountSaysSo(t *testing.T) {
	h := newHarness(t)
	var out map[string]any
	if code := h.doAdmin("POST", "/bans/lift", map[string]any{"steam_id": "111"}, &out); code != http.StatusOK {
		t.Fatalf("lift: status %d", code)
	}
	if ok, _ := out["lifted"].(bool); ok {
		t.Fatalf("nothing was banned, so nothing was lifted: %v", out)
	}
}

// Banning somebody who is not online is ordinary: it takes effect at their
// next hello, and the reply says no session was ended.
func TestBanningAnOfflineAccount(t *testing.T) {
	h := newHarness(t)
	var out map[string]any
	if code := h.doAdmin("POST", "/bans", map[string]any{"steam_id": "111", "reason": "reported"}, &out); code != http.StatusOK {
		t.Fatalf("ban: status %d", code)
	}
	if ended, _ := out["session_ended"].(bool); ended {
		t.Fatal("there was no session to end")
	}

	code := h.do("POST", "/api/v1/client/hello", "", map[string]any{
		"steam_id": "111", "name": "merc", "side": "RED", "protocol_version": 3,
	}, nil)
	if code != http.StatusForbidden {
		t.Fatalf("hello after an offline ban: status %d, want 403", code)
	}
}

// A ban on somebody who is mid-battle has to reach the game server too. A kick
// the coordinator only records leaves the cheater playing.
func TestBanKicksFromTheBattleServer(t *testing.T) {
	h := newHarness(t)

	server := h.newAgent("kick-test", "10.0.0.5:27015")
	clients := []*client{
		h.newClient(111, "RED"), h.newClient(222, "RED"),
		h.newClient(333, "BLU"), h.newClient(444, "BLU"),
	}
	for _, c := range clients {
		c.deploy()
	}
	h.maker.Tick()

	cmd, ok := server.poll()
	if !ok || cmd.Assignment == nil {
		t.Fatal("the server pool was never handed the battle")
	}
	server.state(cmd.Assignment.MatchID, "ready")
	server.state(cmd.Assignment.MatchID, "live")

	if code := h.doAdmin("POST", "/bans", map[string]any{
		"steam_id": "111", "reason": "aimbot",
	}, nil); code != http.StatusOK {
		t.Fatalf("ban: status %d", code)
	}

	kick, ok := server.poll()
	if !ok {
		t.Fatal("the battle server was never told to kick the banned player")
	}
	if kick.Type != pool.CommandKick {
		t.Fatalf("server got %q, want a kick", kick.Type)
	}
	if kick.SteamID != 111 {
		t.Fatalf("kick names %d, want 111", kick.SteamID)
	}
	if kick.MatchID != cmd.Assignment.MatchID {
		t.Fatalf("kick is for battle %q, want %q", kick.MatchID, cmd.Assignment.MatchID)
	}
	if kick.Reason == "" {
		t.Fatal("the kicked player should be told why")
	}
}

// Nothing about the ban list is allowed to leak past the admin key.
func TestBanRoutesNeedTheAdminKey(t *testing.T) {
	h := newHarnessWithAdminKey(t, "sekrit")
	if code := h.doAdmin("GET", "/bans", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("ban list without the key: status %d, want 401", code)
	}
	if code := h.doAdmin("POST", "/bans", map[string]any{"steam_id": "111"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("ban without the key: status %d, want 401", code)
	}
}
