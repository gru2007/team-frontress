// Package steamauth turns a Steam auth session ticket into a SteamID the
// coordinator is willing to act on.
//
// The distinction that matters: dev mode believes the client. WebAPI mode does
// not. Only the second produces identities the game may enforce a roster with,
// which is why Verifier.Verified() is plumbed all the way into the assignment.
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

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// ErrRejected is returned when Steam says the ticket is not good.
var ErrRejected = errors.New("steam rejected the auth ticket")

// Verifier checks tickets.
type Verifier interface {
	// Verify returns the SteamID the ticket actually belongs to. claimed is
	// what the client said it was; a verifier may use it (dev) or ignore it
	// entirely (webapi).
	Verify(ctx context.Context, claimed wire.SteamID, ticket string) (wire.SteamID, error)
	// Verified reports whether this verifier's answers are proof of identity.
	Verified() bool
}

// DevVerifier takes the client's word for it. Development and LAN only.
type DevVerifier struct{}

func (DevVerifier) Verified() bool { return false }

func (DevVerifier) Verify(_ context.Context, claimed wire.SteamID, _ string) (wire.SteamID, error) {
	if !ValidSteamID(claimed) {
		return "", fmt.Errorf("%w: %q is not a SteamID64", ErrRejected, claimed)
	}
	return claimed, nil
}

// WebAPIVerifier calls ISteamUserAuth/AuthenticateUserTicket.
type WebAPIVerifier struct {
	APIKey string
	AppID  uint32
	// BaseURL defaults to the public Steam API. Tests point it elsewhere.
	BaseURL string
	Client  *http.Client

	// cache keeps a verified ticket for a short while: a party of six polling
	// every two seconds would otherwise be six Steam calls a second.
	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	id      wire.SteamID
	expires time.Time
}

const cacheTTL = 2 * time.Minute

func (*WebAPIVerifier) Verified() bool { return true }

func (v *WebAPIVerifier) Verify(ctx context.Context, claimed wire.SteamID, ticket string) (wire.SteamID, error) {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return "", fmt.Errorf("%w: no ticket presented", ErrRejected)
	}
	if id, ok := v.cached(ticket); ok {
		return id, nil
	}

	base := v.BaseURL
	if base == "" {
		base = "https://api.steampowered.com"
	}
	q := url.Values{}
	q.Set("key", v.APIKey)
	q.Set("appid", strconv.FormatUint(uint64(v.AppID), 10))
	q.Set("ticket", ticket)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/ISteamUserAuth/AuthenticateUserTicket/v1/?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("steam auth: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("steam auth: HTTP %d", resp.StatusCode)
	}

	var body struct {
		Response struct {
			Params struct {
				Result          string `json:"result"`
				SteamID         string `json:"steamid"`
				OwnerSteamID    string `json:"ownersteamid"`
				VACBanned       bool   `json:"vacbanned"`
				PublisherBanned bool   `json:"publisherbanned"`
			} `json:"params"`
			Error *struct {
				ErrorCode int    `json:"errorcode"`
				ErrorDesc string `json:"errordesc"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("steam auth: %w", err)
	}
	if e := body.Response.Error; e != nil {
		return "", fmt.Errorf("%w: %s (%d)", ErrRejected, e.ErrorDesc, e.ErrorCode)
	}
	p := body.Response.Params
	if !strings.EqualFold(p.Result, "OK") {
		return "", fmt.Errorf("%w: result %q", ErrRejected, p.Result)
	}
	if p.PublisherBanned {
		return "", fmt.Errorf("%w: publisher ban", ErrRejected)
	}
	id := wire.SteamID(p.SteamID)
	if !ValidSteamID(id) {
		return "", fmt.Errorf("%w: steam returned %q", ErrRejected, p.SteamID)
	}
	if claimed != "" && claimed != id {
		// Not fatal to the ticket, but the client lied about who it is. The
		// verified identity wins and the caller can log the mismatch.
		return id, fmt.Errorf("%w: claimed %s, ticket belongs to %s", ErrRejected, claimed, id)
	}
	v.store(ticket, id)
	return id, nil
}

func (v *WebAPIVerifier) cached(ticket string) (wire.SteamID, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.cache[ticket]
	if !ok || time.Now().After(e.expires) {
		return "", false
	}
	return e.id, true
}

func (v *WebAPIVerifier) store(ticket string, id wire.SteamID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cache == nil {
		v.cache = map[string]cacheEntry{}
	}
	// Cheap bound: the cache is per-ticket and tickets rotate, so drop it all
	// rather than grow without limit.
	if len(v.cache) > 4096 {
		v.cache = map[string]cacheEntry{}
	}
	v.cache[ticket] = cacheEntry{id: id, expires: time.Now().Add(cacheTTL)}
}

// ValidSteamID reports whether s looks like an individual SteamID64.
//
// 76561197960265728 is the base of the individual account range; anything below
// it is not a player, whatever else it may be.
func ValidSteamID(s wire.SteamID) bool {
	n, err := strconv.ParseUint(string(s), 10, 64)
	if err != nil {
		return false
	}
	return n >= 76561197960265728
}
