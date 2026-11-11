package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveSurvivesRestartAndReconcilesLostLiveState(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m := Match{MatchID: "18446744073709551615", State: "run", FormedAt: now, Members: []Member{{SteamID: "76561197960265729", Name: "Alice"}}, Result: &Result{WinningTeam: 2, Players: []PlayerResult{{SteamID: "76561197960265729", Kills: 5}}}}
	if err := s.Put(m); err != nil {
		t.Fatal(err)
	}
	// Neither a caller's input nor a response may mutate stored data.
	m.Members[0].Name = "changed"
	got, _ := s.Get(m.MatchID)
	got.Result.Players[0].Kills = 999
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Reconcile(map[string]bool{}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(m.MatchID)
	if !ok || got.State != "ended" || got.EndedAt == nil || got.Members[0].Name != "Alice" || got.Result.Players[0].Kills != 5 {
		t.Fatalf("archive: %+v", got)
	}
	page := reopened.List("76561197960265729", "ended", 0, 20)
	if page.Total != 1 || len(page.Matches) != 1 {
		t.Fatalf("page: %+v", page)
	}
	if len(reopened.List("nobody", "", 0, 20).Matches) != 0 {
		t.Fatal("player filter ignored")
	}
	if len(reopened.List("", "", 1, 20).Matches) != 0 {
		t.Fatal("offset ignored")
	}
	// Failure before rename leaves the previous archive readable.
	if err := s.Put(Match{MatchID: "../escape"}); err == nil {
		t.Fatal("accepted path traversal")
	}
	if err := os.WriteFile(filepath.Join(dir, ".match-crashed"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal("temporary file broke archive", err)
	}
}

func TestListPaginationAndActiveRecovery(t *testing.T) {
	s, _ := Open("")
	now := time.Now().UTC()
	for _, id := range []string{"1", "2", "3"} {
		if err := s.Put(Match{MatchID: id, State: "setup", FormedAt: now, Members: []Member{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Reconcile(map[string]bool{"2": true}, now); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Get("2"); m.State != "setup" {
		t.Fatal("ended a recoverable match")
	}
	p := s.List("", "ended", 1, 1)
	if p.Total != 2 || len(p.Matches) != 1 || p.Matches[0].MatchID != "1" {
		t.Fatalf("page: %+v", p)
	}
}

func TestKindIsFilledForOldArchives(t *testing.T) {
	dir := t.TempDir()
	write := func(id, body string) {
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("1", `{"match_id":"1","match_group":7,"members":[]}`)
	write("2", `{"match_id":"2","match_group":7,"campaign":"rustvalley-1","members":[]}`)
	write("3", `{"match_id":"3","match_group":2,"members":[]}`)
	write("4", `{"match_id":"4","match_group":7,"kind":"ranked","members":[]}`)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"1": KindCasual, "2": KindWar, "3": KindRanked, "4": KindRanked} {
		if m, _ := s.Get(id); m.Kind != want {
			t.Errorf("match %s: kind %q, want %q", id, m.Kind, want)
		}
	}
}
