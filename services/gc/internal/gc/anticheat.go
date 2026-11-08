package gc

import (
	"errors"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/anticheat"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/reports"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func init() {
	onServer(gcpb.ETFGCMsg_k_EMsgGC_Frontress_AntiCheatEvent, (*GC).handleAntiCheatEvent)
}

var antiCheatKinds = map[gcpb.CMsgGC_Frontress_AntiCheatEvent_EEvent]string{
	gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_VERIFIED:          anticheat.EventVerified,
	gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_KICKED:            anticheat.EventKicked,
	gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_NOT_PROTECTED:     anticheat.EventNotProtected,
	gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_IDENTITY_MISMATCH: anticheat.EventIdentityMismatch,
	gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_VIOLATION:         anticheat.EventViolation,
	gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_TEMP_BANNED:       anticheat.EventTempBanned,
	gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_PERM_BANNED:       anticheat.EventPermBanned,
	gcpb.CMsgGC_Frontress_AntiCheatEvent_kEvent_VOTE_KICKED:       anticheat.EventVoteKicked,
}

// handleAntiCheatEvent records what Easy Anti-Cheat decided about a player on
// a protected server. A ban from a server the operator trusts
// (anticheat_authority) also keeps the player out of matchmaking.
func (g *GC) handleAntiCheatEvent(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGC_Frontress_AntiCheatEvent
	if !g.parse(m, &msg) {
		return
	}
	srv := g.servers[s.SteamID]
	if srv == nil || srv.Session != s {
		return
	}
	id := msg.GetSteamId()
	kind, ok := antiCheatKinds[msg.GetEvent()]
	if !steamauth.ValidIndividual(id) || !ok {
		g.log.Warn("anti-cheat event refused", "server", s.SteamID, "steamid", id, "event", msg.GetEvent().String())
		return
	}

	now := g.now()
	ev := anticheat.Event{
		At:       now,
		Kind:     kind,
		Reason:   msg.GetActionReason(),
		Details:  truncate(msg.GetDetails(), 300),
		Server:   s.ServerName,
		Enforced: msg.GetEnforced(),
	}
	if mid := msg.GetMatchId(); mid != 0 {
		ev.MatchID = lobbyKey(mid)
	}

	var sanction *anticheat.Sanction
	if msg.GetEnforced() && s.AntiCheatAuthority {
		switch kind {
		case anticheat.EventTempBanned:
			sanction = &anticheat.Sanction{Kind: "temporary", Since: now, Until: now.Add(g.cfg.AntiCheat.TempBan()),
				Reason: "Easy Anti-Cheat temporary ban", By: s.ServerName}
		case anticheat.EventPermBanned:
			sanction = &anticheat.Sanction{Kind: "permanent", Since: now,
				Reason: "Easy Anti-Cheat permanent ban", By: s.ServerName}
		}
	}

	puid := truncate(msg.GetProductUserId(), 64)
	if err := g.anticheat.Add(id, puid, ev, sanction); err != nil {
		g.log.Error("save anti-cheat event", "steamid", id, "err", err)
		return
	}
	if kind == anticheat.EventVerified {
		g.log.Debug("anti-cheat verified", "steamid", id, "puid", puid, "server", s.ServerName)
		return
	}
	g.log.Info("anti-cheat event", "steamid", id, "event", kind, "reason", ev.Reason, "details", ev.Details,
		"server", s.ServerName, "enforced", ev.Enforced, "sanctioned", sanction != nil)
	if sanction != nil {
		g.applySanction(id)
		g.closeReportCase(id, reports.OutcomeActioned, "eac:"+s.ServerName, sanction.Reason)
		if sanction.Kind == "permanent" && g.cfg.AntiCheat.SteamGameBans.Auto() {
			if err := g.steamGameBan(id, sanction.Reason, "eac:"+s.ServerName, steamBanEAC); err != nil &&
				!errors.Is(err, ErrSteamBanned) {
				g.log.Warn("steam game ban after an EAC ban", "steamid", id, "err", err)
			}
		}
	}
}

// applySanction takes a sanctioned player's party out of every queue at
// once, instead of waiting for the match that would form around them.
func (g *GC) applySanction(id uint64) {
	sc, ok := g.anticheat.Sanctioned(id, g.now())
	if !ok {
		return
	}
	pl := g.online[id]
	if pl == nil {
		return
	}
	p := g.parties[pl.PartyID]
	if p == nil || len(p.Queues) == 0 {
		return
	}
	for grp := range p.Queues {
		delete(p.Queues, grp)
	}
	for _, member := range p.Members {
		if member == id {
			g.notice(member, "You are "+sc.Describe()+".")
		} else {
			g.notice(member, "A member of your party is "+sc.Describe()+"; the search stopped.")
		}
	}
	g.publishParty(p)
}

// antiCheatBlocked is the reason a player may not queue, or "".
func (g *GC) antiCheatBlocked(id uint64) string {
	if sc, ok := g.anticheat.Sanctioned(id, g.now()); ok {
		return sc.Describe()
	}
	return ""
}

// serverEligible: may this server be handed a new match?
func (g *GC) serverEligible(srv *Server, now time.Time) bool {
	if !srv.idle(now) {
		return false
	}
	return !g.cfg.AntiCheat.RequireProtectedServers || srv.Protected
}

// AntiCheatRecord is a player's record, for the admin API.
func (g *GC) AntiCheatRecord(id uint64) (anticheat.Record, bool) { return g.anticheat.Get(id) }

// AntiCheatSanctions lists active sanctions, for the admin API.
func (g *GC) AntiCheatSanctions() []anticheat.Record { return g.anticheat.ActiveSanctions(g.now()) }

// AntiCheatBan bans a player from matchmaking by hand; hours <= 0 is
// permanent.
func (g *GC) AntiCheatBan(id uint64, hours int, reason, by string) error {
	now := g.now()
	sc := &anticheat.Sanction{Kind: "manual", Since: now, Reason: reason, By: by}
	if hours > 0 {
		sc.Until = now.Add(time.Duration(hours) * time.Hour)
	}
	ev := anticheat.Event{At: now, Kind: anticheat.EventManualBan, Details: reason, Server: by, Enforced: true}
	if err := g.anticheat.Add(id, "", ev, sc); err != nil {
		return err
	}
	g.log.Info("anti-cheat manual ban", "steamid", id, "hours", hours, "by", by, "reason", reason)
	g.applySanction(id)
	g.closeReportCase(id, reports.OutcomeActioned, by, reason)
	return nil
}

// AntiCheatClear lifts a player's sanction.
func (g *GC) AntiCheatClear(id uint64, reason, by string) error {
	if err := g.anticheat.Clear(id, by, reason, g.now()); err != nil {
		return err
	}
	g.log.Info("anti-cheat sanction lifted", "steamid", id, "by", by, "reason", reason)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
