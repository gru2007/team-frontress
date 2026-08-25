package mm

import (
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

func tickets(sizes ...int) []*Ticket {
	out := make([]*Ticket, 0, len(sizes))
	for i, n := range sizes {
		ps := make([]wire.AssignedPlayer, n)
		for j := range ps {
			ps[j] = wire.AssignedPlayer{SteamID: wire.SteamID(string(rune('a'+i)) + string(rune('0'+j)))}
		}
		out = append(out, &Ticket{ID: string(rune('a' + i)), Players: ps})
	}
	return out
}

func TestSplitTeamsBalancesWholeParties(t *testing.T) {
	cases := []struct {
		name  string
		sizes []int
		want  bool
	}{
		{"two even parties", []int{4, 4}, true},
		{"three parties that balance", []int{3, 2, 1}, true},
		{"odd total, off by one is fine", []int{2, 2, 1}, true},
		{"one party cannot be split", []int{4}, false},
		{"lopsided", []int{6, 1, 1}, false},
		{"twelve singles", []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			red, blu, ok := splitTeams(tickets(tc.sizes...))
			if ok != tc.want {
				t.Fatalf("ok = %v, want %v", ok, tc.want)
			}
			if !ok {
				return
			}
			diff := countSeats(red) - countSeats(blu)
			if diff < 0 {
				diff = -diff
			}
			if diff > 1 {
				t.Fatalf("teams differ by %d (%d v %d)", diff, countSeats(red), countSeats(blu))
			}
			if got := len(red) + len(blu); got != len(tc.sizes) {
				t.Fatalf("placed %d parties, had %d", got, len(tc.sizes))
			}
		})
	}
}

func TestChooseMapPrefersWhatEveryonePicked(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(4, 4, 8, 0), 1)
	group, _ := m.cfg.Group(wire.MatchGroupCasual12v12)

	picked := tickets(2, 2)
	picked[0].Maps = []string{"koth_product_final", "cp_process_final"}
	picked[1].Maps = []string{"cp_process_final"}

	if got := m.chooseMapLocked(group.EffectiveMaps(), picked); got != "cp_process_final" {
		t.Fatalf("map = %q, want the one both parties chose", got)
	}
}

func TestChooseMapFallsBackToTheUnionThenTheGroup(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(4, 4, 8, 0), 1)
	group, _ := m.cfg.Group(wire.MatchGroupCasual12v12)

	// Disjoint preferences: nobody gets a veto.
	picked := tickets(2, 2)
	picked[0].Maps = []string{"koth_product_final"}
	picked[1].Maps = []string{"cp_process_final"}
	if got := m.chooseMapLocked(group.EffectiveMaps(), picked); got == "" {
		t.Fatal("no map was chosen from disjoint preferences")
	}

	// No preferences at all: the group's own list.
	none := tickets(2)
	got := m.chooseMapLocked(group.EffectiveMaps(), none)
	if got != "koth_product_final" && got != "cp_process_final" {
		t.Fatalf("map = %q, want one of the group's maps", got)
	}
}

func TestChooseMapAvoidsTheOneJustPlayed(t *testing.T) {
	m, _, clock := newTestMM(t, testConfig(4, 4, 8, 0), 1)
	group, _ := m.cfg.Group(wire.MatchGroupCasual12v12)

	first := m.chooseMapLocked(group.EffectiveMaps(), tickets(2))
	m.recentMap(first)
	*clock = clock.Add(time.Minute)

	if second := m.chooseMapLocked(group.EffectiveMaps(), tickets(2)); second == first {
		t.Fatalf("picked %q twice in a row with another map available", first)
	}
}

func TestMapOutsideTheGroupsListIsIgnored(t *testing.T) {
	m, _, _ := newTestMM(t, testConfig(4, 4, 8, 0), 1)
	group, _ := m.cfg.Group(wire.MatchGroupCasual12v12)

	picked := tickets(2)
	picked[0].Maps = []string{"ctf_2fort_but_haunted"}

	got := m.chooseMapLocked(group.EffectiveMaps(), picked)
	if got == "ctf_2fort_but_haunted" {
		t.Fatal("a map the group does not run was chosen because a client asked for it")
	}
	if got == "" {
		t.Fatal("no map was chosen at all")
	}
}

func TestRosterArgIsTheServerHandoffFormat(t *testing.T) {
	got := rosterArg([]wire.AssignedPlayer{
		{SteamID: "76561198000000001", Team: wire.TeamRed},
		{SteamID: "", Team: wire.TeamBlu}, // a seat with no id is skipped, not sent blank
		{SteamID: "76561198000000002", Team: wire.TeamBlu},
	})
	want := "76561198000000001:2,76561198000000002:3"
	if got != want {
		t.Fatalf("roster = %q, want %q", got, want)
	}

	if rosterArg(nil) != "" {
		t.Fatal("an empty roster produced an argument; the handoff must be skipped entirely")
	}
}
