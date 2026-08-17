package hostelect

import "testing"

func testElector(lat LatencyOracle) *Elector {
	return New(
		Weights{Upload: 0.30, Latency: 0.35, CPU: 0.15, Stability: 0.20,
			DedicatedBonus: 0.50, PublicIPBonus: 0.10, AbandonPenalty: 0.35},
		Requirements{MinUploadKbps: 1500, MinCPUScore: 40, MinMemoryMB: 2048,
			MaxAcceptableRTT: 180, RequireCanHost: true},
		lat,
	)
}

func goodCaps() Capabilities {
	return Capabilities{CanHost: true, UploadKbps: 8000, CPUScore: 70, MemoryMB: 8192}
}

// fixedOracle returns a constant RTT for every pair except those in the map.
type fixedOracle struct {
	base int
	pair map[[2]uint64]int
}

func (f fixedOracle) Estimate(client, host uint64) int {
	if v, ok := f.pair[[2]uint64{client, host}]; ok {
		return v
	}
	return f.base
}

func TestElectsBestConnectedCandidate(t *testing.T) {
	e := testElector(fixedOracle{base: 40})
	cands := []Candidate{
		{SteamID: 1, Online: true, Caps: Capabilities{CanHost: true, UploadKbps: 2000, CPUScore: 45, MemoryMB: 4096}},
		{SteamID: 2, Online: true, Caps: goodCaps()},
		{SteamID: 3, Online: true, Caps: Capabilities{CanHost: true, UploadKbps: 3000, CPUScore: 50, MemoryMB: 4096}},
	}
	got, ranked, ok := e.Elect(cands)
	if !ok {
		t.Fatalf("expected an eligible host, ranking: %s", Describe(ranked))
	}
	if got.SteamID != 2 {
		t.Fatalf("expected the best-provisioned candidate (2), got %d: %s", got.SteamID, Describe(ranked))
	}
}

// These mean "cannot host", so no amount of relaxation may elect them.
func TestAbsoluteDisqualifiersSurviveRelaxation(t *testing.T) {
	e := testElector(fixedOracle{base: 40})
	cases := []struct {
		name string
		c    Candidate
	}{
		{"offline", Candidate{SteamID: 1, Online: false, Caps: goodCaps()}},
		{"cannot host", Candidate{SteamID: 1, Online: true, Caps: Capabilities{UploadKbps: 8000, CPUScore: 70, MemoryMB: 8192}}},
		{"already failed this match", Candidate{SteamID: 1, Online: true, Caps: goodCaps(), ExcludeReason: "already failed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ranked, ok := e.Elect([]Candidate{tc.c}); ok {
				t.Fatalf("expected no eligible host, got %s", Describe(ranked))
			}
		})
	}
}

// These mean "hosts badly". With nobody better available the battle should
// still happen, flagged as degraded.
func TestPreferredFloorsRelaxWhenNobodyClearsThem(t *testing.T) {
	e := testElector(fixedOracle{base: 40})
	cases := []struct {
		name string
		caps Capabilities
	}{
		{"slow upload", Capabilities{CanHost: true, UploadKbps: 100, CPUScore: 70, MemoryMB: 8192}},
		{"weak cpu", Capabilities{CanHost: true, UploadKbps: 8000, CPUScore: 5, MemoryMB: 8192}},
		{"low memory", Capabilities{CanHost: true, UploadKbps: 8000, CPUScore: 70, MemoryMB: 512}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ranked, ok := e.Elect([]Candidate{{SteamID: 1, Online: true, Caps: tc.caps}})
			if !ok {
				t.Fatalf("expected a degraded host rather than no battle: %s", Describe(ranked))
			}
			if !got.Degraded {
				t.Fatal("host elected below a floor should be marked degraded")
			}
			if got.Disqualify == "" {
				t.Fatal("a degraded election must record what fell short")
			}
		})
	}
}

// A roster spanning two continents has no member everyone is close to. That is
// a property of the group, so it must not cost them the battle.
func TestRTTCeilingRelaxesForAWholeRoster(t *testing.T) {
	e := testElector(fixedOracle{base: 400})
	got, ranked, ok := e.Elect([]Candidate{
		{SteamID: 1, Online: true, Caps: goodCaps()},
		{SteamID: 2, Online: true, Caps: goodCaps()},
	})
	if !ok {
		t.Fatalf("expected a degraded host: %s", Describe(ranked))
	}
	if !got.Degraded || got.WorstRTT != 400 {
		t.Fatalf("expected a degraded 400ms election, got degraded=%v rtt=%d", got.Degraded, got.WorstRTT)
	}
}

// Relaxation is a last resort: if anyone clears the floors, they win outright.
func TestQualifiedCandidateBeatsDegradedOne(t *testing.T) {
	e := testElector(fixedOracle{base: 40})
	got, ranked, ok := e.Elect([]Candidate{
		{SteamID: 1, Online: true, Caps: Capabilities{CanHost: true, UploadKbps: 100, CPUScore: 5, MemoryMB: 512}},
		{SteamID: 2, Online: true, Caps: goodCaps()},
	})
	if !ok {
		t.Fatal("expected an eligible host")
	}
	if got.SteamID != 2 || got.Degraded {
		t.Fatalf("the qualified candidate should win outright, got %s", Describe(ranked))
	}
}

func TestPlayerTooSmallForRosterIsIneligible(t *testing.T) {
	e := testElector(fixedOracle{base: 40})
	caps := goodCaps()
	caps.MaxPlayersSupported = 4
	roster := []Candidate{
		{SteamID: 1, Online: true, Caps: caps},
		{SteamID: 2, Online: true, Caps: goodCaps()},
		{SteamID: 3, Online: true, Caps: goodCaps()},
		{SteamID: 4, Online: true, Caps: goodCaps()},
		{SteamID: 5, Online: true, Caps: goodCaps()},
	}
	ranked := e.Rank(roster)
	for _, r := range ranked {
		if r.SteamID == 1 && r.Eligible {
			t.Fatalf("candidate 1 supports 4 players but the roster is 5; should be ineligible")
		}
	}
}

// The latency term must punish the worst-off player, not the average: one
// player on a bad link ruins the match for everyone.
func TestLatencyScoredOnWorstCase(t *testing.T) {
	lat := fixedOracle{base: 20, pair: map[[2]uint64]int{
		{3, 1}: 170, // candidate 1 is far from player 3 only
	}}
	e := testElector(lat)
	cands := []Candidate{
		{SteamID: 1, Online: true, Caps: goodCaps()},
		{SteamID: 2, Online: true, Caps: goodCaps()},
		{SteamID: 3, Online: true, Caps: goodCaps()},
	}
	got, ranked, ok := e.Elect(cands)
	if !ok {
		t.Fatal("expected an eligible host")
	}
	if got.SteamID == 1 {
		t.Fatalf("candidate 1 has a 170ms outlier and should lose: %s", Describe(ranked))
	}
}

func TestStabilityBeatsRawSpecs(t *testing.T) {
	e := testElector(fixedOracle{base: 30})
	// A slightly weaker machine with a clean record should beat a stronger one
	// that keeps abandoning.
	cands := []Candidate{
		{SteamID: 1, Online: true,
			Caps: Capabilities{CanHost: true, UploadKbps: 12000, CPUScore: 95, MemoryMB: 16384},
			Hist: History{HostedOK: 1, Abandons: 4}},
		{SteamID: 2, Online: true,
			Caps: Capabilities{CanHost: true, UploadKbps: 7000, CPUScore: 70, MemoryMB: 8192},
			Hist: History{HostedOK: 20}},
	}
	got, ranked, ok := e.Elect(cands)
	if !ok {
		t.Fatal("expected an eligible host")
	}
	if got.SteamID != 2 {
		t.Fatalf("the reliable host should win, got %d: %s", got.SteamID, Describe(ranked))
	}
}

func TestRankingIsDeterministic(t *testing.T) {
	e := testElector(fixedOracle{base: 30})
	cands := []Candidate{
		{SteamID: 30, Online: true, Caps: goodCaps()},
		{SteamID: 10, Online: true, Caps: goodCaps()},
		{SteamID: 20, Online: true, Caps: goodCaps()},
	}
	first := e.Rank(cands)
	for i := 0; i < 20; i++ {
		again := e.Rank(cands)
		for j := range first {
			if first[j].SteamID != again[j].SteamID {
				t.Fatalf("ranking is unstable at position %d: %d vs %d", j, first[j].SteamID, again[j].SteamID)
			}
		}
	}
}

func TestPopOraclePrefersMeasuredSamples(t *testing.T) {
	o := NewPopOracle()
	o.SetPop(1, 100)
	o.SetPop(2, 200)

	if got := o.Estimate(1, 2); got != o.KnownPopRTT {
		t.Fatalf("cold start across POPs: want %d, got %d", o.KnownPopRTT, got)
	}
	o.Observe(1, 2, 35)
	if got := o.Estimate(1, 2); got != 35 {
		t.Fatalf("first measurement should be used verbatim, got %d", got)
	}
	// The reverse direction has no samples of its own but should reuse this one.
	if got := o.Estimate(2, 1); got != 35 {
		t.Fatalf("reverse estimate should fall back to the observed sample, got %d", got)
	}
	// Nonsense samples must not poison the estimate.
	o.Observe(1, 2, -5)
	o.Observe(1, 2, 99999)
	if got := o.Estimate(1, 2); got != 35 {
		t.Fatalf("out-of-range samples should be ignored, got %d", got)
	}
}

func TestSamePopIsCheaperThanUnknown(t *testing.T) {
	o := NewPopOracle()
	o.SetPop(1, 42)
	o.SetPop(2, 42)
	if o.Estimate(1, 2) >= o.Estimate(1, 99) {
		t.Fatal("same-POP estimate should beat the unknown-peer estimate")
	}
}
