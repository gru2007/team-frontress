// Command greyline-ban is the operator's console for bans.
//
//	greyline-ban list
//	greyline-ban add    <steamid64> -reason "aimbot" [-for 72h] [-steam] [-by gru]
//	greyline-ban lift   <steamid64> [-by gru] [-reason "appealed"]
//	greyline-ban check  <steamid64>
//
// It talks to the coordinator's admin listener, which is loopback by default —
// so this normally runs on the coordinator's own machine, and reaches a remote
// one over an SSH tunnel or with -admin pointed at a bound address.
//
// Two bans, deliberately, and -steam is what asks for the second:
//
//   - The coordinator's own ban is what turns the player away. It is instant,
//     it is ours, and it works when Steam does not.
//   - A Steam game ban — an "игровая блокировка" — is public: it shows on the
//     account's profile, anyone can read it through GetPlayerBans, and it
//     follows the account rather than living in one coordinator's file. It is
//     also, as a record, forever, even after it is lifted. That is the right
//     weight for cheating and much too much for rage-quitting a battle, which
//     is why it is off unless asked for.
//
// -for sets the length of both. An empty -for is a permanent ban, in both.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "greyline-ban:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}

	cmd := args[0]
	fs := flag.NewFlagSet("greyline-ban "+cmd, flag.ContinueOnError)
	var (
		admin  = fs.String("admin", envOr("GREYLINE_ADMIN", "http://127.0.0.1:27101"), "coordinator admin address")
		key    = fs.String("key", os.Getenv("GREYLINE_ADMIN_KEY"), "admin key, when the admin listener has one")
		reason = fs.String("reason", "", "why (shown to the player, and recorded)")
		by     = fs.String("by", envOr("GREYLINE_OPERATOR", ""), "who is doing this, for the record")
		forDur = fs.String("for", "", "how long, e.g. 30m, 72h. Empty is permanent")
		steam  = fs.Bool("steam", false, "also place a Steam game ban on the account's profile")
	)

	switch cmd {
	case "list", "add", "lift", "check":
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}

	// Go's flag package stops parsing at the first non-flag argument, so
	// `add <steamid> -reason "..."` — which is the natural way to type it, and
	// what the usage text shows — would silently drop every flag after the
	// SteamID. Lift the target out first and let the rest be flags, so both
	// orders work.
	target, rest := splitTarget(args[1:])
	if err := fs.Parse(rest); err != nil {
		return err
	}

	c := &client{admin: strings.TrimRight(*admin, "/"), key: *key}

	switch cmd {
	case "list":
		return c.list()
	case "check":
		id, err := steamID(target)
		if err != nil {
			return err
		}
		return c.check(id)
	case "add":
		id, err := steamID(target)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*reason) == "" {
			// A ban with no reason is one nobody can defend in three months,
			// including the person who placed it.
			return errors.New("-reason is required: the player is shown it, and so is the next operator")
		}
		if *forDur != "" {
			if _, err := time.ParseDuration(*forDur); err != nil {
				return fmt.Errorf("-for %q: %w (try 30m, 12h, 168h)", *forDur, err)
			}
		}
		return c.add(id, *reason, *forDur, *by, *steam)
	case "lift":
		id, err := steamID(target)
		if err != nil {
			return err
		}
		return c.lift(id, *by, *reason)
	}
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `greyline-ban — the coordinator's ban console

  greyline-ban list
  greyline-ban check <steamid64>
  greyline-ban add   <steamid64> -reason "aimbot" [-for 72h] [-steam] [-by name]
  greyline-ban lift  <steamid64> [-by name] [-reason "appealed"]

Flags:
  -admin   coordinator admin address (default http://127.0.0.1:27101, $GREYLINE_ADMIN)
  -key     admin key, if the listener has one ($GREYLINE_ADMIN_KEY)
  -reason  why. Required on add; optional note on lift
  -for     how long: 30m, 72h, 168h. Empty means permanent
  -steam   also place a Steam game ban on the account's Steam profile
  -by      who is doing this, for the record ($GREYLINE_OPERATOR)

A -steam ban is public and stays on the profile as a record even after it is
lifted. Use it for cheating, not for conduct.
`)
}

// splitTarget pulls the SteamID64 out of an argument list, wherever it sits,
// and returns everything else for the flag package.
//
// It is picked by shape rather than by position: a SteamID64 is the only
// argument that parses as a number in the individual-account range, so it can
// never be confused with a flag's value — `-reason 76561198…` is not a thing
// anyone types, and `-reason aimbot` is not a number.
func splitTarget(args []string) (target string, rest []string) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		if target == "" && looksLikeSteamID(a) {
			target = a
			continue
		}
		rest = append(rest, a)
	}
	return target, rest
}

func looksLikeSteamID(s string) bool {
	id, err := strconv.ParseUint(s, 10, 64)
	return err == nil && id >= steamIDFloor
}

// steamIDFloor is the start of the individual-account range. Anything below it
// is a SteamID3, an account id, or a typo — and banning a number that is not
// an account is a ban that silently does nothing.
const steamIDFloor = 76561197960265728

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func steamID(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("a SteamID64 is required")
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id < steamIDFloor {
		return 0, fmt.Errorf("%q is not a SteamID64 (they are 17 digits and start 7656119…)", raw)
	}
	return id, nil
}

type client struct {
	admin string
	key   string
}

func (c *client) do(method, path string, body any, into any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.admin+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the coordinator at %s: %w\n"+
			"The admin listener is loopback by default; run this on the coordinator's "+
			"machine, or point -admin at where it is bound", c.admin, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s (HTTP %d)", e.Error, resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, path)
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// banView mirrors what the admin endpoint serves.
type banView struct {
	SteamID       uint64    `json:"steam_id,string"`
	Reason        string    `json:"reason"`
	Source        string    `json:"source"`
	IssuedBy      string    `json:"issued_by"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Permanent     bool      `json:"permanent"`
	RemainingS    int       `json:"remaining_s"`
	SteamGameBan  bool      `json:"steam_game_ban"`
	SteamReportID uint64    `json:"steam_report_id,string"`
}

func (b banView) line() string {
	left := "permanent"
	if !b.Permanent {
		left = (time.Duration(b.RemainingS) * time.Second).String() + " left"
	}
	return fmt.Sprintf("%-17d  %-9s  %-14s  %-6s  %s",
		b.SteamID, b.Source, left, steamMark(b), b.Reason)
}

func steamMark(b banView) string {
	if b.SteamGameBan {
		return "steam"
	}
	return "local"
}

func (c *client) list() error {
	var out struct {
		Bans []banView `json:"bans"`
	}
	if err := c.do("GET", "/bans", nil, &out); err != nil {
		return err
	}
	if len(out.Bans) == 0 {
		fmt.Println("nobody is banned")
		return nil
	}
	fmt.Printf("%-17s  %-9s  %-14s  %-6s  %s\n", "STEAMID64", "SOURCE", "LEFT", "WHERE", "REASON")
	for _, b := range out.Bans {
		fmt.Println(b.line())
	}
	return nil
}

func (c *client) check(id uint64) error {
	var out struct {
		Bans []banView `json:"bans"`
	}
	if err := c.do("GET", "/bans", nil, &out); err != nil {
		return err
	}
	for _, b := range out.Bans {
		if b.SteamID == id {
			fmt.Println(b.line())
			if b.SteamReportID != 0 {
				fmt.Printf("steam report id: %d\n", b.SteamReportID)
			}
			return nil
		}
	}
	fmt.Printf("%d is not banned\n", id)
	return nil
}

func (c *client) add(id uint64, reason, forDur, by string, steam bool) error {
	var out struct {
		Ban          banView `json:"ban"`
		SessionEnded bool    `json:"session_ended"`
		SteamError   string  `json:"steam_error"`
	}
	err := c.do("POST", "/bans", map[string]any{
		"steam_id":  strconv.FormatUint(id, 10),
		"reason":    reason,
		"duration":  forDur,
		"issued_by": by,
		"steam":     steam,
	}, &out)
	if err != nil {
		return err
	}

	fmt.Println(out.Ban.line())
	if out.SessionEnded {
		fmt.Println("their session was ended and they were kicked from any battle they were in")
	}
	if steam {
		if out.SteamError != "" {
			// Loud, and a non-zero exit: the local ban landed, the public one
			// did not, and an operator who does not notice will believe the
			// account is game-banned when it is not.
			return fmt.Errorf("the coordinator ban is in force, but the Steam game ban failed: %s", out.SteamError)
		}
		fmt.Printf("steam game ban placed (report %d)\n", out.Ban.SteamReportID)
	}
	return nil
}

func (c *client) lift(id uint64, by, reason string) error {
	var out struct {
		Lifted     bool   `json:"lifted"`
		Message    string `json:"message"`
		SteamGone  bool   `json:"steam_game_ban_removed"`
		SteamError string `json:"steam_error"`
	}
	err := c.do("POST", "/bans/lift", map[string]any{
		"steam_id": strconv.FormatUint(id, 10),
		"by":       by,
		"reason":   reason,
	}, &out)
	if err != nil {
		return err
	}
	if !out.Lifted {
		fmt.Println(out.Message)
		return nil
	}
	fmt.Printf("%d unbanned\n", id)
	if out.SteamGone {
		fmt.Println("the Steam game ban was removed too")
	}
	if out.SteamError != "" {
		return fmt.Errorf("the coordinator ban is lifted, but the Steam game ban is still on the profile: %s", out.SteamError)
	}
	return nil
}
