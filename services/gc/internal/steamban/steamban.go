// Package steamban issues Steam game bans: ICheatReportingService, the
// publisher-only Web API behind "1 game ban on record" on a Steam profile.
//
// A ban is two calls: ReportPlayerCheating files a report and returns its
// reportid; RequestPlayerGameBan bans on that report. RemovePlayerGameBan
// lifts it. All three need the publisher Web API key, so they run only on the
// GC, never in a game client or server. Valve requires VAC support to be
// enabled for the app (Steamworks -> Anti-Cheat) before the service accepts
// calls; the game does not have to run VAC.
//
// The service methods take their arguments as one input_json form field.
package steamban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Report is what a ban is based on.
type Report struct {
	SteamID  uint64
	AppID    uint32
	Reporter uint64 // optional: who reported it (a player or a game server)
	// Where it came from: an automatic detection (Easy Anti-Cheat), a player
	// report, or a heuristic.
	Detection    bool
	PlayerReport bool
	Heuristic    bool
	// NoReportID files the report without asking for an id to ban on.
	NoReportID bool
	// AppData is ours to define: the report's detail bits (aimbot,
	// wallhack...), shown with the report in Steamworks.
	AppData uint64
}

// Ban is a ban request on a report.
type Ban struct {
	SteamID     uint64
	AppID       uint32
	ReportID    uint64
	Description string
	// Duration 0 is permanent and shows on the player's profile; under a
	// year is a temporary restriction that does not.
	Duration time.Duration
	// Delay applies Valve's default ban delay, so a cheat seller cannot tell
	// which game session got an account caught.
	Delay bool
}

// Client calls ICheatReportingService.
type Client struct {
	Key    string
	Base   string // tests; default https://partner.steam-api.com
	Client *http.Client
}

// Error is Steam refusing a call.
type Error struct {
	Method string
	Status int
	Body   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("steam %s: HTTP %d %s", e.Method, e.Status, e.Body)
}

// ReportPlayerCheating files a cheating report and returns its id (0 with
// NoReportID).
func (c *Client) ReportPlayerCheating(ctx context.Context, r Report) (uint64, error) {
	in := map[string]any{
		"steamid": strconv.FormatUint(r.SteamID, 10),
		"appid":   r.AppID,
	}
	if r.Reporter != 0 {
		in["steamidreporter"] = strconv.FormatUint(r.Reporter, 10)
	}
	if r.Detection {
		in["detection"] = true
	}
	if r.PlayerReport {
		in["playerreport"] = true
	}
	if r.Heuristic {
		in["heuristic"] = true
	}
	if r.NoReportID {
		in["noreportid"] = true
	}
	if r.AppData != 0 {
		in["appdata"] = strconv.FormatUint(r.AppData, 10)
	}
	var out struct {
		Response struct {
			ReportID json.Number `json:"reportid"`
		} `json:"response"`
	}
	if err := c.call(ctx, "ReportPlayerCheating", in, &out); err != nil {
		return 0, err
	}
	if r.NoReportID {
		return 0, nil
	}
	id, err := strconv.ParseUint(out.Response.ReportID.String(), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("steam ReportPlayerCheating: no reportid in the response")
	}
	return id, nil
}

// RequestPlayerGameBan bans a player on a report.
func (c *Client) RequestPlayerGameBan(ctx context.Context, b Ban) error {
	if b.ReportID == 0 {
		return errors.New("steam RequestPlayerGameBan: a ban needs a reportid")
	}
	return c.call(ctx, "RequestPlayerGameBan", map[string]any{
		"steamid":          strconv.FormatUint(b.SteamID, 10),
		"appid":            b.AppID,
		"reportid":         strconv.FormatUint(b.ReportID, 10),
		"cheatdescription": b.Description,
		"duration":         uint32(b.Duration / time.Second),
		"delayban":         b.Delay,
		"flags":            0,
	}, nil)
}

// RemovePlayerGameBan lifts a player's game ban in an app.
func (c *Client) RemovePlayerGameBan(ctx context.Context, steamID uint64, appID uint32) error {
	return c.call(ctx, "RemovePlayerGameBan", map[string]any{
		"steamid": strconv.FormatUint(steamID, 10),
		"appid":   appID,
	}, nil)
}

func (c *Client) call(ctx context.Context, method string, in map[string]any, into any) error {
	if c.Key == "" {
		return errors.New("steam game bans need a publisher Web API key")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	base := c.Base
	if base == "" {
		base = "https://partner.steam-api.com"
	}
	form := url.Values{"key": {c.Key}, "input_json": {string(raw)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/ICheatReportingService/"+method+"/v1/", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// Not the error itself: it can carry the URL, and the form has the key.
		return fmt.Errorf("steam %s: request failed", method)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return &Error{Method: method, Status: resp.StatusCode, Body: msg}
	}
	if into == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("steam %s: %w", method, err)
	}
	return nil
}
