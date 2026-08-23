package main

import (
	"testing"

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
