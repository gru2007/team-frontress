package mm

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

type timeoutBackend struct{ forceEnded bool }

func (b *timeoutBackend) CreateGame(context.Context, BackendGameRequest) (BackendGame, error) {
	return BackendGame{ExternalMatchID: "0123456789abcdef", State: "created"}, nil
}
func (b *timeoutBackend) AddPlayers(context.Context, string, []wire.AssignedPlayer) (BackendGame, error) {
	return BackendGame{}, nil
}
func (b *timeoutBackend) Game(context.Context, string) (BackendGame, error) {
	return BackendGame{ExternalMatchID: "0123456789abcdef", State: "created"}, nil
}
func (b *timeoutBackend) Ratings(context.Context, []wire.SteamID) (map[wire.SteamID]int, error) {
	return nil, nil
}
func (b *timeoutBackend) ActiveGame(context.Context, wire.SteamID) (BackendGame, bool, error) {
	return BackendGame{}, false, nil
}
func (b *timeoutBackend) ActiveGames(context.Context) ([]BackendGame, error) { return nil, nil }
func (b *timeoutBackend) ForceEnd(context.Context, string) (BackendGame, error) {
	b.forceEnded = true
	return BackendGame{State: "interrupted"}, nil
}

func TestBackendBootTimeoutForceEndsBeforeRequeue(t *testing.T) {
	cfg := config.Defaults()
	cfg.Pool.BootDeadlineSecs = 1
	backend := &timeoutBackend{}
	m := NewBackend(cfg, backend, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Now()
	ticket := &Ticket{
		ID: "ticket", MatchGroup: wire.MatchGroupCasual12v12,
		state: tsMatched, queuedAt: now, lastPoll: now, matchID: "0123456789abcdef",
	}
	match := &Match{
		ID: "0123456789abcdef", MatchGroup: wire.MatchGroupCasual12v12,
		Map: "koth_product_final", state: msWaitingServer,
		createdAt: now.Add(-2 * time.Second), tickets: []string{ticket.ID},
	}
	m.tickets[ticket.ID] = ticket
	m.matches[match.ID] = match

	m.bootBackend(context.Background(), match)

	if !backend.forceEnded {
		t.Fatal("backend game was not force-ended")
	}
	if ticket.state != tsSearching {
		t.Fatalf("ticket state = %v, want searching", ticket.state)
	}
}
