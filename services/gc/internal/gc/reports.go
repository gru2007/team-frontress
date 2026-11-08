package gc

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/anticheat"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/reports"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func init() {
	onClient(gcpb.ETFGCMsg_k_EMsgGC_ReportPlayer, (*GC).handleReportPlayer)
}

var reportSources = map[gcpb.CMsgGC_ReportPlayer_ESource]string{
	gcpb.CMsgGC_ReportPlayer_kSource_MENU:       "menu",
	gcpb.CMsgGC_ReportPlayer_kSource_SCOREBOARD: "scoreboard",
	gcpb.CMsgGC_ReportPlayer_kSource_KILLER:     "killer",
	gcpb.CMsgGC_ReportPlayer_kSource_RESULTS:    "results",
}

func (g *GC) handleReportPlayer(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGC_ReportPlayer
	if !g.parse(m, &msg) {
		return
	}
	target := steamauth.MinIndividual + uint64(msg.GetAccountIdTarget())
	if msg.GetAccountIdTarget() == 0 || target == s.SteamID || !steamauth.ValidIndividual(s.SteamID) ||
		msg.GetReason() <= gcpb.CMsgGC_ReportPlayer_kReason_INVALID || msg.GetReason() >= gcpb.CMsgGC_ReportPlayer_kReason_COUNT {
		g.fail(s.SteamID, "report_invalid")
		return
	}
	r := reports.Report{
		Reporter: lobbyKey(s.SteamID), Target: lobbyKey(target), Reason: int32(msg.GetReason()), At: g.now(),
		Detail:   reportDetail(int32(msg.GetReason()), msg.GetDetail()),
		Comment:  cleanLine(msg.GetComment(), 200),
		Source:   reportSources[msg.GetSource()],
		KilledBy: min(msg.GetKilledByTarget(), 99),
		Server:   cleanAddress(msg.GetServerAddress()),
		Map:      cleanLine(msg.GetMap(), 64),
		EAC:      msg.GetEacForwarded(),
	}
	if p := g.online[s.SteamID]; p != nil {
		if party := g.parties[p.PartyID]; party != nil && len(party.Members) > 1 {
			r.Party = strconv.FormatUint(party.ID, 10)
		}
	}
	if l := g.lobbyOf(s.SteamID); l != nil && l.member(target) != nil {
		r.MatchID = lobbyKey(l.MatchID)
	} else {
		// Reports from direct community-server connections are also accepted.
		// Attach archived match context only when both players really shared it.
		for _, grp := range g.cfg.MatchGroups {
			for _, match := range g.history.Results(grp.MatchGroup, r.Reporter, r.At.Add(-24*time.Hour)) {
				for _, player := range match.Members {
					if player.SteamID == r.Target {
						r.MatchID = match.MatchID
						break
					}
				}
				if r.MatchID != "" {
					break
				}
			}
			if r.MatchID != "" {
				break
			}
		}
	}
	if err := g.reports.Add(r); err != nil {
		if errors.Is(err, reports.ErrCooldown) {
			g.fail(s.SteamID, "report_cooldown")
		} else {
			g.log.Error("save player report", "err", err)
			g.fail(s.SteamID, "report_save")
		}
		return
	}
	g.log.Info("player report saved", "reporter", s.SteamID, "target", target, "reason", r.Reason,
		"detail", r.Detail, "match", r.MatchID, "server", r.Server, "eac", r.EAC)
	g.emit(s.SteamID, "report_saved", map[string]any{"target": strconv.FormatUint(target, 10)})
	if r.Reason == reports.ReasonCheating {
		g.forwardCheatingReport(s.SteamID, target, s.AppID, r.Detail)
	}
}

// reportDetail keeps the detail bits that belong to the reason.
func reportDetail(reason int32, bits uint32) uint32 {
	switch reason {
	case reports.ReasonCheating:
		return bits & reports.CheatingDetails
	case reports.ReasonHarassment:
		return bits & reports.HarassmentDetails
	}
	return 0
}

// cleanAddress keeps a server address (host:port, IPv6 in brackets, or a
// Steam Datagram Relay name) and nothing else.
func cleanAddress(s string) string {
	if len(s) > 64 {
		return ""
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.ContainsRune(".:[]-_", c)) {
			return ""
		}
	}
	return s
}

// --- cases --------------------------------------------------------------------

// reportWindow is how far back a case's reports and anti-cheat events count
// toward its priority. Older ones are still shown.
const reportWindow = 30 * 24 * time.Hour

// CaseEAC is what Easy Anti-Cheat said about a reported player.
type CaseEAC struct {
	ProductUserID    string              `json:"product_user_id,omitempty"`
	VerifiedAt       time.Time           `json:"verified_at,omitempty"` // last protected session; zero: EAC has never seen them
	Violations       int                 `json:"violations"`
	IdentityMismatch int                 `json:"identity_mismatch"`
	Kicks            int                 `json:"kicks"`
	NotProtected     int                 `json:"not_protected"`
	VoteKicks        int                 `json:"vote_kicks"`
	Sanction         *anticheat.Sanction `json:"sanction,omitempty"`
	SteamBanned      bool                `json:"steam_banned,omitempty"` // a Steam game ban from us
}

// ReportCase is one reported player, as moderators see the queue.
type ReportCase struct {
	SteamID    string              `json:"steam_id"`
	Name       string              `json:"name,omitempty"`
	Status     string              `json:"status"` // open, actioned, dismissed
	Score      float64             `json:"score"`
	Severity   string              `json:"severity"` // critical, high, medium, low
	Open       int                 `json:"open_reports"`
	Total      int                 `json:"total_reports"`
	Reporters  int                 `json:"reporters"` // distinct reporters, a party counting once
	Reasons    map[string]int      `json:"reasons"`
	Details    map[string]int      `json:"details"`
	Matches    int                 `json:"matches"`
	LastReport time.Time           `json:"last_report"`
	Flags      []string            `json:"flags"`
	EAC        CaseEAC             `json:"eac"`
	Resolution *reports.Resolution `json:"resolution,omitempty"`
}

// ReportView is one report with who filed it.
type ReportView struct {
	reports.Report
	ReporterName   string   `json:"reporter_name,omitempty"`
	ReporterWeight float64  `json:"reporter_weight"`
	Details        []string `json:"details,omitempty"`
	ReasonName     string   `json:"reason_name"`
	Open           bool     `json:"open"`
}

// ReportCaseDetail is a case with everything behind it.
type ReportCaseDetail struct {
	ReportCase
	Reports     []ReportView         `json:"reports"`
	AntiCheat   anticheat.Record     `json:"anticheat"`
	Resolutions []reports.Resolution `json:"resolutions"`
	// SteamBans: may this console issue Steam game bans, and is one on its
	// way to Steam for this player right now.
	SteamBans       bool `json:"steam_bans"`
	SteamBanPending bool `json:"steam_ban_pending"`
}

var reasonWeights = map[int32]float64{
	reports.ReasonCheating:   3,
	reports.ReasonGriefing:   1.5,
	reports.ReasonHarassment: 1,
	reports.ReasonIdle:       0.75,
}

// reportCase scores a reported player. Reports count once per reporter (or
// party), by reason and by how reliable that reporter has been; what Easy
// Anti-Cheat recorded about the player counts on top. Reports alone never
// ban anyone: the score orders the moderators' queue.
func (g *GC) reportCase(target string) ReportCase {
	now := g.now()
	all := g.reports.About(target)
	open := g.reports.OpenReports(target)
	c := ReportCase{SteamID: target, Open: len(open), Total: len(all), Reasons: map[string]int{}, Details: map[string]int{}, Status: "open"}
	if id, err := strconv.ParseUint(target, 10, 64); err == nil {
		c.Name = g.playerName(id)
	}
	if res := g.reports.Resolutions(target); len(res) > 0 {
		last := res[len(res)-1]
		c.Resolution = &last
		if len(open) == 0 {
			c.Status = last.Outcome
		}
		for _, r := range res {
			if r.Outcome == reports.OutcomeActioned {
				c.Flags = append(c.Flags, "repeat_offender")
				c.Score += 3
				break
			}
		}
	}
	if len(all) > 0 {
		c.LastReport = all[0].At
	}

	// Each reporter group counts once, at its heaviest reason.
	groups := map[string]float64{}
	killers := map[string]bool{}
	matches := map[string]bool{}
	for _, r := range open {
		c.Reasons[reports.ReasonName(r.Reason)]++
		for _, d := range reports.DetailNames {
			if r.Detail&d.Bit != 0 {
				c.Details[d.Name]++
			}
		}
		if r.MatchID != "" {
			matches["m:"+r.MatchID] = true
		} else if r.Server != "" {
			matches["s:"+r.Server+"/"+r.Map] = true
		}
		if now.Sub(r.At) > reportWindow {
			continue
		}
		w := reasonWeights[r.Reason] * g.reports.Standing(r.Reporter).Weight()
		if w > groups[r.Group()] {
			groups[r.Group()] = w
		}
		if r.Reason == reports.ReasonCheating && r.KilledBy > 0 {
			killers[r.Group()] = true
		}
	}
	c.Reporters = len(groups)
	c.Matches = len(matches)
	for _, w := range groups {
		c.Score += w
	}
	if c.Matches > 1 {
		c.Score += math.Min(float64(c.Matches-1), 4)
	}
	if c.Matches >= 3 {
		c.Flags = append(c.Flags, "many_matches")
	}
	if len(killers) >= 2 {
		c.Flags = append(c.Flags, "killed_reporters")
		c.Score += 1
	}

	if id, err := strconv.ParseUint(target, 10, 64); err == nil {
		c.EAC = g.caseEAC(id, now)
	}
	e := c.EAC
	c.Score += math.Min(float64(e.Violations)*6, 18) + float64(min(e.IdentityMismatch, 1))*4 +
		math.Min(float64(e.VoteKicks)*3, 9) + math.Min(float64(e.Kicks)*0.5, 2) + math.Min(float64(e.NotProtected), 3)
	if e.Sanction != nil {
		c.Flags = append(c.Flags, "sanctioned")
	}
	if e.Violations > 0 {
		c.Flags = append(c.Flags, "eac_violation")
	}
	if e.IdentityMismatch > 0 {
		c.Flags = append(c.Flags, "identity_mismatch")
	}
	if e.VoteKicks > 0 {
		c.Flags = append(c.Flags, "vote_kicked")
	}
	if e.VerifiedAt.IsZero() {
		// Every report came from a server without EAC: only people saw it.
		c.Flags = append(c.Flags, "never_protected")
	}

	c.Score = math.Round(c.Score*10) / 10
	switch {
	case c.Status != "open":
		c.Severity = "closed"
	case c.Score >= 12:
		c.Severity = "critical"
	case c.Score >= 6:
		c.Severity = "high"
	case c.Score >= 3:
		c.Severity = "medium"
	default:
		c.Severity = "low"
	}
	if c.Flags == nil {
		c.Flags = []string{}
	}
	return c
}

func (g *GC) caseEAC(id uint64, now time.Time) CaseEAC {
	rec, _ := g.anticheat.Get(id)
	out := CaseEAC{ProductUserID: rec.ProductUserID, VerifiedAt: rec.VerifiedAt, SteamBanned: rec.SteamBan != nil}
	if sc, ok := g.anticheat.Sanctioned(id, now); ok {
		out.Sanction = &sc
	}
	for _, ev := range rec.Events {
		if now.Sub(ev.At) > reportWindow {
			continue
		}
		switch ev.Kind {
		case anticheat.EventViolation, anticheat.EventTempBanned, anticheat.EventPermBanned:
			out.Violations++
		case anticheat.EventIdentityMismatch:
			out.IdentityMismatch++
		case anticheat.EventKicked:
			out.Kicks++
		case anticheat.EventNotProtected:
			out.NotProtected++
		case anticheat.EventVoteKicked:
			out.VoteKicks++
		}
	}
	return out
}

func (g *GC) playerName(id uint64) string {
	if p := g.online[id]; p != nil && p.Name != "" {
		return p.Name
	}
	return g.players.Get(id).Name
}

// ReportCases is the moderators' queue: open cases by priority, or with
// status "closed" the ones already handled (newest first), or "all".
func (g *GC) ReportCases(status string, limit int) []ReportCase {
	var out []ReportCase
	for _, target := range g.reports.Targets() {
		c := g.reportCase(target)
		switch {
		case status == "all":
		case status == "closed" && c.Status != "open":
		case (status == "" || status == "open") && c.Status == "open":
		default:
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status == "open" && out[j].Status == "open" && out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].LastReport.After(out[j].LastReport)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []ReportCase{}
	}
	return out
}

// ReportCaseDetail is one case with its reports and anti-cheat record.
func (g *GC) ReportCaseDetail(id uint64) (ReportCaseDetail, bool) {
	target := lobbyKey(id)
	all := g.reports.About(target)
	if len(all) == 0 {
		return ReportCaseDetail{}, false
	}
	d := ReportCaseDetail{ReportCase: g.reportCase(target), Resolutions: g.reports.Resolutions(target),
		SteamBans: g.steamBansOn(), SteamBanPending: g.steamBanBusy[id]}
	if d.Resolutions == nil {
		d.Resolutions = []reports.Resolution{}
	}
	openSince := time.Time{}
	if d.Resolution != nil {
		openSince = d.Resolution.At
	}
	for _, r := range all {
		v := ReportView{Report: r, ReasonName: reports.ReasonName(r.Reason), Open: r.At.After(openSince)}
		v.ReporterWeight = math.Round(g.reports.Standing(r.Reporter).Weight()*100) / 100
		if rid, err := strconv.ParseUint(r.Reporter, 10, 64); err == nil {
			v.ReporterName = g.playerName(rid)
		}
		for _, dn := range reports.DetailNames {
			if r.Detail&dn.Bit != 0 {
				v.Details = append(v.Details, dn.Name)
			}
		}
		d.Reports = append(d.Reports, v)
	}
	d.AntiCheat, _ = g.anticheat.Get(id)
	if d.AntiCheat.Events == nil {
		d.AntiCheat.Events = []anticheat.Event{}
	}
	return d, true
}

// DismissReportCase closes a case without action.
func (g *GC) DismissReportCase(id uint64, by, note string) error {
	return g.closeReportCaseErr(id, reports.OutcomeDismissed, by, note)
}

// closeReportCase closes a player's open case, if there is one, after a ban.
func (g *GC) closeReportCase(id uint64, outcome, by, note string) {
	if err := g.closeReportCaseErr(id, outcome, by, note); err != nil && !errors.Is(err, reports.ErrNothingOpen) {
		g.log.Error("close report case", "steamid", id, "err", err)
	}
}

func (g *GC) closeReportCaseErr(id uint64, outcome, by, note string) error {
	reporters, err := g.reports.Resolve(lobbyKey(id), outcome, by, note, g.now())
	if err != nil {
		return err
	}
	g.log.Info("report case closed", "steamid", id, "outcome", outcome, "by", by, "reporters", len(reporters))
	if outcome == reports.OutcomeActioned {
		for _, rep := range reporters {
			if rid, err := strconv.ParseUint(rep, 10, 64); err == nil {
				g.sendReportFeedback(rid)
			}
		}
	}
	return nil
}

// sendReportFeedback tells a reporter, once they are in the game, that
// action was taken against players they reported. Never who: a report is
// private, and naming the player would tell a cheater who reported them.
func (g *GC) sendReportFeedback(id uint64) {
	if !g.isOnline(id) {
		return
	}
	n, err := g.reports.TakeFeedback(lobbyKey(id))
	if err != nil {
		g.log.Error("report feedback", "steamid", id, "err", err)
		return
	}
	if n > 0 {
		g.emit(id, "report_feedback", map[string]any{"count": n})
	}
}
