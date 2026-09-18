// Package tf2pickup is the small private-API client used by the Frontress gateway.
package tf2pickup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

type Client struct {
	base   *url.URL
	secret string
	http   *http.Client
}

type HTTPError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("tf2pickup %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

func (e *HTTPError) Temporary() bool {
	return e.Status == http.StatusRequestTimeout || e.Status == http.StatusTooManyRequests || e.Status >= 500
}

func New(baseURL, secret string) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("tf2pickup base_url %q is not an absolute URL", baseURL)
	}
	return &Client{base: base, secret: secret, http: &http.Client{Timeout: 20 * time.Second}}, nil
}

func (c *Client) CreateGame(ctx context.Context, game mm.BackendGameRequest) (mm.BackendGame, error) {
	body := map[string]any{
		"externalMatchId": game.ExternalMatchID,
		"map":             game.Map,
		"matchGroup":      game.MatchGroup,
		"matchMode":       game.MatchMode,
		"maxPlayers":      game.MaxPlayers,
		"serverConfig":    game.ServerConfig,
		"matchEmulation":  game.MatchEmulation,
		"players":         encodePlayers(game.Players),
	}
	var result response
	if err := c.do(ctx, http.MethodPost, "/api/frontress/v1/games", body, &result); err != nil {
		return mm.BackendGame{}, err
	}
	return result.backend(), nil
}

func (c *Client) AddPlayers(ctx context.Context, matchID string, players []wire.AssignedPlayer) (mm.BackendGame, error) {
	var result response
	path := "/api/frontress/v1/games/" + url.PathEscape(matchID) + "/players"
	if err := c.do(ctx, http.MethodPost, path, map[string]any{"players": encodePlayers(players)}, &result); err != nil {
		return mm.BackendGame{}, err
	}
	return result.backend(), nil
}

func (c *Client) Game(ctx context.Context, matchID string) (mm.BackendGame, error) {
	var result response
	path := "/api/frontress/v1/games/" + url.PathEscape(matchID)
	if err := c.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return mm.BackendGame{}, err
	}
	return result.backend(), nil
}

func (c *Client) Ratings(ctx context.Context, ids []wire.SteamID) (map[wire.SteamID]int, error) {
	var result struct {
		Ratings []struct {
			SteamID wire.SteamID `json:"steamId"`
			Rating  int          `json:"rating"`
		} `json:"ratings"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/frontress/v1/ratings", map[string]any{"steamIds": ids}, &result); err != nil {
		return nil, err
	}
	out := make(map[wire.SteamID]int, len(result.Ratings))
	for _, rating := range result.Ratings {
		out[rating.SteamID] = rating.Rating
	}
	return out, nil
}

func (c *Client) ActiveGame(ctx context.Context, steamID wire.SteamID) (mm.BackendGame, bool, error) {
	var result struct {
		Game *response `json:"game"`
	}
	path := "/api/frontress/v1/players/" + url.PathEscape(string(steamID)) + "/active-game"
	if err := c.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return mm.BackendGame{}, false, err
	}
	if result.Game == nil {
		return mm.BackendGame{}, false, nil
	}
	return result.Game.backend(), true, nil
}

func (c *Client) ActiveGames(ctx context.Context) ([]mm.BackendGame, error) {
	var result struct {
		Games []response `json:"games"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/frontress/v1/games", nil, &result); err != nil {
		return nil, err
	}
	games := make([]mm.BackendGame, 0, len(result.Games))
	for _, game := range result.Games {
		games = append(games, game.backend())
	}
	return games, nil
}

func (c *Client) ForceEnd(ctx context.Context, matchID string) (mm.BackendGame, error) {
	var result response
	path := "/api/frontress/v1/games/" + url.PathEscape(matchID) + "/force-end"
	if err := c.do(ctx, http.MethodPut, path, struct{}{}, &result); err != nil {
		return mm.BackendGame{}, err
	}
	return result.backend(), nil
}

type requestPlayer struct {
	SteamID wire.SteamID `json:"steamId"`
	Name    string       `json:"name,omitempty"`
	Team    string       `json:"team"`
}

func encodePlayers(players []wire.AssignedPlayer) []requestPlayer {
	out := make([]requestPlayer, 0, len(players))
	for _, player := range players {
		team := "red"
		if player.Team == wire.TeamBlu {
			team = "blu"
		}
		out = append(out, requestPlayer{SteamID: player.SteamID, Name: player.Name, Team: team})
	}
	return out
}

type response struct {
	ExternalMatchID string          `json:"externalMatchId"`
	Map             string          `json:"map"`
	MatchGroup      wire.MatchGroup `json:"matchGroup"`
	MaxPlayers      int             `json:"maxPlayers"`
	CreatedAt       *time.Time      `json:"createdAt"`
	ReadyAt         *time.Time      `json:"readyAt"`
	StartedAt       *time.Time      `json:"startedAt"`
	State           string          `json:"state"`
	Score           *struct {
		Red int `json:"red"`
		Blu int `json:"blu"`
	} `json:"score"`
	Server *struct {
		Connect  string  `json:"connect"`
		Password string  `json:"password"`
		STV      *string `json:"stv"`
	} `json:"server"`
	Players []struct {
		SteamID          wire.SteamID `json:"steamId"`
		Team             string       `json:"team"`
		ConnectionStatus string       `json:"connectionStatus"`
	} `json:"players"`
}

func (r response) backend() mm.BackendGame {
	g := mm.BackendGame{
		ExternalMatchID: r.ExternalMatchID,
		Map:             r.Map,
		MatchGroup:      r.MatchGroup,
		MaxPlayers:      r.MaxPlayers,
		State:           r.State,
	}
	if r.StartedAt != nil {
		g.StartedAt = *r.StartedAt
	}
	if r.CreatedAt != nil {
		g.CreatedAt = *r.CreatedAt
	}
	if r.ReadyAt != nil {
		g.ReadyAt = *r.ReadyAt
	}
	if r.Score != nil {
		g.RedScore, g.BluScore = r.Score.Red, r.Score.Blu
	}
	if r.Server != nil {
		g.Connect, g.Password = r.Server.Connect, r.Server.Password
		if r.Server.STV != nil {
			g.STV = *r.Server.STV
		}
	}
	for _, player := range r.Players {
		team := wire.TeamRed
		if player.Team == "blu" {
			team = wire.TeamBlu
		}
		g.Players = append(g.Players, mm.BackendPlayer{
			SteamID:   player.SteamID,
			Team:      team,
			Connected: player.ConnectionStatus == "connected" || player.ConnectionStatus == "joining",
		})
	}
	return g
}

func (c *Client) do(ctx context.Context, method, path string, body any, into any) error {
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, input)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "secret "+c.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tf2pickup %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Method: method, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("tf2pickup %s %s: decode response: %w", method, path, err)
	}
	return nil
}
