// Package hostelect picks which player in a roster should run the listen
// server, and ranks the fallbacks used when that host disappears.
//
// The scoring is deliberately explicit rather than a single opaque number
// produced by heuristics: every match records why a host was chosen, so a bad
// choice can be traced to the input that caused it.
package hostelect

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Capabilities is what a client reports about its ability to host.
type Capabilities struct {
	CanHost             bool
	UploadKbps          uint32
	CPUScore            uint32
	MemoryMB            uint32
	SDRPopID            uint32
	HasPublicIP         bool
	IsDedicated         bool
	MaxPlayersSupported uint32
}

// History is what the coordinator has learned about a player over time. It is
// the difference between "looks good on paper" and "actually finishes matches".
type History struct {
	HostedOK      int // matches hosted through to a reported result
	HostedFailed  int // assigned host that never came up
	Abandons      int // left a live match they were hosting
	AvgServerFPS  float64
	LastPenaltyAt int64 // unix seconds; recent failures weigh more
}

// Candidate is one roster member under consideration.
type Candidate struct {
	SteamID uint64
	Caps    Capabilities
	Hist    History
	// Online is false for a roster member whose coordinator link is dead. Such
	// a player can never be host.
	Online bool
	// ExcludeReason, when set, disqualifies the candidate outright. Used to
	// keep a host that just failed from being re-elected immediately.
	ExcludeReason string
}

// Weights control the relative pull of each scoring term. They come from the
// coordinator config so they can be retuned without a rebuild.
type Weights struct {
	Upload         float64
	Latency        float64
	CPU            float64
	Stability      float64
	DedicatedBonus float64
	PublicIPBonus  float64
	AbandonPenalty float64
}

// Requirements describe the host we want. RequireCanHost is absolute; the
// numeric floors are relaxed by Elect when no candidate clears them, since a
// battle on a mediocre host beats no battle at all.
type Requirements struct {
	MinUploadKbps    uint32
	MinCPUScore      uint32
	MinMemoryMB      uint32
	MaxAcceptableRTT int
	RequireCanHost   bool
}

// LatencyOracle estimates the round trip, in milliseconds, a client would see
// against a given host.
type LatencyOracle interface {
	Estimate(client, host uint64) int
}

// Scored is one candidate's evaluation, kept for the match record.
type Scored struct {
	SteamID    uint64
	Total      float64
	Upload     float64
	Latency    float64
	CPU        float64
	Stability  float64
	Bonus      float64
	WorstRTT   int
	MeanRTT    int
	Eligible   bool
	Disqualify string
	// Degraded is set when this candidate only became eligible because the
	// numeric floors were relaxed to avoid having no battle at all.
	Degraded bool
}

func (s Scored) String() string {
	if !s.Eligible {
		return fmt.Sprintf("%d: ineligible (%s)", s.SteamID, s.Disqualify)
	}
	degraded := ""
	if s.Degraded {
		degraded = fmt.Sprintf(" DEGRADED: %s", s.Disqualify)
	}
	return fmt.Sprintf("%d: %.3f (up %.2f, lat %.2f rtt~%dms/max%dms, cpu %.2f, stab %.2f, bonus %.2f)%s",
		s.SteamID, s.Total, s.Upload, s.Latency, s.MeanRTT, s.WorstRTT, s.CPU, s.Stability, s.Bonus, degraded)
}

// Elector ranks candidates.
type Elector struct {
	W   Weights
	Req Requirements
	Lat LatencyOracle
	// UploadCeilingKbps is the upload rate above which more bandwidth stops
	// helping. Roughly what a 24-player Source server needs with headroom.
	UploadCeilingKbps uint32
}

// New builds an elector with a sensible upload ceiling.
func New(w Weights, req Requirements, lat LatencyOracle) *Elector {
	return &Elector{W: w, Req: req, Lat: lat, UploadCeilingKbps: 12000}
}

// Rank scores every candidate against the whole roster and returns them best
// first. Ineligible candidates are included, last, with the reason recorded —
// callers need them to explain "nobody here can host".
func (e *Elector) Rank(candidates []Candidate) []Scored {
	return e.rank(candidates, false)
}

func (e *Elector) rank(candidates []Candidate, relaxed bool) []Scored {
	out := make([]Scored, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, e.score(c, candidates, relaxed))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Eligible != out[j].Eligible {
			return out[i].Eligible
		}
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		// Deterministic tiebreak so repeated elections on identical input do
		// not oscillate between two equally good hosts.
		return out[i].SteamID < out[j].SteamID
	})
	return out
}

// Elect returns the best eligible candidate.
//
// The numeric floors describe the host we want, not the host we must have. When
// nobody clears them the election runs again with them relaxed, because at a
// small population refusing to form a battle is worse than forming one on a
// mediocre host. In practice the floor that disqualifies a whole roster is
// almost always the RTT ceiling, and that is a property of who happens to be
// queued together rather than of anybody's machine.
//
// Three disqualifications survive the relaxed pass, because they mean "cannot",
// not "poorly": the coordinator link is down, the candidate already failed to
// host this match, and the client itself reported it cannot host.
func (e *Elector) Elect(candidates []Candidate) (Scored, []Scored, bool) {
	ranked := e.rank(candidates, false)
	if len(ranked) > 0 && ranked[0].Eligible {
		return ranked[0], ranked, true
	}

	relaxed := e.rank(candidates, true)
	if len(relaxed) > 0 && relaxed[0].Eligible {
		return relaxed[0], relaxed, true
	}
	return Scored{}, ranked, false
}

func (e *Elector) score(c Candidate, roster []Candidate, relaxed bool) Scored {
	s := Scored{SteamID: c.SteamID, Eligible: true}

	// Absolute: these mean the candidate cannot host at all.
	switch {
	case c.ExcludeReason != "":
		s.Eligible, s.Disqualify = false, c.ExcludeReason
	case !c.Online:
		s.Eligible, s.Disqualify = false, "offline"
	case e.Req.RequireCanHost && !c.Caps.CanHost:
		s.Eligible, s.Disqualify = false, "client reports it cannot host"
	}

	// Preferred: a candidate below these hosts badly, but hosts.
	if s.Eligible {
		switch {
		case c.Caps.UploadKbps < e.Req.MinUploadKbps:
			s.Disqualify = fmt.Sprintf("upload %d kbps below floor %d", c.Caps.UploadKbps, e.Req.MinUploadKbps)
		case c.Caps.CPUScore < e.Req.MinCPUScore:
			s.Disqualify = fmt.Sprintf("cpu score %d below floor %d", c.Caps.CPUScore, e.Req.MinCPUScore)
		case c.Caps.MemoryMB < e.Req.MinMemoryMB:
			s.Disqualify = fmt.Sprintf("memory %d MB below floor %d", c.Caps.MemoryMB, e.Req.MinMemoryMB)
		case int(c.Caps.MaxPlayersSupported) > 0 && len(roster) > int(c.Caps.MaxPlayersSupported):
			s.Disqualify = fmt.Sprintf("supports %d players, roster is %d", c.Caps.MaxPlayersSupported, len(roster))
		}
		if s.Disqualify != "" {
			s.Eligible = relaxed
			s.Degraded = relaxed
		}
	}

	// Latency is computed even for ineligible candidates: it is useful in the
	// log when explaining why a match could not be formed.
	s.MeanRTT, s.WorstRTT = e.latencies(c, roster)
	if s.Eligible && e.Req.MaxAcceptableRTT > 0 && s.WorstRTT > e.Req.MaxAcceptableRTT {
		// The floor that most often takes out an entire roster at once: a group
		// spanning two continents has no member everyone is close to.
		s.Eligible = relaxed
		s.Degraded = relaxed
		s.Disqualify = fmt.Sprintf("worst-case rtt %dms above ceiling %dms", s.WorstRTT, e.Req.MaxAcceptableRTT)
	}

	s.Upload = clamp01(float64(c.Caps.UploadKbps) / float64(maxU32(e.UploadCeilingKbps, 1)))
	s.CPU = clamp01(float64(c.Caps.CPUScore) / 100.0)

	// Latency term punishes the worst-off player rather than the average. One
	// player on 200ms ruins the match even if everyone else is on 20ms.
	if e.Req.MaxAcceptableRTT > 0 {
		s.Latency = clamp01(1.0 - float64(s.WorstRTT)/float64(e.Req.MaxAcceptableRTT))
	} else {
		s.Latency = clamp01(1.0 - float64(s.WorstRTT)/250.0)
	}

	s.Stability = stability(c.Hist)

	s.Bonus = 0
	if c.Caps.IsDedicated {
		s.Bonus += e.W.DedicatedBonus
	}
	if c.Caps.HasPublicIP {
		s.Bonus += e.W.PublicIPBonus
	}
	s.Bonus -= e.W.AbandonPenalty * recentAbandonWeight(c.Hist)

	s.Total = e.W.Upload*s.Upload +
		e.W.Latency*s.Latency +
		e.W.CPU*s.CPU +
		e.W.Stability*s.Stability +
		s.Bonus

	return s
}

func (e *Elector) latencies(host Candidate, roster []Candidate) (mean, worst int) {
	if e.Lat == nil {
		return 0, 0
	}
	total, n := 0, 0
	for _, c := range roster {
		if c.SteamID == host.SteamID {
			continue
		}
		rtt := e.Lat.Estimate(c.SteamID, host.SteamID)
		total += rtt
		n++
		if rtt > worst {
			worst = rtt
		}
	}
	if n == 0 {
		return 0, 0
	}
	return total / n, worst
}

// stability rewards a track record of finishing matches and decays quickly with
// failures, since a host that flakes twice will very likely flake again.
func stability(h History) float64 {
	attempts := h.HostedOK + h.HostedFailed + h.Abandons
	if attempts == 0 {
		return 0.5 // unknown host: neither trusted nor punished
	}
	success := float64(h.HostedOK) / float64(attempts)
	// Confidence grows with sample size, so one lucky match does not outrank a
	// host with a long clean record.
	confidence := float64(attempts) / float64(attempts+3)
	return clamp01(0.5 + confidence*(success-0.5))
}

// recentAbandonWeight scales the abandon penalty by how many abandons there
// are, saturating so a serial abandoner cannot go arbitrarily negative.
func recentAbandonWeight(h History) float64 {
	if h.Abandons == 0 {
		return 0
	}
	return clamp01(float64(h.Abandons) / 3.0)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func maxU32(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Latency oracle
// ---------------------------------------------------------------------------

// PopOracle estimates latency from Steam Datagram Relay point-of-presence IDs
// plus whatever RTTs the coordinator has actually measured.
//
// Measured samples always win. POP identity is only the cold-start estimate,
// used before two players have ever been in a match together.
type PopOracle struct {
	mu sync.RWMutex

	pops map[uint64]uint32 // steamID -> POP
	// observed[client][host] = smoothed RTT in ms, from client heartbeats.
	observed map[uint64]map[uint64]float64

	// SamePopRTT, KnownPopRTT and UnknownRTT are the cold-start estimates.
	SamePopRTT   int
	KnownPopRTT  int
	UnknownRTT   int
	SmoothFactor float64
}

func NewPopOracle() *PopOracle {
	return &PopOracle{
		pops:         make(map[uint64]uint32),
		observed:     make(map[uint64]map[uint64]float64),
		SamePopRTT:   25,
		KnownPopRTT:  90,
		UnknownRTT:   110,
		SmoothFactor: 0.3,
	}
}

// SetPop records a player's nearest relay.
func (o *PopOracle) SetPop(steamID uint64, pop uint32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pops[steamID] = pop
}

// Observe folds a measured client-to-host RTT into the running estimate.
func (o *PopOracle) Observe(client, host uint64, rttMs int) {
	if rttMs <= 0 || rttMs > 5000 {
		return // nonsense sample; a client can report anything
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	m := o.observed[client]
	if m == nil {
		m = make(map[uint64]float64)
		o.observed[client] = m
	}
	prev, ok := m[host]
	if !ok {
		m[host] = float64(rttMs)
		return
	}
	m[host] = prev + o.SmoothFactor*(float64(rttMs)-prev)
}

// Estimate returns the expected RTT between a client and a prospective host.
func (o *PopOracle) Estimate(client, host uint64) int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if m, ok := o.observed[client]; ok {
		if v, ok := m[host]; ok {
			return int(v)
		}
	}
	// The link is symmetric enough that the reverse sample is a fair proxy.
	if m, ok := o.observed[host]; ok {
		if v, ok := m[client]; ok {
			return int(v)
		}
	}
	pa, oka := o.pops[client]
	pb, okb := o.pops[host]
	switch {
	case !oka || !okb:
		return o.UnknownRTT
	case pa == pb:
		return o.SamePopRTT
	default:
		return o.KnownPopRTT
	}
}

// Describe renders the ranking for the match log.
func Describe(ranked []Scored) string {
	var b strings.Builder
	for i, s := range ranked {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(s.String())
	}
	return b.String()
}
