// Package inventory supplies econ shared objects to the Frontress GC.
//
// Sources are server-side and composable.  The first implementation delegates
// Valve/TF2 ownership to TC2's existing Steam-authenticated SDK endpoint.  A
// playtest-app Steam Inventory Service source can be added behind Source and
// merged here without changing the game client or the GC wire envelope.
package inventory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
	"google.golang.org/protobuf/proto"
)

const econItemType = 1

type Request struct {
	SteamID wire.SteamID
	AppID   uint32
	Ticket  string
}

// Source returns one complete inventory snapshot. Implementations may obtain
// it from TC2, Steam Inventory Service for our playtest AppID, or another
// trusted store. The coordinator, never the client, decides which sources win.
type Source interface {
	Load(context.Context, Request) (*gcproto.CMsgSOCacheSubscribed, error)
}

type TC2SDK struct {
	Endpoint string
	Client   *http.Client
}

type tc2Response struct {
	Result  int             `json:"result"`
	SteamID json.RawMessage `json:"steamID"`
	Version uint64          `json:"version"`
	Message string          `json:"msg"`
	Error   string          `json:"error"`
}

func (s *TC2SDK) Load(ctx context.Context, in Request) (*gcproto.CMsgSOCacheSubscribed, error) {
	if s == nil || s.Endpoint == "" {
		return nil, errors.New("TC2 inventory endpoint is disabled")
	}
	if in.Ticket == "" || in.AppID == 0 {
		return nil, errors.New("inventory ticket and app_id are required")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("inventory endpoint: %w", err)
	}
	q := u.Query()
	q.Set("appid", strconv.FormatUint(uint64(in.AppID), 10))
	q.Set("ticket", in.Ticket)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TC2 inventory request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TC2 inventory response: HTTP %d", resp.StatusCode)
	}
	var result tc2Response
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("TC2 inventory JSON: %w", err)
	}
	if result.Result != 1 {
		return nil, fmt.Errorf("TC2 inventory rejected request (result %d): %s", result.Result, result.Error)
	}
	owner, err := responseSteamID(result.SteamID)
	if err != nil || owner != string(in.SteamID) {
		return nil, fmt.Errorf("TC2 inventory owner mismatch")
	}
	raw, err := base64.StdEncoding.DecodeString(result.Message)
	if err != nil {
		return nil, fmt.Errorf("TC2 inventory message: %w", err)
	}
	cache := new(gcproto.CMsgSOCacheSubscribed)
	if err := proto.Unmarshal(raw, cache); err != nil {
		return nil, fmt.Errorf("TC2 inventory protobuf: %w", err)
	}
	owner64, _ := strconv.ParseUint(string(in.SteamID), 10, 64)
	cache.Owner = proto.Uint64(owner64)
	if result.Version != 0 {
		cache.Version = proto.Uint64(result.Version)
	}
	// Never allow this upstream to smuggle party/lobby state into the GC.
	var econ []*gcproto.CMsgSOCacheSubscribed_SubscribedType
	for _, typ := range cache.Objects {
		if typ.GetTypeId() == econItemType {
			econ = append(econ, typ)
		}
	}
	if len(econ) == 0 {
		econ = []*gcproto.CMsgSOCacheSubscribed_SubscribedType{{TypeId: proto.Int32(econItemType)}}
	}
	cache.Objects = econ
	return cache, nil
}

func responseSteamID(raw json.RawMessage) (string, error) {
	value := strings.TrimSpace(string(raw))
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		var out string
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", err
		}
		return out, nil
	}
	if value == "" || value == "null" {
		return "", errors.New("missing steamID")
	}
	return value, nil
}
