package srclog

import "testing"

func TestStripUnwrapsALogPacket(t *testing.T) {
	packet := append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'R'},
		[]byte("L 08/23/2026 - 21:14:02: World triggered \"Game_Over\" reason \"Reached Time Limit\"\n")...)
	line, ok := Strip(packet)
	if !ok {
		t.Fatal("a normal log packet was not recognised")
	}
	if line[len(line)-1] == '\n' {
		t.Error("the trailing newline survived")
	}
	ev, ok := Parse(line)
	if !ok || ev.Kind != KindGameOver {
		t.Fatalf("parsed %+v, ok=%v, want a game over", ev, ok)
	}
	if ev.Reason != "Reached Time Limit" {
		t.Errorf("reason = %q", ev.Reason)
	}
}

func TestStripSkipsTheLogSecret(t *testing.T) {
	// A server with sv_logsecret set sends 'S' plus the secret before the line.
	packet := append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'S'},
		[]byte("1234567L 08/23/2026 - 21:14:02: World triggered \"Round_Win\" (winner \"Red\")\n")...)
	line, ok := Strip(packet)
	if !ok {
		t.Fatal("a log packet with a secret was dropped")
	}
	ev, ok := Parse(line)
	if !ok || ev.Kind != KindRoundWin || ev.Team != "Red" {
		t.Fatalf("parsed %+v, ok=%v, want a round win for Red", ev, ok)
	}
}

func TestFinalScores(t *testing.T) {
	for _, tc := range []struct {
		line string
		team string
		want int
	}{
		{`L 08/23/2026 - 21:14:02: Team "Red" final score "3" with "6" players`, "Red", 3},
		{`L 08/23/2026 - 21:14:02: Team "Blue" final score "5" with "6" players`, "Blue", 5},
	} {
		ev, ok := Parse(tc.line)
		if !ok || ev.Kind != KindTeamScore {
			t.Fatalf("%q parsed as %+v, ok=%v", tc.line, ev, ok)
		}
		if ev.Team != tc.team || ev.Score != tc.want {
			t.Errorf("%q -> team %q score %d, want %q %d", tc.line, ev.Team, ev.Score, tc.team, tc.want)
		}
	}
	if got := NormalizeTeam("Blue"); got != "BLU" {
		t.Errorf("NormalizeTeam(Blue) = %q, want BLU", got)
	}
}

// The one thing this package must never do is turn chat into an event: a
// player can type anything, including a line that looks like a game over.
func TestChatIsNotAnEvent(t *testing.T) {
	lines := []string{
		`L 08/23/2026 - 21:14:02: "gru<2><[U:1:1]><Red>" say "World triggered ""Game_Over"""`,
		`L 08/23/2026 - 21:14:02: "gru<2><[U:1:1]><Red>" say_team "Team ""Red"" final score ""99"""`,
		`L 08/23/2026 - 21:14:02: "gru<2><[U:1:1]><Red>" entered the game`,
	}
	for _, line := range lines {
		if ev, ok := Parse(line); ok {
			t.Errorf("%q was read as %+v", line, ev)
		}
	}
}

func TestMapLoad(t *testing.T) {
	ev, ok := Parse(`L 08/23/2026 - 21:14:02: Loading map "koth_product_final"`)
	if !ok || ev.Kind != KindMapLoad || ev.Map != "koth_product_final" {
		t.Fatalf("parsed %+v, ok=%v", ev, ok)
	}
}
