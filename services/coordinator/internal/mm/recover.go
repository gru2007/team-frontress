package mm

import (
	"context"
	"fmt"

	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// Hydrate restores all durable backend matches before matchmaking is exposed.
func (m *Matchmaker) Hydrate(ctx context.Context) error {
	if m.backend == nil {
		return nil
	}
	games, err := m.backend.ActiveGames(ctx)
	if err != nil {
		return fmt.Errorf("list active tf2pickup games: %w", err)
	}
	var pending []*Match
	m.mu.Lock()
	for _, game := range games {
		if _, exists := m.matches[game.ExternalMatchID]; exists {
			continue
		}
		mt := m.matchFromBackend(game)
		m.matches[mt.ID] = mt
		if mt.state == msWaitingServer {
			pending = append(pending, mt)
		}
	}
	m.mu.Unlock()
	for _, mt := range pending {
		go m.awaitBackendReady(ctx, mt)
	}
	return nil
}

// RecoverActive restores an ephemeral ticket around a durable game.
func (m *Matchmaker) RecoverActive(ctx context.Context, group wire.MatchGroup, leader wire.SteamID, party []wire.AssignedPlayer) (*Ticket, bool, error) {
	if m.backend == nil {
		return nil, false, nil
	}
	game, found, err := m.backend.ActiveGame(ctx, leader)
	if err != nil || !found {
		return nil, found, err
	}
	if game.MatchGroup != group {
		return nil, true, fmt.Errorf("%w: player is already active in match group %d", ErrActiveGameConflict, game.MatchGroup)
	}
	rosterTeams := make(map[wire.SteamID]wire.Team, len(game.Players))
	for _, player := range game.Players {
		rosterTeams[player.SteamID] = player.Team
	}
	for i := range party {
		team, ok := rosterTeams[party[i].SteamID]
		if !ok {
			return nil, true, fmt.Errorf("%w: party member %s is not in the active game's roster", ErrActiveGameConflict, party[i].SteamID)
		}
		party[i].Team = team
	}

	m.mu.Lock()
	mt := m.matches[game.ExternalMatchID]
	startWatcher := false
	if mt == nil {
		mt = m.matchFromBackend(game)
		m.matches[mt.ID] = mt
		startWatcher = mt.state == msWaitingServer
	}
	key := leaderKey{leader: leader, group: group}
	if old := m.tickets[m.byLeader[key]]; old != nil {
		m.mu.Unlock()
		return old, true, nil
	}
	now := m.now()
	ticket := &Ticket{
		ID: m.newID(), MatchGroup: group, Leader: leader, Players: party,
		state: tsMatched, queuedAt: now, lastPoll: now, matchID: mt.ID,
	}
	mt.tickets = append(mt.tickets, ticket.ID)
	if mt.state == msLive {
		ticket.state = tsAssigned
		ticket.assignment = m.assignmentLocked(mt, teamOf(mt, ticket), false)
	}
	m.tickets[ticket.ID] = ticket
	m.byLeader[key] = ticket.ID
	m.mu.Unlock()

	if startWatcher {
		go m.awaitBackendReady(context.Background(), mt)
	}
	return ticket, true, nil
}

func (m *Matchmaker) matchFromBackend(game BackendGame) *Match {
	now := m.now()
	createdAt := game.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	mt := &Match{
		ID: game.ExternalMatchID, MatchGroup: game.MatchGroup, Map: game.Map,
		MaxPlayers: game.MaxPlayers, Password: game.Password,
		createdAt: createdAt, lastNonEmpty: now,
	}
	for _, player := range game.Players {
		mt.Players = append(mt.Players, wire.AssignedPlayer{SteamID: player.SteamID, Team: player.Team})
		if player.Connected {
			mt.players++
		}
	}
	if game.Ready() {
		mt.state = msLive
		mt.Server = &pool.Server{Provider: "tf2pickup", Connect: game.Connect, STV: game.STV, Ephemeral: true}
		mt.startedAt = game.ReadyAt
		if mt.startedAt.IsZero() {
			mt.startedAt = game.StartedAt
		}
		if mt.startedAt.IsZero() {
			mt.startedAt = now
		}
	} else {
		mt.state = msWaitingServer
		mt.waitDetail = "Match found. Waiting for tf2pickup to start a server."
	}
	return mt
}
