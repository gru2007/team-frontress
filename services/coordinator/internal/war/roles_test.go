package war

import (
	"math/rand"
	"sort"
	"testing"
)

// TestNeitherSideIsStructurallyTheAttacker is the answer to the objection this
// design invites: "RED always defends and BLU always attacks, so people will
// pick a side and be stuck doing one job all evening".
//
// It is worth being precise about, because half of it is true. The *in-game
// team* is fixed — the attacking side always wears BLU, because every payload
// and attack/defend map is built that way (see Rules.AttackerTeam). The *war
// side* is not: which belligerent is on the offensive is a property of the
// front, and a front exists wherever somebody owns ground next to somebody
// else's. Both sides generate candidates by the same rule and are scored by
// the same terms — distance to the defender's headquarters, momentum,
// mobilization — none of which name RED or BLU.
//
// So a player's allegiance does not decide whether they attack; where the war
// currently is does, and that moves. This measures it rather than asserting
// it, because a scoring change that quietly favoured one side would otherwise
// only show up as an evening where one team never got to attack.
func TestNeitherSideIsStructurallyTheAttacker(t *testing.T) {
	var shares []float64
	totalRed, totalBlu, campaigns := 0, 0, 0

	for seed := int64(1); seed <= 60; seed++ {
		e := newTestEngine(t)
		rng := rand.New(rand.NewSource(seed))
		red, blu, battles := 0, 0, 0

		// A coin-flip war: no side is better at winning, so any bias left in
		// the result is the front-picking rule's own.
		for battles < 500 {
			fronts := e.ActiveFronts()
			if len(fronts) == 0 {
				break // the campaign was decided
			}
			f := fronts[rng.Intn(len(fronts))]
			outcome := OutcomeRedWin
			if rng.Intn(2) == 0 {
				outcome = OutcomeBluWin
			}
			if _, err := e.RecordBattle(BattleResult{
				BattleID: randHex(4), FrontID: f.ID, Outcome: outcome,
				Map: "cp_process_final", Mode: "5cp", Players: 12,
			}); err != nil {
				continue
			}
			if f.Attacker == SideRed {
				red++
			} else {
				blu++
			}
			battles++
		}

		if red+blu < 5 {
			continue // too short to say anything about
		}
		campaigns++
		totalRed += red
		totalBlu += blu
		shares = append(shares, float64(red)/float64(red+blu))
	}

	if campaigns < 20 {
		t.Fatalf("only %d campaigns ran; the sample cannot answer the question", campaigns)
	}

	total := totalRed + totalBlu
	redShare := float64(totalRed) / float64(total)
	sort.Float64s(shares)
	t.Logf("%d campaigns, %d battles: RED attacked %.1f%% of them, BLU %.1f%%",
		campaigns, total, 100*redShare, 100*(1-redShare))
	t.Logf("per campaign, RED's share of the attacking: median %.0f%%, middle half %.0f%%–%.0f%%",
		100*shares[len(shares)/2], 100*shares[len(shares)/4], 100*shares[3*len(shares)/4])

	// Wide on purpose. The claim being defended is "neither side is the
	// designated defender", not "the split is exactly even" — one campaign is
	// a story and stories are lopsided. A rule that made one side the
	// attacker would land far outside this.
	if redShare < 0.40 || redShare > 0.60 {
		t.Fatalf("RED is on the offensive in %.1f%% of battles across %d campaigns — "+
			"one side has become the designated attacker, which makes its allegiance "+
			"a choice of role rather than of loyalty", 100*redShare, campaigns)
	}
}
