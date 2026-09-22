package mm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/rcon"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// RCONSetup drives a game server over RCON. It is the production ServerSetup.
//
// Everything it sends is a stock Source or TF convar, so it works against an
// unmodified dedicated server. The match id is written into sv_tags as
// "tfmm:<id>" — a server-side agent can read it back from A2S or from the
// server's own convar without the coordinator needing a custom protocol.
type RCONSetup struct {
	// Timeout bounds a single RCON connection.
	Timeout time.Duration
	// Hostname is a printf-style template with one %s, the match id. Empty
	// leaves the server's hostname alone.
	Hostname string
}

// NewRCONSetup returns a setup with sensible timeouts.
func NewRCONSetup(hostname string) *RCONSetup {
	return &RCONSetup{Timeout: 15 * time.Second, Hostname: hostname}
}

// Setup prepares the server and changes the map.
func (r *RCONSetup) Setup(ctx context.Context, s *pool.Server, spec Spec) error {
	c, err := r.dial(ctx, s)
	if err != nil {
		return err
	}
	defer c.Close()

	// Real Valve matchmaking servers never carry sv_password -- the GC hands
	// players a direct lobby join instead, and the roster the GC gave the
	// server is the only door. spec.RosterViaGC means this server's own GC
	// session is about to receive that same roster object (see
	// Matchmaker.pushServerRoster / gcpusher.go), so we grant it the same
	// deal: no password, native roster gate only. A server with no GC
	// identity configured never gets that push, so it keeps the password as
	// its only door, same as an unmodified dedicated server would.
	pw := spec.Password
	if spec.RosterViaGC {
		pw = ""
	}

	cmds := []string{
		fmt.Sprintf("sv_password %s", quote(pw)),
		fmt.Sprintf("sv_tags %s", quote("tfmm:"+spec.MatchID)),
		fmt.Sprintf("maxplayers %d", spec.MaxPlayers),
		// Official-match status, granted per match rather than baked into the
		// server: tf_match_emulation is what makes CTFGameRules report a match
		// group, which is what turns on the match HUD, the ready-up and
		// tournament handling a group's ruleset asks for, and the match
		// summary at game over. Without it a matchmade server is a community
		// server that happens to have the right twelve people on it.
		//
		// The two companions are not optional. restartmatch 0 makes game over
		// end the match instead of silently starting another one on the same
		// map, and randommap 0 keeps map choice where it belongs -- with the
		// coordinator, which picked this one and will pick the next.
		fmt.Sprintf("tf_match_emulation %d", spec.MatchEmulation),
		"tf_match_emulation_restartmatch 0",
		"tf_match_emulation_randommap 0",
		// tf_mm_trusted is the game's own "this is an official server" flag.
		// On Valve's build it is checked by their backend; there is no backend
		// here, so it is ours to grant, and we grant it for the duration of a
		// match and take it away again. It is FCVAR_NOTIFY, so it travels to
		// clients, and CServerGameDLL::GetServerBrowserGameData publishes it
		// as server browser game data. Server-side it makes returning players
		// go back to the team they left and stops a spectator slot being used
		// to unbalance the sides.
		fmt.Sprintf("tf_mm_trusted %d", boolInt(spec.MatchEmulation != 0)),
		// tf_mm_servermode/tf_mm_strict are the other half of "official
		// server": they are what make CTFGCServerSystem set m_bMMServerMode
		// and turn SteamIDAllowedToConnect from "anybody may join" into
		// "only the roster this server's GC session was given may join" (see
		// tf_gc_server.cpp). Granting them without RosterViaGC would lock the
		// server, since SteamIDAllowedToConnect returns false with no
		// CMatchInfo to check against -- so this only ever turns on together
		// with dropping the password above, never on its own.
		fmt.Sprintf("tf_mm_servermode %d", boolInt(spec.RosterViaGC)),
		fmt.Sprintf("tf_mm_strict %d", boolInt(spec.RosterViaGC)),
	}
	if r.Hostname != "" {
		cmds = append(cmds, fmt.Sprintf("hostname %s", quote(fmt.Sprintf(r.Hostname, spec.MatchID))))
	}
	if spec.ServerConfig != "" {
		cmds = append(cmds, fmt.Sprintf("exec %s", spec.ServerConfig))
	}

	// Everything above has to be in place before the map changes, whichever
	// way the map ends up changing.
	for _, cmd := range cmds {
		if _, err := c.Exec(cmd); err != nil {
			return fmt.Errorf("rcon %q: %w", firstWord(cmd), err)
		}
	}

	if _, err := c.Exec(fmt.Sprintf("changelevel %s", spec.Map)); err != nil {
		return fmt.Errorf("rcon %q: %w", "changelevel", err)
	}
	return nil
}

// AddPlayers is now a deliberate no-op. Seating a backfilled or standby
// player into a running match happens over the native GC transport (see
// Matchmaker.pushServerRoster in gcpusher.go), which pushes the match's full,
// current CSOTFGameServerLobby -- the same shared object CTFGCServerSystem
// already builds CMatchInfo's roster gate from. There is deliberately no
// bespoke RCON command here for a plugin (or a plugin-shaped stand-in) to
// answer: an unmodified dedicated server never saw one, and this coordinator
// does not add one either.
func (r *RCONSetup) AddPlayers(ctx context.Context, s *pool.Server, matchID string, roster []wire.AssignedPlayer) error {
	return nil
}

// PlayerCount asks the server how many humans are on it.
func (r *RCONSetup) PlayerCount(ctx context.Context, s *pool.Server) (int, bool) {
	c, err := r.dial(ctx, s)
	if err != nil {
		return 0, false
	}
	defer c.Close()

	out, err := c.Exec("status")
	if err != nil {
		return 0, false
	}
	return rcon.StatusPlayers(out)
}

// Teardown clears the match's password and tag so a returned server is not
// left locked behind a password nobody has.
func (r *RCONSetup) Teardown(ctx context.Context, s *pool.Server) error {
	if s.Ephemeral {
		return nil // it is about to be destroyed
	}
	c, err := r.dial(ctx, s)
	if err != nil {
		return err
	}
	defer c.Close()

	// There is no RCON match-end command to send here: the lobby object a
	// server holds is torn down over GC instead (Matchmaker.clearServerRoster,
	// called by the mm layer around this Teardown, not by RCONSetup itself --
	// RCONSetup only ever sends stock convars). tf_match_emulation still goes
	// off here: a returned server that thinks it is running an official match
	// shows the match HUD to whoever lands on it next. tf_mm_servermode and
	// tf_mm_strict go off with it -- a server sitting in the pool with no
	// match roster must not keep SteamIDAllowedToConnect's gate up, or
	// nobody, matched or not, could ever connect to it again. sv_password
	// clears unconditionally even though a GC-identified server ran without
	// one, so a returned server is never left locked behind a password
	// nobody has.
	for _, cmd := range []string{"sv_password \"\"", "sv_tags \"\"", "tf_match_emulation 0", "tf_mm_trusted 0", "tf_mm_servermode 0", "tf_mm_strict 0", "kickall"} {
		if _, err := c.Exec(cmd); err != nil {
			return fmt.Errorf("rcon %q: %w", firstWord(cmd), err)
		}
	}
	return nil
}

func (r *RCONSetup) dial(ctx context.Context, s *pool.Server) (*rcon.Conn, error) {
	if s.RCON == "" {
		return nil, fmt.Errorf("server %s has no rcon password configured", s.Connect)
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left > 0 && left < timeout {
			timeout = left
		}
	}
	return rcon.Dial(pool.RCONAddr(s), s.RCON, timeout)
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`"`, "", "\n", "", ";", "").Replace(s) + `"`
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}
