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

	cmds := []string{
		fmt.Sprintf("sv_password %s", quote(spec.Password)),
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

	// Hand the match itself over, if the server is one of ours.
	//
	// tf_mm_match_begin builds a real lobby object in the server's own shared
	// object cache, which is what makes CTFGCServerSystem create a CMatchInfo
	// -- and a CMatchInfo is what the whole server-side half of matchmaking
	// hangs off: the roster gate, team assignment, abandon tracking, the match
	// HUD, the match summary and the result the game itself reports. It also
	// changes the map, from the lobby, which is why nothing does it afterwards.
	//
	// It goes last because it takes the password off: the roster is the door
	// from that point on, and the password is only a fallback for a server
	// that could not raise the gate. See CTFMMServer::BeginMatch.
	//
	// A server that does not have the command answers "Unknown command", and
	// then we change the map ourselves -- so an unmodified dedicated server
	// still runs matches, just as a passworded community server with none of
	// the above.
	if roster := rosterArg(spec.Roster); roster != "" {
		begin := fmt.Sprintf("tf_mm_match_begin %s %d %s %s %s %s",
			quote(spec.MatchID),
			int(spec.MatchGroup),
			quote(spec.Map),
			quote(spec.ServerConfig),
			quote(spec.Password),
			quote(roster))
		out, err := c.Exec(begin)
		if err != nil {
			return fmt.Errorf("rcon %q: %w", "tf_mm_match_begin", err)
		}
		if !strings.Contains(strings.ToLower(out), "unknown command") {
			return nil
		}
	}

	if _, err := c.Exec(fmt.Sprintf("changelevel %s", spec.Map)); err != nil {
		return fmt.Errorf("rcon %q: %w", "changelevel", err)
	}
	return nil
}

const matchAddOKPrefix = "TFMM_MATCH_ADD_OK"

// classifyMatchAddReply distinguishes an old unmodified server (which has no
// roster gate and therefore needs no admission command) from one of our servers
// that understood the command but failed to update its lobby.
func classifyMatchAddReply(out string) (supported, accepted bool) {
	if strings.Contains(strings.ToLower(out), "unknown command") {
		return false, false
	}
	return true, strings.Contains(out, matchAddOKPrefix)
}

// AddPlayers announces new seats in a running match.
//
// A server that does not know the command says so, and that is not an error: it
// has no roster gate either, so the players it was never told about will get in
// on the password like they always did.
func (r *RCONSetup) AddPlayers(ctx context.Context, s *pool.Server, matchID string, roster []wire.AssignedPlayer) error {
	seats := rosterArg(roster)
	if seats == "" {
		return nil
	}

	c, err := r.dial(ctx, s)
	if err != nil {
		return err
	}
	defer c.Close()

	cmd := fmt.Sprintf("tf_mm_match_add %s %s", quote(matchID), quote(seats))
	out, err := c.Exec(cmd)
	if err != nil {
		return fmt.Errorf("rcon %q: %w", "tf_mm_match_add", err)
	}

	supported, accepted := classifyMatchAddReply(out)
	if !supported {
		return nil
	}
	if accepted {
		return nil
	}
	reply := strings.TrimSpace(out)
	if reply == "" {
		reply = "<empty response>"
	}
	return fmt.Errorf("rcon %q was not acknowledged: %s", "tf_mm_match_add", reply)
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

	// The match goes back before anything else: tf_mm_match_end takes down the
	// lobby, the roster gate and the official flag together, and a server
	// handed back still gating on a roster nobody holds is a server the next
	// match cannot use. An unmodified server does not have the command and
	// says so, which is not an error here.
	if _, err := c.Exec("tf_mm_match_end returned"); err != nil {
		return fmt.Errorf("rcon %q: %w", "tf_mm_match_end", err)
	}

	// tf_match_emulation goes off with the match. A returned server that still
	// thinks it is running an official match shows the match HUD to whoever
	// lands on it next.
	for _, cmd := range []string{"sv_password \"\"", "sv_tags \"\"", "tf_match_emulation 0", "tf_mm_trusted 0", "kickall"} {
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

// rosterArg packs the roster into the one argument tf_mm_match_begin takes:
// "steamid:team,steamid:team". Names are left out on purpose -- they are
// player-controlled text going into a console command, and the server does not
// need them for anything the gate does.
func rosterArg(roster []wire.AssignedPlayer) string {
	var b strings.Builder
	for _, p := range roster {
		if p.SteamID == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s:%d", p.SteamID, int(p.Team))
	}
	return b.String()
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
