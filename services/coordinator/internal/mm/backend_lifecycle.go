package mm

import (
	"context"
	"fmt"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/pool"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

func (m *Matchmaker) bootBackend(ctx context.Context, mt *Match) {
	group, _ := m.cfg.Group(mt.MatchGroup)
	request := BackendGameRequest{
		ExternalMatchID: mt.ID,
		Map:             mt.Map,
		MatchGroup:      mt.MatchGroup,
		MatchMode:       string(group.EffectiveMode()),
		MaxPlayers:      matchCapacity(mt, group),
		ServerConfig:    group.ServerConfig,
		MatchEmulation:  group.EffectiveMatchEmulation(),
		Players:         append([]wire.AssignedPlayer(nil), mt.Players...),
	}

	m.mu.Lock()
	mt.state = msWaitingServer
	mt.waitDetail = "Match found. Waiting for tf2pickup to start a server."
	m.mu.Unlock()

	bootCtx, cancel := context.WithDeadline(ctx, mt.createdAt.Add(m.cfg.Pool.BootDeadline()))
	defer cancel()
	var game BackendGame
	for {
		var err error
		game, err = m.backend.CreateGame(bootCtx, request)
		if err == nil {
			break
		}
		m.log.Warn("could not persist match in tf2pickup", "match", mt.ID, "err", err)
		if !waitForBackendRetry(bootCtx) {
			if ctx.Err() == nil {
				m.abortBackendMatch(ctx, mt, fmt.Errorf("tf2pickup did not accept the match before the boot deadline"))
			}
			return
		}
	}
	m.awaitBackendGame(ctx, bootCtx, mt, game)
}

// awaitBackendReady watches a game restored from the durable backend. It does
// not recreate it because the original request may predate current config.
func (m *Matchmaker) awaitBackendReady(ctx context.Context, mt *Match) {
	bootCtx, cancel := context.WithDeadline(ctx, mt.createdAt.Add(m.cfg.Pool.BootDeadline()))
	defer cancel()
	game, err := m.backend.Game(bootCtx, mt.ID)
	if err != nil {
		m.log.Warn("could not read restored tf2pickup game", "match", mt.ID, "err", err)
	}
	m.awaitBackendGame(ctx, bootCtx, mt, game)
}

func (m *Matchmaker) awaitBackendGame(ctx, bootCtx context.Context, mt *Match, game BackendGame) {
	for {
		if game.Ready() {
			m.publishBackendGame(mt, game)
			return
		}
		if game.Over() {
			m.failMatch(mt, fmt.Errorf("tf2pickup ended the match before a server became ready"), false)
			return
		}
		if !waitForBackendRetry(bootCtx) {
			if ctx.Err() == nil {
				m.abortBackendMatch(ctx, mt, fmt.Errorf("tf2pickup server boot exceeded %s", m.cfg.Pool.BootDeadline()))
			}
			return
		}
		var err error
		game, err = m.backend.Game(bootCtx, mt.ID)
		if err != nil {
			m.log.Warn("tf2pickup game is not available yet", "match", mt.ID, "err", err)
		}
	}
}

func waitForBackendRetry(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(2 * time.Second):
		return true
	}
}

func (m *Matchmaker) abortBackendMatch(ctx context.Context, mt *Match, cause error) {
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		game, err := m.backend.ForceEnd(requestCtx, mt.ID)
		cancel()
		if err == nil && game.Over() {
			m.failMatch(mt, cause, true)
			return
		}
		if err == nil {
			err = fmt.Errorf("force-end returned non-final state %q", game.State)
		}
		m.log.Error("could not force-end timed out tf2pickup game", "match", mt.ID, "err", err)
		if !waitForBackendRetry(ctx) {
			return
		}
	}
}

func (m *Matchmaker) publishBackendGame(mt *Match, game BackendGame) {
	now := m.now()
	m.mu.Lock()
	if mt.state == msOver || mt.state == msLive {
		m.mu.Unlock()
		return
	}
	mt.Server = &pool.Server{Provider: "tf2pickup", Connect: game.Connect, STV: game.STV, Ephemeral: true}
	mt.Password = game.Password
	mt.state = msLive
	mt.waitDetail = ""
	mt.startedAt = game.ReadyAt
	if mt.startedAt.IsZero() {
		mt.startedAt = now
	}
	mt.lastNonEmpty = now
	for _, id := range mt.tickets {
		if ticket := m.tickets[id]; ticket != nil {
			ticket.state = tsAssigned
			ticket.assignment = m.assignmentLocked(mt, teamOf(mt, ticket), false)
		}
	}
	m.mu.Unlock()
	m.log.Info("match handed to tf2pickup", "match", mt.ID, "server", game.Connect)
}

func (m *Matchmaker) superviseBackendMatches(ctx context.Context) {
	now := m.now()
	m.mu.Lock()
	var matches []*Match
	for _, mt := range m.matches {
		if mt.state == msLive && now.Sub(mt.lastPolled) >= 5*time.Second {
			mt.lastPolled = now
			matches = append(matches, mt)
		}
	}
	m.mu.Unlock()

	for _, mt := range matches {
		game, err := m.backend.Game(ctx, mt.ID)
		if err != nil {
			m.log.Warn("could not read match from tf2pickup", "match", mt.ID, "err", err)
			continue
		}
		connected := 0
		present := make([]wire.AssignedPlayer, 0, len(game.Players))
		for _, player := range game.Players {
			if player.Connected {
				connected++
				present = append(present, wire.AssignedPlayer{SteamID: player.SteamID, Team: player.Team})
			}
		}
		m.mu.Lock()
		mt.players = connected
		if connected > 0 {
			mt.lastNonEmpty = now
			mt.everSeenPlayers = true
		}
		m.mu.Unlock()
		if !game.Over() {
			continue
		}
		winner := wire.TeamUnassigned
		if game.RedScore > game.BluScore {
			winner = wire.TeamRed
		} else if game.BluScore > game.RedScore {
			winner = wire.TeamBlu
		}
		m.endMatch(ctx, mt, &wire.MatchResult{
			MatchID:  mt.ID,
			Winner:   winner,
			RedScore: game.RedScore,
			BluScore: game.BluScore,
			Aborted:  game.State == "interrupted",
			Players:  present,
		}, "reported by tf2pickup")
	}
}
