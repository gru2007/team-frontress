// Package steam verifies that a client connecting to the coordinator really is
// the SteamID it claims to be.
//
// Two independent checks exist in the system and they cover different attacks:
//
//  1. This package — the coordinator validates the client's auth session ticket
//     with Steam. It proves identity on the coordinator link and is what gates
//     queueing, war progress and bans.
//
//  2. Peer corroboration in the match layer — SteamNetworkingSockets
//     authenticates the identity of every P2P connection on its own, so the
//     host reports the SteamIDs Steam actually handed it and the coordinator
//     cross-checks them against the roster. Even if the coordinator link were
//     spoofed, a forged identity cannot complete a match.
//
// Ticket validation needs a *publisher* Web API key for the AppID the game
// ships under — Team Frontress Playtest, 5147520. Without one, run
// auth_mode=dev on a trusted network and rely on check 2.
//
// The ticket has to come from ISteamUser::GetAuthTicketForWebApi. Steam's own
// header says a GetAuthSessionTicket ticket is "not to be used for
// ISteamUserAuth\AuthenticateUserTicket - it will fail", and it fails with the
// same rejection a forged ticket gets, so a client on the wrong call looks
// exactly like an attack. See docs/STEAM-SETUP.md.
package steam

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Identity is the outcome of a successful authentication.
type Identity struct {
	SteamID      uint64
	OwnerSteamID uint64 // differs from SteamID under Family Sharing
	VACBanned    bool
	PubBanned    bool
	Verified     bool // false in dev mode: identity is asserted, not proven
}

var (
	ErrTicketInvalid = errors.New("steam: auth ticket rejected")
	ErrBanned        = errors.New("steam: account is banned")
	ErrNotOwner      = errors.New("steam: ticket does not prove ownership of the app")
)

// Authenticator verifies a claimed SteamID against an auth ticket.
type Authenticator interface {
	Authenticate(ctx context.Context, claimedSteamID uint64, ticket []byte) (*Identity, error)
	Mode() string
}

// ---------------------------------------------------------------------------
// dev
// ---------------------------------------------------------------------------

// DevAuthenticator trusts whatever the client asserts. LAN / local testing only.
type DevAuthenticator struct{}

func (DevAuthenticator) Mode() string { return "dev" }

func (DevAuthenticator) Authenticate(_ context.Context, claimed uint64, _ []byte) (*Identity, error) {
	if claimed == 0 {
		return nil, fmt.Errorf("%w: no SteamID asserted", ErrTicketInvalid)
	}
	return &Identity{SteamID: claimed, OwnerSteamID: claimed, Verified: false}, nil
}

// ---------------------------------------------------------------------------
// Steam Web API
// ---------------------------------------------------------------------------

// WebAPIAuthenticator validates tickets through
// ISteamUserAuth/AuthenticateUserTicket.
type WebAPIAuthenticator struct {
	Key   string
	AppID uint32
	// Identity is the string the client passed to
	// ISteamUser::GetAuthTicketForWebApi. Steam binds the ticket to it, so a
	// mismatch here is rejected exactly like a forged ticket — and the failure
	// looks identical, which is why both halves are configured from one place.
	Identity         string
	RejectBanned     bool
	RequireOwnership bool
	HTTP             *http.Client
	BaseURL          string // overridable for tests
	CacheTTL         time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	id      *Identity
	err     error
	expires time.Time
}

func NewWebAPIAuthenticator(key string, appID uint32, identity string, rejectBanned, requireOwnership bool, cacheTTL time.Duration) *WebAPIAuthenticator {
	return &WebAPIAuthenticator{
		Key:              key,
		AppID:            appID,
		Identity:         identity,
		RejectBanned:     rejectBanned,
		RequireOwnership: requireOwnership,
		HTTP:             &http.Client{Timeout: 8 * time.Second},
		BaseURL:          "https://api.steampowered.com",
		CacheTTL:         cacheTTL,
		cache:            make(map[string]cacheEntry),
	}
}

func (a *WebAPIAuthenticator) Mode() string { return "webapi" }

type authTicketResponse struct {
	Response struct {
		Params struct {
			Result          string `json:"result"`
			SteamID         string `json:"steamid"`
			OwnerSteamID    string `json:"ownersteamid"`
			VACBanned       bool   `json:"vacbanned"`
			PublisherBanned bool   `json:"publisherbanned"`
		} `json:"params"`
		Error *struct {
			Code int    `json:"errorcode"`
			Desc string `json:"errordesc"`
		} `json:"error"`
	} `json:"response"`
}

func (a *WebAPIAuthenticator) Authenticate(ctx context.Context, claimed uint64, ticket []byte) (*Identity, error) {
	if len(ticket) == 0 {
		return nil, fmt.Errorf("%w: empty ticket", ErrTicketInvalid)
	}
	hexTicket := hex.EncodeToString(ticket)

	if id, err, ok := a.lookup(hexTicket); ok {
		return a.finish(claimed, id, err)
	}

	id, err := a.validate(ctx, hexTicket)
	a.store(hexTicket, id, err)
	return a.finish(claimed, id, err)
}

// finish applies the claimed-vs-actual check outside the cache, so a cached
// positive result cannot be replayed by a different account.
func (a *WebAPIAuthenticator) finish(claimed uint64, id *Identity, err error) (*Identity, error) {
	if err != nil {
		return nil, err
	}
	if claimed != 0 && claimed != id.SteamID {
		return nil, fmt.Errorf("%w: ticket belongs to %d, client claimed %d", ErrTicketInvalid, id.SteamID, claimed)
	}
	out := *id
	return &out, nil
}

func (a *WebAPIAuthenticator) validate(ctx context.Context, hexTicket string) (*Identity, error) {
	q := url.Values{}
	q.Set("key", a.Key)
	q.Set("appid", strconv.FormatUint(uint64(a.AppID), 10))
	q.Set("ticket", hexTicket)
	if a.Identity != "" {
		q.Set("identity", a.Identity)
	}

	endpoint := a.BaseURL + "/ISteamUserAuth/AuthenticateUserTicket/v1/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("steam: web api unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steam: web api status %d", resp.StatusCode)
	}

	var body authTicketResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("steam: bad web api response: %w", err)
	}
	if body.Response.Error != nil {
		return nil, fmt.Errorf("%w: %s (%d)", ErrTicketInvalid,
			body.Response.Error.Desc, body.Response.Error.Code)
	}
	p := body.Response.Params
	if p.Result != "OK" {
		return nil, fmt.Errorf("%w: result=%s", ErrTicketInvalid, p.Result)
	}
	sid, err := strconv.ParseUint(p.SteamID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: unparseable steamid %q", ErrTicketInvalid, p.SteamID)
	}
	owner := sid
	if p.OwnerSteamID != "" {
		if o, err := strconv.ParseUint(p.OwnerSteamID, 10, 64); err == nil {
			owner = o
		}
	}
	id := &Identity{
		SteamID:      sid,
		OwnerSteamID: owner,
		VACBanned:    p.VACBanned,
		PubBanned:    p.PublisherBanned,
		Verified:     true,
	}
	if a.RejectBanned && (id.VACBanned || id.PubBanned) {
		return nil, fmt.Errorf("%w: vac=%v publisher=%v", ErrBanned, id.VACBanned, id.PubBanned)
	}
	if a.RequireOwnership && id.OwnerSteamID == 0 {
		return nil, ErrNotOwner
	}
	return id, nil
}

func (a *WebAPIAuthenticator) lookup(key string) (*Identity, error, bool) {
	if a.CacheTTL <= 0 {
		return nil, nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.cache[key]
	if !ok || time.Now().After(e.expires) {
		return nil, nil, false
	}
	return e.id, e.err, true
}

func (a *WebAPIAuthenticator) store(key string, id *Identity, err error) {
	if a.CacheTTL <= 0 {
		return
	}
	// Only cache decisive outcomes. Transport failures must be retried, or a
	// blip in Steam connectivity would lock players out for the whole TTL.
	if err != nil && !errors.Is(err, ErrTicketInvalid) && !errors.Is(err, ErrBanned) && !errors.Is(err, ErrNotOwner) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) > 8192 {
		a.cache = make(map[string]cacheEntry)
	}
	a.cache[key] = cacheEntry{id: id, err: err, expires: time.Now().Add(a.CacheTTL)}
}
