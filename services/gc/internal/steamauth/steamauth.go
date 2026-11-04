// Package steamauth turns a Steam web API ticket into a SteamID the GC is
// willing to act on.
//
// Dev mode believes the client. WebAPI mode asks Steam
// (ISteamUserAuth/AuthenticateUserTicket) and is the only mode whose
// identities mean anything.
package steamauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrRejected means the ticket is not good.
var ErrRejected = errors.New("steam rejected the auth ticket")

// MinIndividual is the bottom of the individual-account SteamID64 range.
const MinIndividual uint64 = 76561197960265728

// ValidIndividual reports whether id is a player's SteamID64.
func ValidIndividual(id uint64) bool {
	return id >= MinIndividual && id < MinIndividual+(1<<32)
}

// Verifier checks tickets.
type Verifier interface {
	// Verify returns the SteamID the ticket belongs to. claimed is what the
	// client said it is.
	Verify(ctx context.Context, claimed uint64, ticket string) (uint64, error)
	// Verified reports whether answers are proof of identity.
	Verified() bool
}

// Dev takes the client's word for it. LAN and development only.
type Dev struct{}

func (Dev) Verified() bool { return false }

func (Dev) Verify(_ context.Context, claimed uint64, _ string) (uint64, error) {
	if !ValidIndividual(claimed) {
		return 0, fmt.Errorf("%w: %d is not a player's SteamID64", ErrRejected, claimed)
	}
	return claimed, nil
}

// WebAPI asks Steam.
type WebAPI struct {
	APIKey string
	// AppIDs are the apps whose tickets are accepted. The same build ships as
	// a playtest and as the main app, and a ticket is only good for the app
	// its client runs as, so each is tried until one recognises the ticket.
	AppIDs []uint32
	// Identity is what the game passed to GetAuthTicketForWebApi.
	Identity string
	BaseURL  string
	Client   *http.Client

	mu       sync.Mutex
	lastGood uint32
}

func (*WebAPI) Verified() bool { return true }

func (v *WebAPI) Verify(ctx context.Context, claimed uint64, ticket string) (uint64, error) {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return 0, fmt.Errorf("%w: no ticket", ErrRejected)
	}
	var rejected error
	for _, appID := range v.ordered() {
		id, err := v.verifyWith(ctx, appID, ticket)
		switch {
		case err == nil:
			v.mu.Lock()
			v.lastGood = appID
			v.mu.Unlock()
			if claimed != 0 && claimed != id {
				return 0, fmt.Errorf("%w: claimed %d, ticket belongs to %d", ErrRejected, claimed, id)
			}
			return id, nil
		case errors.Is(err, ErrRejected):
			// Steam says "invalid ticket", not "wrong app": only final once
			// every app has turned it down.
			if rejected == nil {
				rejected = err
			}
		default:
			return 0, err
		}
	}
	if rejected == nil {
		rejected = errors.New("steam auth: no app_id configured")
	}
	return 0, rejected
}

func (v *WebAPI) ordered() []uint32 {
	v.mu.Lock()
	first := v.lastGood
	v.mu.Unlock()
	out := make([]uint32, 0, len(v.AppIDs))
	for _, id := range v.AppIDs {
		if id != 0 && id == first {
			out = append(out, id)
		}
	}
	for _, id := range v.AppIDs {
		if id != 0 && id != first {
			out = append(out, id)
		}
	}
	return out
}

func (v *WebAPI) verifyWith(ctx context.Context, appID uint32, ticket string) (uint64, error) {
	base := v.BaseURL
	if base == "" {
		base = "https://api.steampowered.com"
	}
	q := url.Values{}
	q.Set("key", v.APIKey)
	q.Set("appid", strconv.FormatUint(uint64(appID), 10))
	q.Set("ticket", ticket)
	if v.Identity != "" {
		q.Set("identity", v.Identity)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/ISteamUserAuth/AuthenticateUserTicket/v1/?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("steam auth: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("steam auth: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Response struct {
			Params struct {
				Result          string `json:"result"`
				SteamID         string `json:"steamid"`
				PublisherBanned bool   `json:"publisherbanned"`
			} `json:"params"`
			Error *struct {
				ErrorCode int    `json:"errorcode"`
				ErrorDesc string `json:"errordesc"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("steam auth: %w", err)
	}
	if e := body.Response.Error; e != nil {
		return 0, fmt.Errorf("%w: %s (%d)", ErrRejected, e.ErrorDesc, e.ErrorCode)
	}
	p := body.Response.Params
	if !strings.EqualFold(p.Result, "OK") {
		return 0, fmt.Errorf("%w: result %q", ErrRejected, p.Result)
	}
	if p.PublisherBanned {
		return 0, fmt.Errorf("%w: publisher ban", ErrRejected)
	}
	id, err := strconv.ParseUint(p.SteamID, 10, 64)
	if err != nil || !ValidIndividual(id) {
		return 0, fmt.Errorf("%w: steam returned %q", ErrRejected, p.SteamID)
	}
	return id, nil
}
