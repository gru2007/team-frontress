package rcon

import "testing"

func TestStatusPlayersReadsTheHumanCount(t *testing.T) {
	const status = `hostname: Team Frontress
version : 8622218/24 8622218 secure
udp/ip  : 10.0.0.1:27015
map     : koth_product_final at: 0 x, 0 y, 0 z
players : 7 humans, 2 bots (24 max)
`
	n, ok := StatusPlayers(status)
	if !ok {
		t.Fatal("a normal status reply was not understood")
	}
	if n != 7 {
		t.Fatalf("players = %d, want 7 (humans, not humans+bots)", n)
	}
}

func TestStatusPlayersRefusesToGuess(t *testing.T) {
	// An unparseable reply must not read as an empty server: "empty" is what
	// ends a match.
	if _, ok := StatusPlayers("rcon: bad password"); ok {
		t.Fatal("garbage was parsed as a player count")
	}
	if _, ok := StatusPlayers(""); ok {
		t.Fatal("an empty reply was parsed as a player count")
	}
}
