package mm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
	"github.com/gru2007/team-frontress/services/coordinator/internal/players"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// rankedConfig is a ranked group with whatever restrictions a test needs.
func rankedConfig(r config.Restrictions) config.Config {
	cfg := testConfig(4, 4, 12, 0)
	cfg.MatchGroups[0].Name = "Ranked"
	cfg.MatchGroups[0].Mode = config.ModeRanked
	cfg.MatchGroups[0].Restrictions = r
	return cfg
}

func queueParty(m *Matchmaker, ids ...string) error {
	_, err := m.Enqueue(&Ticket{
		MatchGroup: wire.MatchGroupCasual12v12,
		Leader:     wire.SteamID(ids[0]),
		Players:    roster(ids...),
		Maps:       []string{"koth_product_final"},
	})
	return err
}

func TestMaxPartySizeRefusesAStack(t *testing.T) {
	m, _, _ := newTestMM(t, rankedConfig(config.Restrictions{MaxPartySize: 2}), 1)

	if err := queueParty(m, "76561198000000001", "76561198000000002"); err != nil {
		t.Fatalf("a party of two was refused by a max of two: %v", err)
	}
	err := queueParty(m, "76561198000000003", "76561198000000004", "76561198000000005")
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("err = %v, want ErrRestricted so the client can show the reason", err)
	}
	if !strings.Contains(err.Error(), "Ranked") {
		t.Errorf("refusal %q does not say which queue refused", err)
	}
}

func TestSoloQueueSaysSo(t *testing.T) {
	m, _, _ := newTestMM(t, rankedConfig(config.Restrictions{MaxPartySize: 1}), 1)

	err := queueParty(m, "76561198000000001", "76561198000000002")
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("err = %v, want the party refused", err)
	}
	if !strings.Contains(err.Error(), "solo") {
		t.Errorf("refusal %q should say the queue is solo only", err)
	}
}

func TestMinMatchesGatesTheGroup(t *testing.T) {
	store, err := players.New("")
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ := newTestMM(t, rankedConfig(config.Restrictions{MinMatchesPlayed: 3}), 1)
	m.UsePlayers(store)

	newbie := wire.SteamID("76561198000000001")
	if err := queueParty(m, string(newbie)); !errors.Is(err, ErrRestricted) {
		t.Fatalf("err = %v, want a player with no matches refused", err)
	}

	for i := 0; i < 3; i++ {
		store.Played(wire.AssignedPlayer{SteamID: newbie}, "m", "win")
	}
	if err := queueParty(m, string(newbie)); err != nil {
		t.Fatalf("three matches were not enough for a minimum of three: %v", err)
	}
}

func TestAbandonCooldownExpires(t *testing.T) {
	store, err := players.New("")
	if err != nil {
		t.Fatal(err)
	}
	m, _, clock := newTestMM(t, rankedConfig(config.Restrictions{AbandonCooldownMins: 30}), 1)
	m.UsePlayers(store)
	store.UseClock(func() time.Time { return *clock })

	id := wire.SteamID("76561198000000001")
	store.Abandoned(wire.AssignedPlayer{SteamID: id}, "m1")

	err = queueParty(m, string(id))
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("err = %v, want the abandoner held out of the queue", err)
	}
	if !strings.Contains(err.Error(), "30m") {
		t.Errorf("refusal %q should say how long is left", err)
	}

	*clock = clock.Add(31 * time.Minute)
	if err := queueParty(m, string(id)); err != nil {
		t.Fatalf("the cooldown outlived its 30 minutes: %v", err)
	}
}

func TestInviteOnlyGroup(t *testing.T) {
	invited := wire.SteamID("76561198000000001")
	m, _, _ := newTestMM(t, rankedConfig(config.Restrictions{AllowedSteamIDs: []wire.SteamID{invited}}), 1)

	if err := queueParty(m, string(invited)); err != nil {
		t.Fatalf("the invited player was refused: %v", err)
	}
	if err := queueParty(m, "76561198000000009"); !errors.Is(err, ErrRestricted) {
		t.Fatalf("err = %v, want everyone not on the list refused", err)
	}
}

func TestBanIsPerGroup(t *testing.T) {
	banned := wire.SteamID("76561198000000009")
	m, _, _ := newTestMM(t, rankedConfig(config.Restrictions{BannedSteamIDs: []wire.SteamID{banned}}), 1)

	if err := queueParty(m, string(banned)); !errors.Is(err, ErrRestricted) {
		t.Fatalf("err = %v, want the banned player refused", err)
	}
	// The party the banned player is in is refused too: matchmaking cannot
	// take four of five people and leave one behind.
	err := queueParty(m, "76561198000000001", string(banned))
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("err = %v, want a party carrying a banned player refused", err)
	}
}

// A result that names who was there turns the rest of the roster into
// abandons; a result that names nobody must not.
func TestAbandonsComeFromWhoActuallyPlayed(t *testing.T) {
	store, err := players.New("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(2, 2, 12, 0)
	m, _, clock := newTestMM(t, cfg, 1)
	m.UsePlayers(store)
	store.UseClock(func() time.Time { return *clock })

	party(t, m, "76561198000000001", 1)
	party(t, m, "76561198000000002", 1)
	settle(m)

	var matchID string
	var seats []wire.AssignedPlayer
	m.mu.Lock()
	for id, mt := range m.matches {
		matchID, seats = id, mt.Players
	}
	m.mu.Unlock()
	if matchID == "" {
		t.Fatal("no match formed")
	}

	// Long enough that not showing up is a choice, not a crashed server.
	*clock = clock.Add(20 * time.Minute)

	played, absent := seats[0], seats[1]
	if err := m.ReportResult(context.Background(), wire.MatchResult{
		MatchID: matchID,
		Winner:  played.Team,
		Players: []wire.AssignedPlayer{played},
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	if got := store.Get(played.SteamID); got.Matches != 1 || got.Wins != 1 {
		t.Errorf("the player who played got %+v, want one match and one win", got)
	}
	if got := store.Get(absent.SteamID); got.Abandons != 1 {
		t.Errorf("the player who never connected got %+v, want one abandon", got)
	}
}

func TestAResultWithNoPlayersBlamesNobody(t *testing.T) {
	store, err := players.New("")
	if err != nil {
		t.Fatal(err)
	}
	m, _, clock := newTestMM(t, testConfig(2, 2, 12, 0), 1)
	m.UsePlayers(store)
	store.UseClock(func() time.Time { return *clock })

	party(t, m, "76561198000000001", 1)
	party(t, m, "76561198000000002", 1)
	settle(m)

	var matchID string
	m.mu.Lock()
	for id := range m.matches {
		matchID = id
	}
	m.mu.Unlock()

	*clock = clock.Add(20 * time.Minute)
	if err := m.ReportResult(context.Background(), wire.MatchResult{MatchID: matchID, Winner: wire.TeamRed}); err != nil {
		t.Fatalf("report: %v", err)
	}
	for _, id := range []wire.SteamID{"765611980000000010", "765611980000000020"} {
		if rec := store.Get(id); rec.Abandons != 0 {
			t.Errorf("%s was marked an abandoner by a server that never said who played", id)
		}
	}
}
