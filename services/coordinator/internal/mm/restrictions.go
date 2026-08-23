package mm

import (
	"errors"
	"fmt"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/players"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// ErrRestricted is the queue refusing a party on the group's own terms rather
// than because anything went wrong. The API answers it with 403, and the
// client shows the reason: "you need 5 matches", not "bad request".
var ErrRestricted = errors.New("restricted")

// RestrictionError says which rule refused, and about whom. The reason is
// player-facing text.
type RestrictionError struct {
	Reason string
	Player wire.SteamID
}

func (e *RestrictionError) Error() string { return e.Reason }
func (e *RestrictionError) Unwrap() error { return ErrRestricted }

// PlayerHistory is the part of players.Store the queue gate reads. Nil is a
// coordinator with no records, where history-based restrictions cannot be
// enforced and are therefore not.
type PlayerHistory interface {
	Get(wire.SteamID) players.Record
}

// PlayerStore is the whole of it: the gate reads, finished matches write.
type PlayerStore interface {
	PlayerHistory
	Played(p wire.AssignedPlayer, matchID, result string)
	Abandoned(p wire.AssignedPlayer, matchID string)
}

// UsePlayers gives the matchmaker the record store that history-based
// restrictions read, and that finished matches are written to.
func (m *Matchmaker) UsePlayers(h PlayerStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.players = h
}

// checkRestrictions decides whether this party may queue for this group.
//
// Party size is checked first because it is about the party, not the players:
// telling a six-stack that one of its members lacks matches, when the party
// was never going to fit anyway, is the wrong answer to the wrong question.
func checkRestrictions(g config.MatchGroupConfig, t *Ticket, hist PlayerHistory, verified bool, now time.Time) error {
	r := g.Restrictions

	if cap := g.PartyCap(); t.Size() > cap {
		if r.MaxPartySize > 0 && r.MaxPartySize == cap {
			if cap == 1 {
				return &RestrictionError{Reason: fmt.Sprintf("%s is solo queue only", g.Name)}
			}
			return &RestrictionError{Reason: fmt.Sprintf("%s takes parties of at most %d", g.Name, cap)}
		}
		return &RestrictionError{Reason: fmt.Sprintf("a party of %d cannot fit one team in %s", t.Size(), g.Name)}
	}
	if r.MinPartySize > 0 && t.Size() < r.MinPartySize {
		return &RestrictionError{Reason: fmt.Sprintf("%s is queued as a team of %d or more", g.Name, r.MinPartySize)}
	}
	if r.RequireVerifiedAuth && !verified {
		// Config validation refuses this combination, so reaching it means the
		// coordinator was reconfigured underneath a running queue.
		return &RestrictionError{Reason: fmt.Sprintf("%s needs a verified Steam identity, which this coordinator cannot check", g.Name)}
	}

	allowed := map[wire.SteamID]bool{}
	for _, id := range r.AllowedSteamIDs {
		allowed[id] = true
	}
	banned := map[wire.SteamID]bool{}
	for _, id := range r.BannedSteamIDs {
		banned[id] = true
	}

	for _, p := range t.Players {
		who := playerLabel(p)
		if len(allowed) > 0 && !allowed[p.SteamID] {
			return &RestrictionError{Player: p.SteamID, Reason: fmt.Sprintf("%s is not on the list for %s", who, g.Name)}
		}
		if banned[p.SteamID] {
			return &RestrictionError{Player: p.SteamID, Reason: fmt.Sprintf("%s is banned from %s", who, g.Name)}
		}

		// Everything below is history, and history needs a store.
		if hist == nil {
			continue
		}
		rec := hist.Get(p.SteamID)
		if n := r.MinMatchesPlayed; n > 0 && rec.Matches < n {
			return &RestrictionError{
				Player: p.SteamID,
				Reason: fmt.Sprintf("%s needs %d finished matches for %s and has %d", who, n, g.Name, rec.Matches),
			}
		}
		if n := r.MaxAbandons; n > 0 && rec.Abandons >= n {
			return &RestrictionError{
				Player: p.SteamID,
				Reason: fmt.Sprintf("%s abandoned %d matches and is out of %s", who, rec.Abandons, g.Name),
			}
		}
		if mins := r.AbandonCooldownMins; mins > 0 && !rec.LastAbandon.IsZero() {
			until := rec.LastAbandon.Add(time.Duration(mins) * time.Minute)
			if now.Before(until) {
				left := until.Sub(now).Round(time.Minute)
				if left < time.Minute {
					left = time.Minute
				}
				return &RestrictionError{
					Player: p.SteamID,
					Reason: fmt.Sprintf("%s left a match early and can queue for %s again in %s", who, g.Name, left),
				}
			}
		}
	}
	return nil
}

// playerLabel is what a refusal calls someone. A party of one is "you"; in a
// party the others need to know which of them is the problem.
func playerLabel(p wire.AssignedPlayer) string {
	if p.Name != "" {
		return p.Name
	}
	return string(p.SteamID)
}
