package gc

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/war"
)

func TestWarFormationHonorsFactionAndMapTeamMapping(t *testing.T) {
	theater, err := war.LoadTheater("../../theater.example.json")
	if err != nil {
		t.Fatal(err)
	}
	log, past, err := war.OpenLog(filepath.Join(t.TempDir(), "war.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	engine, err := war.NewEngine(theater, log, past)
	if err != nil {
		t.Fatal(err)
	}
	grp := config.MatchGroupConfig{MatchGroup: 7, Enabled: true, MinPlayers: 4, IdealPlayers: 4, MaxPlayers: 4, Maps: []string{"pl_badwater"}}
	g, err := New(Deps{War: engine, Config: config.Config{War: config.WarConfig{Enabled: true}, MatchGroups: []config.MatchGroupConfig{grp}}})
	if err != nil {
		t.Fatal(err)
	}
	var queue []mm.Entry
	for i := uint64(1); i <= 4; i++ {
		side := "RED"
		if i > 2 {
			side = "BLU"
		}
		if err := g.players.ChooseWarSide(i, engine.Campaign(), side); err != nil {
			t.Fatal(err)
		}
		queue = append(queue, mm.Entry{PartyID: i, QueuedAt: time.Now(), Players: []mm.Player{{SteamID: i, Rating: 1500}}})
	}
	f, ok := g.formFactions(queue, mm.Sizes{Min: 4, Ideal: 4, Max: 4}, 0)
	if !ok || mm.Count(f.Red) != 2 || mm.Count(f.Blu) != 2 {
		t.Fatal("no equal faction match", f)
	}
	if _, ok := g.formFactions(queue[:2], mm.Sizes{Min: 2, Ideal: 2, Max: 4}, 0); ok {
		t.Fatal("matched one faction against itself")
	}
	b := &battle{Battle: war.Battle{Attacker: war.SideRED, AttackerTeam: 3, Fields: []war.Battlefield{{Map: "pl_badwater", AttackerTeam: 3}}}, Front: war.Front{ID: "front-test"}}
	g.createLobby(grp, f, mm.Sizes{Min: 4, Ideal: 4, Max: 4}, b, "", nil)
	for _, l := range g.lobbies {
		if l.War == nil || !l.War.UniformSwap || l.War.Campaign != engine.Campaign() {
			t.Fatal("missing campaign presentation mapping", l.War)
		}
		for _, m := range l.Members {
			want := mm.Blu
			if m.SteamID > 2 {
				want = mm.Red
			}
			if m.Team != want {
				t.Fatal("campaign side seated on wrong map team", m)
			}
		}
	}
}
