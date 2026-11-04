package war

import (
	"testing"
	"time"
)

func TestVictoryIntermissionReplayAndNewCampaign(t *testing.T) {
	e, path := newTestEngine(t)
	fronts, err := e.Reconcile(2, 1)
	if err != nil || len(fronts) != 1 {
		t.Fatal(fronts, err)
	}
	f := fronts[0]
	if err = e.ApplyResult(f.ID, "battle-1", true); err != nil {
		t.Fatal(err)
	}
	stage := e.Fronts()[0].StageIndex
	if err = e.ApplyResult(f.ID, "battle-1", true); err != nil || e.Fronts()[0].StageIndex != stage {
		t.Fatal("duplicate result advanced twice", err)
	}
	if err = e.ApplyResult(f.ID, "battle-2", true); err != nil {
		t.Fatal(err)
	}
	hq := e.Fronts()[0]
	if hq.NodeID != "a_redhq" {
		t.Fatal("expected push to RED HQ", hq)
	}
	if err = e.ApplyResult(hq.ID, "battle-3", true); err != nil {
		t.Fatal(err)
	}
	s := e.Snapshot()
	if s.Winner != SideBLU || s.EndedAt == 0 {
		t.Fatalf("no victory: %+v", s)
	}
	if fs, err := e.Reconcile(100, 10); err != nil || len(fs) != 0 {
		t.Fatal("battle opened during intermission", err)
	}
	log, past, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	restarted, err := NewEngine(testTheater(), log, past)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Snapshot().Winner != SideBLU {
		t.Fatal("restart lost winner")
	}
	if changed, err := restarted.AdvanceCampaign(time.Unix(s.EndedAt+86399, 0), 24*time.Hour); err != nil || changed {
		t.Fatal("restarted early", err)
	}
	if changed, err := restarted.AdvanceCampaign(time.Unix(s.EndedAt+86400, 0), 24*time.Hour); err != nil || !changed {
		t.Fatal("did not restart", err)
	}
	after := restarted.Snapshot()
	if after.Campaign == s.Campaign || after.Winner != "" || after.Owner["a_redhq"] != SideRED {
		t.Fatalf("bad new campaign: %+v", after)
	}
}
