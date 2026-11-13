package gc

import (
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
)

// The casual map picker shows, per map, who is waiting for it and who is
// playing it: a party that ticked maps counts for those, one that ticked none
// for every map.
func TestStatusCountsQueuedAndPlayingPerMap(t *testing.T) {
	now := time.Now()
	grp := config.MatchGroupConfig{MatchGroup: casualGroup, Enabled: true, MinPlayers: 2, MaxPlayers: 24,
		Maps: []string{"cp_badlands", "cp_granary", "cp_process"}}
	g, err := New(Deps{Config: config.Config{MatchGroups: []config.MatchGroupConfig{grp},
		MapIndex: map[string]uint32{"cp_badlands": 1, "cp_granary": 2, "cp_process": 3}}})
	if err != nil {
		t.Fatal(err)
	}

	// Two players who want Badlands or Process, one who takes anything.
	duo := g.newParty(10)
	duo.Members = append(duo.Members, 11)
	duo.Queues[grp.MatchGroup] = now
	duo.Criteria = &gcpb.CTFGroupMatchCriteriaProto{CasualCriteria: &gcpb.CTFCasualMatchCriteria{
		SelectedMapsBits: []uint32{1<<1 | 1<<3}}}
	solo := g.newParty(12)
	solo.Queues[grp.MatchGroup] = now
	idle := g.newParty(13) // not queued
	_ = idle

	l := &Lobby{ID: 1, MatchID: 2, Group: grp.MatchGroup, Map: "cp_granary", MaxPlayers: 24, State: lobbyRun, FormedAt: now}
	l.addMember(20, mm.Red, 0, now)
	l.addMember(21, mm.Blu, 0, now)
	g.lobbies[l.ID] = l

	g.publishStatus()
	st := g.Status()
	if len(st.Groups) != 1 {
		t.Fatalf("groups = %d", len(st.Groups))
	}
	gs := st.Groups[0]
	if gs.Queued != 3 {
		t.Errorf("queued = %d, want 3", gs.Queued)
	}
	wantQueued := map[string]int{"cp_badlands": 3, "cp_granary": 1, "cp_process": 3}
	wantPlaying := map[string]int{"cp_badlands": 0, "cp_granary": 2, "cp_process": 0}
	for m, n := range wantQueued {
		if gs.MapQueued[m] != n {
			t.Errorf("map_queued[%s] = %d, want %d", m, gs.MapQueued[m], n)
		}
		if gs.MapPlaying[m] != wantPlaying[m] {
			t.Errorf("map_playing[%s] = %d, want %d", m, gs.MapPlaying[m], wantPlaying[m])
		}
	}
}
