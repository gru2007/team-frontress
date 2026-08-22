package main

import "testing"

func TestPresenceSteamID(t *testing.T) {
	tests := []struct {
		line string
		want uint64
		ok   bool
	}{
		{"GREYLINE_PRESENCE 76561198000000001", 76561198000000001, true},
		{"  GREYLINE_PRESENCE 76561198000000002  ", 76561198000000002, true},
		{"connected: Alice 76561198000000003 on the roster", 0, false},
		{"GREYLINE_PRESENCE not-a-steamid", 0, false},
		{"GREYLINE_PRESENCE 0", 0, false},
	}
	for _, tc := range tests {
		got, ok := presenceSteamID(tc.line)
		if got != tc.want || ok != tc.ok {
			t.Errorf("presenceSteamID(%q) = (%d, %v), want (%d, %v)", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}
