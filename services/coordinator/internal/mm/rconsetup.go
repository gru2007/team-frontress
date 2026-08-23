package mm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/rcon"
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
	}
	if r.Hostname != "" {
		cmds = append(cmds, fmt.Sprintf("hostname %s", quote(fmt.Sprintf(r.Hostname, spec.MatchID))))
	}
	if spec.ServerConfig != "" {
		cmds = append(cmds, fmt.Sprintf("exec %s", spec.ServerConfig))
	}
	// The map change goes last: everything above must be in place when the
	// first player connects.
	cmds = append(cmds, fmt.Sprintf("changelevel %s", spec.Map))

	for _, cmd := range cmds {
		if _, err := c.Exec(cmd); err != nil {
			return fmt.Errorf("rcon %q: %w", firstWord(cmd), err)
		}
	}
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

	for _, cmd := range []string{"sv_password \"\"", "sv_tags \"\"", "kickall"} {
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

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}
