package gc

import (
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/history"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/state"
)

func TestArchiveRetainsLeaversBackfillAndResult(t *testing.T) {
	g, err := New(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	l := &Lobby{ID: 1, MatchID: 2, State: lobbyRun, FormedAt: time.Now(), Members: []*Member{
		{SteamID: 76561197960265729, Team: mm.Red},
		{SteamID: 76561197960265730, Team: mm.Blu},
	}}
	g.lobbies[l.ID] = l
	g.archiveMatch(l, nil, "")
	g.dropMember(l, 76561197960265729)
	l.addMember(76561197960265731, mm.Red, 3, time.Now())
	g.lobbyChanged(l)
	g.archiveMatch(l, &history.Result{WinningTeam: 3, Players: []history.PlayerResult{}}, "")
	g.endLobby(l, "the server ended the match", false)
	m, ok := g.history.Get("2")
	if !ok || m.State != "ended" || m.EndedAt == nil || m.Result == nil || m.Result.WinningTeam != 3 || len(m.Members) != 3 {
		t.Fatalf("archive: %+v", m)
	}
	for _, member := range m.Members {
		if member.SteamID == "76561197960265729" && !member.Left {
			t.Fatal("leaver was not retained as a leaver")
		}
	}
}

func TestArchiveTellsWarBattlesFromCasualMatches(t *testing.T) {
	g, err := New(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	casual := &Lobby{ID: 1, MatchID: 10, Group: casualGroup, State: lobbyRun, FormedAt: time.Now()}
	battle := &Lobby{ID: 2, MatchID: 11, Group: casualGroup, State: lobbyRun, FormedAt: time.Now(),
		War: &state.War{Campaign: "rustvalley-1", NodeID: "n1", NodeName: "Rust Valley"}}
	ranked := &Lobby{ID: 3, MatchID: 12, Group: ladderGroup, State: lobbyRun, FormedAt: time.Now()}
	for _, l := range []*Lobby{casual, battle, ranked} {
		g.archiveMatch(l, nil, "")
	}
	for id, want := range map[string]string{"10": history.KindCasual, "11": history.KindWar, "12": history.KindRanked} {
		if m, _ := g.history.Get(id); m.Kind != want {
			t.Errorf("match %s: kind %q, want %q", id, m.Kind, want)
		}
	}
	if m, _ := g.history.Get("11"); m.Sector != "Rust Valley" || m.Campaign != "rustvalley-1" {
		t.Errorf("war battle archive: %+v", m)
	}
}
