package gc

import (
	"errors"
	"strconv"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/reports"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func init() {
	onClient(gcpb.ETFGCMsg_k_EMsgGC_ReportPlayer, (*GC).handleReportPlayer)
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
	r := reports.Report{Reporter: lobbyKey(s.SteamID), Target: lobbyKey(target), Reason: int32(msg.GetReason()), At: g.now()}
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
	g.log.Info("player report saved", "reporter", s.SteamID, "target", target, "reason", r.Reason, "match", r.MatchID)
	g.emit(s.SteamID, "report_saved", map[string]any{"target": strconv.FormatUint(target, 10)})
}
