package srclog

import "testing"

func TestParsesTheLinesThatDecideABattle(t *testing.T) {
	cases := []struct {
		line string
		want Event
	}{
		{
			`L 08/17/2026 - 21:14:03: Started map "cp_process_final" (CRC "1234")`,
			Event{Kind: KindMapStarted, Map: "cp_process_final"},
		},
		{
			`L 08/17/2026 - 21:16:44: World triggered "Round_Win" (winner "Red")`,
			Event{Kind: KindRoundWin, Team: TeamRed},
		},
		{
			`L 08/17/2026 - 21:16:44: World triggered "Round_Win" (winner "Blue")`,
			Event{Kind: KindRoundWin, Team: TeamBlu},
		},
		{
			`L 08/17/2026 - 21:16:44: Team "Red" current score "1" with "6" players`,
			Event{Kind: KindScore, Team: TeamRed, Score: 1, Players: 6},
		},
		{
			`L 08/17/2026 - 21:31:02: Team "Blue" final score "3" with "12" players`,
			Event{Kind: KindScore, Team: TeamBlu, Score: 3, Players: 12, Final: true},
		},
		{
			`L 08/17/2026 - 21:31:02: World triggered "Game_Over" reason "Reached Win Limit"`,
			Event{Kind: KindGameOver, Reason: "Reached Win Limit"},
		},
		{
			`L 08/17/2026 - 21:10:00: "Merc<3><[U:1:12345]><Red>" entered the game`,
			Event{Kind: KindPlayerJoined, Name: "Merc", SteamID: 76561197960278073},
		},
		{
			`L 08/17/2026 - 21:40:00: "Merc<3><[U:1:12345]><Blue>" disconnected (reason "Disconnect by user")`,
			Event{Kind: KindPlayerLeft, Name: "Merc", SteamID: 76561197960278073},
		},
	}
	for _, tc := range cases {
		got, ok := Parse(tc.line)
		if !ok {
			t.Errorf("did not parse: %s", tc.line)
			continue
		}
		if got.Kind != tc.want.Kind || got.Team != tc.want.Team || got.Score != tc.want.Score ||
			got.Players != tc.want.Players || got.Final != tc.want.Final || got.Map != tc.want.Map ||
			got.SteamID != tc.want.SteamID || got.Reason != tc.want.Reason || got.Name != tc.want.Name {
			t.Errorf("%s\n got %+v\nwant %+v", tc.line, got, tc.want)
		}
	}
}

func TestIgnoresTheNoise(t *testing.T) {
	for _, line := range []string{
		`L 08/17/2026 - 21:16:44: "A<2><[U:1:1]><Red>" say "gg"`,
		`L 08/17/2026 - 21:16:44: "A<2><[U:1:1]><Red>" killed "B<3><[U:1:2]><Blue>" with "scattergun"`,
		`L 08/17/2026 - 21:16:44: rcon from "127.0.0.1:5000": command "status"`,
		``,
	} {
		if ev, ok := Parse(line); ok {
			t.Errorf("parsed noise as %s: %s", ev.Kind, line)
		}
	}
}

// The UDP log stream wraps each line in a small header; the parser has to see
// through it or the agent learns nothing from a remote server.
func TestParsesUDPFramedLines(t *testing.T) {
	line := "\xff\xff\xff\xffRL 08/17/2026 - 21:31:02: World triggered \"Game_Over\" reason \"Reached Win Limit\""
	ev, ok := Parse(line)
	if !ok || ev.Kind != KindGameOver {
		t.Fatalf("UDP-framed line parsed as %+v (ok=%v)", ev, ok)
	}
}

func TestSteamIDConversion(t *testing.T) {
	if got := SteamID64("[U:1:0]"); got != 76561197960265728 {
		t.Errorf("account 0 became %d", got)
	}
	for _, raw := range []string{"Console", "BOT", "", "[U:1:notanumber]"} {
		if got := SteamID64(raw); got != 0 {
			t.Errorf("%q became %d, want 0", raw, got)
		}
	}
}
