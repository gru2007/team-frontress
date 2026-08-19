package war

import "testing"

func TestBattleWeightScalesWithHeadcount(t *testing.T) {
	cases := []struct {
		players int
		want    float64
	}{
		{2, 0.25}, // floored: a 1v1 is worth something, not nothing
		{3, 0.25}, // still under the floor
		{6, 0.5},  // half a full battle, half a stage
		{12, 1.0}, // full strength
		{24, 1.0}, // and never more than a whole stage
	}
	for _, c := range cases {
		if got := BattleWeight(c.players, 12, 0.25); got != c.want {
			t.Errorf("BattleWeight(%d) = %v, want %v", c.players, got, c.want)
		}
	}
	if got := BattleWeight(4, 0, 0.25); got != 1 {
		t.Errorf("with no full-strength figure configured every battle is whole: got %v", got)
	}
}

// TestFourThinWinsClearOneStage is the whole point of weighting: the war
// advances at the pace the population can actually fight at.
func TestFourThinWinsClearOneStage(t *testing.T) {
	f := &Front{
		Attacker: SideRed, Defender: SideBlu, Status: FrontActive,
		Plan: []StageKind{StageSkirmish, StageAdvance, StageAssault},
	}
	for i := 1; i <= 3; i++ {
		f.Stage, f.Push, f.Status = Advance(f, SideRed, 0.25)
		if f.Stage != 0 {
			t.Fatalf("thin win %d cleared a stage on its own: stage = %d", i, f.Stage)
		}
	}
	f.Stage, f.Push, f.Status = Advance(f, SideRed, 0.25)
	if f.Stage != 1 {
		t.Fatalf("four quarter-weight wins did not clear one stage: stage = %d, push = %v", f.Stage, f.Push)
	}
	if f.Push != 0 {
		t.Fatalf("a stage cleared exactly should leave nothing behind: push = %v", f.Push)
	}
}

// TestAFullStrengthBattleStillClearsAStageOnItsOwn: weighting must not slow
// down the case it was never about.
func TestAFullStrengthBattleStillClearsAStageOnItsOwn(t *testing.T) {
	f := &Front{
		Attacker: SideRed, Defender: SideBlu, Status: FrontActive,
		Plan: []StageKind{StageSkirmish, StageAdvance, StageAssault},
	}
	f.Stage, f.Push, f.Status = Advance(f, SideRed, 1)
	if f.Stage != 1 || f.Push != 0 {
		t.Fatalf("a full battle should move exactly one stage: stage = %d, push = %v", f.Stage, f.Push)
	}
}

// TestAnEventWithNoWeightCountsWhole keeps war logs written before battles
// were weighted replaying to the state they produced at the time.
func TestAnEventWithNoWeightCountsWhole(t *testing.T) {
	f := &Front{
		Attacker: SideRed, Defender: SideBlu, Status: FrontActive,
		Plan: []StageKind{StageSkirmish, StageAdvance},
	}
	stage, _, _ := Advance(f, SideRed, 0)
	if stage != 1 {
		t.Fatalf("an unweighted (pre-weighting) battle must still clear a stage: got %d", stage)
	}
}

// TestPartialGroundIsHeldAgainstTheDefender: a defender chipping away at an
// offensive has to earn the pushback the same way.
func TestPartialGroundIsHeldAgainstTheDefender(t *testing.T) {
	f := &Front{
		Attacker: SideRed, Defender: SideBlu, Status: FrontActive, Stage: 2,
		Plan: []StageKind{StageSkirmish, StageAdvance, StageAssault, StageAssault},
	}
	f.Stage, f.Push, f.Status = Advance(f, SideBlu, 0.5)
	if f.Stage != 2 {
		t.Fatalf("half a defensive win pushed the offensive back a whole stage: %d", f.Stage)
	}
	f.Stage, f.Push, f.Status = Advance(f, SideBlu, 0.5)
	if f.Stage != 1 {
		t.Fatalf("two half-weight defensive wins should push back one stage: %d", f.Stage)
	}
}

// TestAThinDefensiveWinCannotCollapseAnOffensiveEarly: the collapse line is
// the offensive breaking, and it should take a whole stage's worth to do it.
func TestAThinDefensiveWinCannotCollapseAnOffensiveEarly(t *testing.T) {
	f := &Front{
		Attacker: SideRed, Defender: SideBlu, Status: FrontActive, Stage: 0,
		Plan: []StageKind{StageSkirmish, StageAdvance},
	}
	f.Stage, f.Push, f.Status = Advance(f, SideBlu, 0.25)
	if f.Status == FrontCollapsed {
		t.Fatal("a quarter-weight defensive win broke the whole offensive")
	}
	for i := 0; i < 3; i++ {
		f.Stage, f.Push, f.Status = Advance(f, SideBlu, 0.25)
	}
	if f.Status != FrontCollapsed {
		t.Fatalf("a stage's worth of defensive wins at the collapse line should break it: %s", f.Status)
	}
}
