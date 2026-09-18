package rcon

import "testing"

const statusReply = `hostname: Team Frontress | 9f2c
version : 8622370/24 8622370 secure
udp/ip  : 10.0.0.9:27015
players : 2 humans, 0 bots (24 max)
# userid name                uniqueid            connected ping loss state
#      2 "gru"               [U:1:12345]         01:23       50    0 active
#      3 "someone else"      STEAM_0:1:6172      00:41       60    0 active
#      4 "Bot"               BOT                                     active
`

func TestStatusRosterReadsSteamIDs(t *testing.T) {
	roster := StatusRoster(statusReply)
	if len(roster) != 3 {
		t.Fatalf("parsed %d players, want 3: %+v", len(roster), roster)
	}
	if roster[0].Name != "gru" || roster[0].SteamID != "76561197960278073" {
		t.Errorf("first player = %+v", roster[0])
	}
	if roster[1].SteamID != "76561197960278073" && roster[1].SteamID == "" {
		t.Errorf("the old STEAM_0 spelling was not understood: %+v", roster[1])
	}
	if !roster[2].Bot {
		t.Errorf("a bot was counted as a player: %+v", roster[2])
	}
}

// A player can put anything in their name, including something that looks like
// a SteamID or another status line.
func TestStatusRosterIsNotFooledByAName(t *testing.T) {
	reply := "#      2 \"# 9 \\\"admin\\\" [U:1:999]\"    [U:1:12345]  01:23 50 0 active\n"
	roster := StatusRoster(reply)
	if len(roster) != 1 {
		t.Fatalf("parsed %d players, want 1: %+v", len(roster), roster)
	}
	if roster[0].SteamID != "76561197960278073" {
		t.Errorf("SteamID = %q, want the one outside the quotes", roster[0].SteamID)
	}
}

func TestSteamID64Spellings(t *testing.T) {
	cases := map[string]string{
		"[U:1:12345]":       "76561197960278073",
		"STEAM_0:1:6172":    "76561197960278073",
		"76561197960278073": "76561197960278073",
	}
	for in, want := range cases {
		got, ok := SteamID64(in)
		if !ok || got != want {
			t.Errorf("SteamID64(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"BOT", "", "[I:1:5]", "STEAM_0:1", "not-an-id"} {
		if got, ok := SteamID64(bad); ok {
			t.Errorf("SteamID64(%q) = %q, want a refusal", bad, got)
		}
	}
}

func TestTagValue(t *testing.T) {
	if got := TagValue(`"sv_tags" = "tfmm:9f2c" ( def. "" )`); got != "tfmm:9f2c" {
		t.Fatalf("TagValue = %q", got)
	}
	if got := TagValue("nonsense"); got != "" {
		t.Fatalf("TagValue = %q, want empty for a reply that is not a convar", got)
	}
}
