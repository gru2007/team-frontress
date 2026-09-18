package mm

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

type recoveryBackend struct{ game BackendGame }

func (b recoveryBackend) CreateGame(context.Context, BackendGameRequest) (BackendGame, error) {
	return b.game, nil
}
func (b recoveryBackend) AddPlayers(context.Context, string, []wire.AssignedPlayer) (BackendGame, error) {
	return b.game, nil
}
func (b recoveryBackend) Game(context.Context, string) (BackendGame, error) { return b.game, nil }
func (b recoveryBackend) Ratings(context.Context, []wire.SteamID) (map[wire.SteamID]int, error) {
	return nil, nil
}
func (b recoveryBackend) ActiveGame(context.Context, wire.SteamID) (BackendGame, bool, error) {
	return b.game, true, nil
}
func (b recoveryBackend) ActiveGames(context.Context) ([]BackendGame, error) {
	return []BackendGame{b.game}, nil
}
func (b recoveryBackend) ForceEnd(context.Context, string) (BackendGame, error) {
	b.game.State = "interrupted"
	return b.game, nil
}

func TestRecoverActiveRestoresAssignment(t *testing.T) {
	const player = wire.SteamID("76561198000000001")
	cfg := config.Defaults()
	backend := recoveryBackend{game: BackendGame{
		ExternalMatchID: "durable-match", Map: "koth_product_final",
		MatchGroup: wire.MatchGroupCasual12v12, MaxPlayers: 24,
		State: "started", Connect: "10.0.0.1:27015", Password: "secret",
		Players: []BackendPlayer{{SteamID: player, Team: wire.TeamRed, Connected: true}},
	}}
	m := NewBackend(cfg, backend, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ticket, found, err := m.RecoverActive(context.Background(), wire.MatchGroupCasual12v12, player, []wire.AssignedPlayer{{SteamID: player}})
	if err != nil || !found {
		t.Fatalf("RecoverActive() = found %v, err %v", found, err)
	}
	status, err := m.Status(ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != wire.QueueStateAssigned || status.Assignment == nil {
		t.Fatalf("status = %#v, want assigned", status)
	}
	if status.Assignment.MatchID != "durable-match" || status.Assignment.Connect != "10.0.0.1:27015" {
		t.Fatalf("assignment = %#v", status.Assignment)
	}
	if status.Assignment.Team != wire.TeamRed {
		t.Fatalf("team = %v, want RED", status.Assignment.Team)
	}
}

func TestRecoverActiveRejectsDifferentMatchGroup(t *testing.T) {
	const player = wire.SteamID("76561198000000001")
	backend := recoveryBackend{game: BackendGame{
		ExternalMatchID: "durable-match",
		MatchGroup:      wire.MatchGroupCasual12v12,
		State:           "started",
		Players:         []BackendPlayer{{SteamID: player, Team: wire.TeamRed}},
	}}
	m := NewBackend(config.Defaults(), backend, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, found, err := m.RecoverActive(context.Background(), wire.MatchGroupLadder6v6, player, []wire.AssignedPlayer{{SteamID: player}})
	if err == nil || !found {
		t.Fatalf("RecoverActive() = found %v, err %v; want an active-game conflict", found, err)
	}
}

func TestRecoverActiveRejectsPartyMemberOutsideRoster(t *testing.T) {
	const player = wire.SteamID("76561198000000001")
	const teammate = wire.SteamID("76561198000000002")
	backend := recoveryBackend{game: BackendGame{
		ExternalMatchID: "durable-match",
		MatchGroup:      wire.MatchGroupCasual12v12,
		State:           "started",
		Players:         []BackendPlayer{{SteamID: player, Team: wire.TeamRed}},
	}}
	m := NewBackend(config.Defaults(), backend, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	party := []wire.AssignedPlayer{{SteamID: player}, {SteamID: teammate}}
	_, found, err := m.RecoverActive(context.Background(), wire.MatchGroupCasual12v12, player, party)
	if err == nil || !found {
		t.Fatalf("RecoverActive() = found %v, err %v; want a roster conflict", found, err)
	}
}

func TestHydrateRestoresActiveMatchesBeforeAPlayerQueues(t *testing.T) {
	const player = wire.SteamID("76561198000000001")
	backend := recoveryBackend{game: BackendGame{
		ExternalMatchID: "durable-match", Map: "koth_product_final",
		MatchGroup: wire.MatchGroupCasual12v12, MaxPlayers: 24,
		State: "started", Connect: "10.0.0.1:27015",
		Players: []BackendPlayer{{SteamID: player, Team: wire.TeamRed, Connected: true}},
	}}
	m := NewBackend(config.Defaults(), backend, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := m.Hydrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.LiveMatches(); got != 1 {
		t.Fatalf("LiveMatches() = %d, want 1", got)
	}
	if got := m.Population(); got != 1 {
		t.Fatalf("Population() = %d, want 1", got)
	}
	if got := m.OpenMatches()[wire.MatchGroupCasual12v12]; got != 1 {
		t.Fatalf("OpenMatches()[casual] = %d, want 1", got)
	}
}
