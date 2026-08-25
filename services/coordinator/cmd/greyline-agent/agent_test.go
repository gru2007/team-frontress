package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/srclog"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

func TestMatchIDFromTags(t *testing.T) {
	cases := map[string]string{
		"tfmm:9f2c":                              "9f2c",
		"increased_maxplayers,tfmm:9f2c,nocrits": "9f2c",
		"":                                       "",
		"nocrits":                                "",
		"tfmm:":                                  "",
	}
	for tags, want := range cases {
		if got := matchIDFromTags(tags); got != want {
			t.Errorf("matchIDFromTags(%q) = %q, want %q", tags, got, want)
		}
	}
}

func TestWinnerFromScores(t *testing.T) {
	if got := winner(3, 1); got != wire.TeamRed {
		t.Errorf("3-1 = %v, want RED", got)
	}
	if got := winner(1, 3); got != wire.TeamBlu {
		t.Errorf("1-3 = %v, want BLU", got)
	}
	// A draw must not be reported as a win: the war would advance a front on it.
	if got := winner(2, 2); got != wire.TeamUnassigned {
		t.Errorf("2-2 = %v, want nobody", got)
	}
}

func TestGameReportBeatsLogScraping(t *testing.T) {
	a := &agent{
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		reported: map[string]bool{},
		scores:   map[string]int{},
		client:   &http.Client{},
	}

	var got wire.MatchResult
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	a.opt.coordinator = srv.URL
	a.opt.secret = "s"

	feed := []string{
		`[frontress] match_result abcdef0123456789 status=0 winner=3 red=1 blu=3 duration=900 bots=0 players=2`,
		`[frontress] match_player abcdef0123456789 steamid=76561198000000001 team=2 score=5`,
		`[frontress] match_player abcdef0123456789 steamid=76561198000000002 team=3 score=9`,
		`[frontress] match_result_end abcdef0123456789`,
	}
	for _, line := range feed {
		ev, ok := srclog.Parse(line)
		if !ok {
			t.Fatalf("did not parse %q", line)
		}
		a.handle(context.Background(), ev)
	}

	if got.MatchID != "abcdef0123456789" {
		t.Fatalf("match id = %q", got.MatchID)
	}
	if got.Winner != wire.TeamBlu || got.RedScore != 1 || got.BluScore != 3 {
		t.Fatalf("result = %+v", got)
	}
	if len(got.Players) != 2 {
		t.Fatalf("players = %d, want 2: the game named who was actually there", len(got.Players))
	}

	// A second closing line must not report the match twice.
	got = wire.MatchResult{}
	ev, _ := srclog.Parse(`[frontress] match_result_end abcdef0123456789`)
	a.handle(context.Background(), ev)
	if got.MatchID != "" {
		t.Fatal("the match was reported twice")
	}
}
