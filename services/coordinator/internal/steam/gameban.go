package steam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Game bans — "игровые блокировки" — are the publisher's own bans, issued
// through ICheatReportingService. They are a different thing from the
// coordinator's ban list and both are worth having:
//
//   - The coordinator's list is what actually turns somebody away. It is ours,
//     it answers instantly, and it works when the Steam Web API does not.
//   - A game ban is public. It shows on the account's Steam profile forever,
//     anyone can read it through ISteamUser/GetPlayerBans, and it follows the
//     account to every server of ours rather than living in one coordinator's
//     file.
//
// Steam enforces a game ban only on *secure servers for the same AppID*. Ours
// register as Source SDK Base 2013 DS (244310) while the client runs as
// 5147520, so Steam will not enforce it for us — the coordinator still has to.
// See docs/STEAM-SETUP.md for what would change that.
//
// Two calls, in order: ReportPlayerCheating produces a report id, and
// RequestPlayerGameBan turns that report into the ban. Both need the publisher
// key and go to partner.steam-api.com, not api.steampowered.com.

// partnerBaseURL is where publisher-key calls go. Sending them to
// api.steampowered.com returns 403, which reads like a bad key.
const partnerBaseURL = "https://partner.steam-api.com"

var (
	// ErrNoGameBans is a coordinator that has no publisher key or no AppID, so
	// it can record a ban but cannot put one on a Steam profile.
	ErrNoGameBans = errors.New("steam: game bans need a publisher web api key and an app id")
	// ErrGameBanRefused is Steam declining the call.
	ErrGameBanRefused = errors.New("steam: game ban refused")
)

// CheatReporter issues and removes Steam game bans.
type CheatReporter struct {
	Key     string
	AppID   uint32
	HTTP    *http.Client
	BaseURL string // overridable for tests
}

// NewCheatReporter builds a reporter. It is usable — and returns
// ErrNoGameBans from every call — when the key or the AppID is missing, so a
// dev coordinator does not have to special-case it everywhere.
func NewCheatReporter(key string, appID uint32) *CheatReporter {
	return &CheatReporter{
		Key:     key,
		AppID:   appID,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		BaseURL: partnerBaseURL,
	}
}

// Available reports whether this reporter can actually reach Steam.
func (c *CheatReporter) Available() bool {
	return c != nil && c.Key != "" && c.AppID != 0
}

// GameBanRequest is one account being banned.
type GameBanRequest struct {
	SteamID uint64
	// Description is what the ban is recorded as. Valve shows it to nobody but
	// the publisher, so it is for the next operator who asks "why".
	Description string
	// Duration is how long the ban lasts. Zero is permanent, which is what an
	// operator who says nothing means.
	Duration time.Duration
	// ReporterSteamID is optional: the account or game server that saw it.
	ReporterSteamID uint64
	// PlayerReport marks a ban that started as a player's complaint rather
	// than something the coordinator detected.
	PlayerReport bool
}

// Ban reports the account as cheating and turns that report into a game ban.
// It returns the report id, which Steam keys the ban on and which is worth
// keeping in the local record: it is the only handle on this ban in Valve's
// own tooling.
func (c *CheatReporter) Ban(ctx context.Context, req GameBanRequest) (reportID uint64, err error) {
	if !c.Available() {
		return 0, ErrNoGameBans
	}
	if req.SteamID == 0 {
		return 0, fmt.Errorf("%w: no SteamID", ErrGameBanRefused)
	}

	reportID, err = c.report(ctx, req)
	if err != nil {
		return 0, err
	}
	if err := c.requestBan(ctx, req, reportID); err != nil {
		// The report is already filed and stays visible in GetCheatingReports,
		// which is the right outcome for a half-finished ban: an operator can
		// see the accusation even though the ban did not land.
		return reportID, err
	}
	return reportID, nil
}

func (c *CheatReporter) report(ctx context.Context, req GameBanRequest) (uint64, error) {
	form := url.Values{}
	form.Set("key", c.Key)
	form.Set("appid", strconv.FormatUint(uint64(c.AppID), 10))
	form.Set("steamid", strconv.FormatUint(req.SteamID, 10))
	if req.ReporterSteamID != 0 {
		form.Set("steamidreporter", strconv.FormatUint(req.ReporterSteamID, 10))
	}
	// An operator banning by hand is neither a heuristic nor an automatic
	// detection, and saying otherwise would poison the only signal Valve's own
	// reporting has about how the ban was arrived at.
	form.Set("playerreport", boolParam(req.PlayerReport))
	form.Set("heuristic", "0")
	form.Set("detection", "0")
	form.Set("suspicionstarttime", strconv.FormatInt(time.Now().Unix(), 10))

	var body struct {
		Response struct {
			ReportID json.Number `json:"reportid"`
		} `json:"response"`
	}
	if err := c.post(ctx, "ReportPlayerCheating", form, &body); err != nil {
		return 0, err
	}
	id, err := strconv.ParseUint(body.Response.ReportID.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: no usable report id in the reply", ErrGameBanRefused)
	}
	return id, nil
}

func (c *CheatReporter) requestBan(ctx context.Context, req GameBanRequest, reportID uint64) error {
	description := strings.TrimSpace(req.Description)
	if description == "" {
		description = "banned by a GREYLINE operator"
	}
	if len(description) > 256 {
		description = description[:256]
	}

	form := url.Values{}
	form.Set("key", c.Key)
	form.Set("appid", strconv.FormatUint(uint64(c.AppID), 10))
	form.Set("steamid", strconv.FormatUint(req.SteamID, 10))
	form.Set("reportid", strconv.FormatUint(reportID, 10))
	form.Set("cheatdescription", description)
	// Steam takes seconds, and zero means permanent — the same thing an empty
	// duration means in the coordinator's own list.
	form.Set("duration", strconv.FormatInt(int64(req.Duration.Seconds()), 10))
	// delayban spreads bans out so a wave of them cannot be traced back to one
	// detection. That is for automatic anti-cheat; an operator banning by hand
	// wants it to happen now.
	form.Set("delayban", "0")
	form.Set("flags", "0")

	return c.post(ctx, "RequestPlayerGameBan", form, nil)
}

// Unban removes the game ban this AppID has on an account. Steam keeps no
// record of which ban is being removed, so there is nothing to pass but the
// account.
func (c *CheatReporter) Unban(ctx context.Context, steamID uint64) error {
	if !c.Available() {
		return ErrNoGameBans
	}
	if steamID == 0 {
		return fmt.Errorf("%w: no SteamID", ErrGameBanRefused)
	}
	form := url.Values{}
	form.Set("key", c.Key)
	form.Set("appid", strconv.FormatUint(uint64(c.AppID), 10))
	form.Set("steamid", strconv.FormatUint(steamID, 10))
	return c.post(ctx, "RemovePlayerGameBan", form, nil)
}

func (c *CheatReporter) post(ctx context.Context, method string, form url.Values, into any) error {
	endpoint := c.BaseURL + "/ICheatReportingService/" + method + "/v1/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("steam: %s unreachable: %w", method, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		// The single most common cause, and the one the status code alone
		// never explains: an ordinary account key instead of a publisher one,
		// or a publisher without Manage Signing on this app.
		return fmt.Errorf("%w: %s returned 403 — the key must be a *publisher* key "+
			"with Manage Signing on app %d", ErrGameBanRefused, method, c.AppID)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s returned 401 — the publisher key is not valid",
			ErrGameBanRefused, method)
	default:
		return fmt.Errorf("%w: %s returned %d", ErrGameBanRefused, method, resp.StatusCode)
	}

	if into == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("steam: bad %s reply: %w", method, err)
	}
	return nil
}

func boolParam(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
