package mm

import (
	"context"
	"errors"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

var ErrActiveGameConflict = errors.New("party conflicts with an active game")

// MatchBackend owns durable games, server allocation, RCON and result detection.
// The matchmaker only decides the roster and follows the resulting game state.
type MatchBackend interface {
	CreateGame(context.Context, BackendGameRequest) (BackendGame, error)
	AddPlayers(context.Context, string, []wire.AssignedPlayer) (BackendGame, error)
	Game(context.Context, string) (BackendGame, error)
	Ratings(context.Context, []wire.SteamID) (map[wire.SteamID]int, error)
	ActiveGame(context.Context, wire.SteamID) (BackendGame, bool, error)
	ActiveGames(context.Context) ([]BackendGame, error)
	ForceEnd(context.Context, string) (BackendGame, error)
}

// Ratings loads durable matchmaking ratings without making the API layer know
// which backend stores them.
func (m *Matchmaker) Ratings(ctx context.Context, ids []wire.SteamID) (map[wire.SteamID]int, error) {
	if m.backend == nil {
		return nil, nil
	}
	return m.backend.Ratings(ctx, ids)
}

type BackendGameRequest struct {
	ExternalMatchID string
	Map             string
	MatchGroup      wire.MatchGroup
	MatchMode       string
	MaxPlayers      int
	ServerConfig    string
	MatchEmulation  int
	Players         []wire.AssignedPlayer
}

type BackendGame struct {
	ExternalMatchID string
	Map             string
	MatchGroup      wire.MatchGroup
	MaxPlayers      int
	CreatedAt       time.Time
	ReadyAt         time.Time
	StartedAt       time.Time
	State           string
	Connect         string
	Password        string
	STV             string
	RedScore        int
	BluScore        int
	Players         []BackendPlayer
}

type BackendPlayer struct {
	SteamID   wire.SteamID
	Team      wire.Team
	Connected bool
}

func (g BackendGame) Ready() bool {
	return (g.State == "launching" || g.State == "started") && g.Connect != ""
}

func (g BackendGame) Over() bool { return g.State == "ended" || g.State == "interrupted" }
