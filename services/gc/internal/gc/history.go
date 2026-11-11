package gc

import (
	"strconv"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/history"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
)

// MATCH_FLAG_PLAYER_* in tf_matchmaking_shared.h.
const (
	matchPlayerAbandoned   = 1 << 2
	matchPlayerPlayed      = 1 << 3
	matchPlayerNeverActive = 1 << 5
)

func (g *GC) newMatchResult(l *Lobby, msg *gcpb.CMsgGC_Match_Result) *history.Result {
	r := &history.Result{
		WinningTeam: msg.GetWinningTeam(), RedScore: msg.GetRedScore(), BlueScore: msg.GetBlueScore(),
		Duration: msg.GetDuration(), Status: int32(msg.GetStatus()), MapIndex: msg.GetMapIndex(),
		GameType: msg.GetGameType(), WinReason: msg.GetWinReason(), Flags: msg.GetFlags(), Bots: msg.GetBots(),
		RecordedAt: g.now(), Players: []history.PlayerResult{},
	}
	archived, _ := g.history.Get(lobbyKey(l.MatchID))
	roster := make(map[uint64]bool, len(archived.Members)+len(l.Members))
	for _, p := range archived.Members {
		if id, err := strconv.ParseUint(p.SteamID, 10, 64); err == nil && id != 0 {
			roster[id] = true
		}
	}
	for _, p := range l.Members {
		roster[p.SteamID] = true
	}
	seen := make(map[uint64]bool, len(roster))
	validPlayers := msg.Players[:0]
	for _, p := range msg.GetPlayers() {
		id := p.GetSteamId()
		if !roster[id] || seen[id] {
			continue
		}
		seen[id] = true
		validPlayers = append(validPlayers, p)
		flags := p.GetFlags()
		row := history.PlayerResult{
			SteamID: lobbyKey(id), Team: p.GetTeam(), Score: p.GetScore(), Kills: p.GetKills(),
			Deaths: p.GetDeaths(), Damage: p.GetDamage(), Healing: p.GetHealing(), Support: p.GetSupport(),
			Ping: p.GetPing(), Flags: flags, Rank: p.GetRank(), Classes: p.GetClassesPlayed(),
			Medals:      [5]uint32{p.GetScoreMedal(), p.GetKillsMedal(), p.GetDamageMedal(), p.GetHealingMedal(), p.GetSupportMedal()},
			LeaveReason: int32(p.GetLeaveReason()), LeaveTime: p.GetLeaveTime(), ConnectTime: p.GetConnectTime(),
			Excluded: !r.Completed() || l.State != lobbyRun || l.member(id) == nil || (p.GetTeam() != 2 && p.GetTeam() != 3) ||
				(p.Flags != nil && (flags&matchPlayerPlayed == 0 || flags&(matchPlayerAbandoned|matchPlayerNeverActive) != 0)),
		}
		if party := p.GetOriginalPartyId(); party != 0 {
			row.PartyID = lobbyKey(party)
		}
		for i, medal := range row.Medals {
			if medal > 3 {
				row.Medals[i] = 0
			}
		}
		for _, source := range p.GetXpBreakdown() {
			row.XPSources = append(row.XPSources, history.XPSource{Type: int32(source.GetType()), Amount: source.GetAmount()})
		}
		r.Players = append(r.Players, row)
	}
	msg.Players = validPlayers
	for _, event := range msg.GetEvents() {
		row := history.MatchEvent{Type: int32(event.GetEventType()), Time: event.GetTime(), Bounty: event.GetBounty()}
		if victim := event.GetVictimSteamId(); roster[victim] {
			row.Victim = lobbyKey(victim)
		}
		for _, killer := range event.GetKillerSteamId() {
			if roster[killer] {
				row.Killers = append(row.Killers, lobbyKey(killer))
			}
		}
		r.Events = append(r.Events, row)
	}
	return r
}

func (g *GC) History() *history.Store { return g.history }

// matchKind is whether a match is a war battle, a ranked match or a casual
// one. War battles run in a casual queue, so the war decides first.
func (g *GC) matchKind(l *Lobby) string {
	if l.War != nil {
		return history.KindWar
	}
	if grp, ok := g.cfg.Group(l.Group); ok {
		if grp.EffectiveMode() == config.ModeRanked {
			return history.KindRanked
		}
		return history.KindCasual
	}
	if l.Group == ladderGroup {
		return history.KindRanked
	}
	return history.KindCasual
}

// archiveMatch runs on the loop. Former participants and a received result
// survive roster removals, backfill and the destruction of the live lobby.
func (g *GC) archiveMatch(l *Lobby, result *history.Result, reason string) {
	id := strconv.FormatUint(l.MatchID, 10)
	m, _ := g.history.Get(id)
	m.MatchID, m.Group, m.Map, m.State, m.FormedAt = id, l.Group, l.Map, string(l.State), l.FormedAt
	m.Kind = g.matchKind(l)
	if l.War != nil {
		m.Campaign, m.UniformSwap, m.Sector = l.War.Campaign, l.War.UniformSwap, l.War.NodeName
		if m.Sector == "" {
			m.Sector = g.nodeName(l.War.NodeID)
		}
	}
	if grp, ok := g.cfg.Group(l.Group); ok {
		m.GroupName = grp.Name
		if grp.Region != "" {
			m.Region = grp.Region
		}
	}
	if srv := g.servers[l.ServerID]; srv != nil && srv.Region != "" {
		m.Region = srv.Region
	}
	if !l.StartedAt.IsZero() {
		at := l.StartedAt
		m.StartedAt = &at
	}
	if result != nil {
		m.Result = result
	}
	if reason != "" {
		at := g.now()
		m.State, m.Reason, m.EndedAt = "ended", reason, &at
	}
	for i := range m.Members {
		m.Members[i].Left = true
		id, _ := strconv.ParseUint(m.Members[i].SteamID, 10, 64)
		m.Members[i].VoteKicked = l.kicked[id]
	}
	for _, member := range l.Members {
		sid := strconv.FormatUint(member.SteamID, 10)
		team := "red"
		if member.Team == mm.Blu {
			team = "blu"
		}
		entry := history.Member{SteamID: sid, Name: g.players.Get(member.SteamID).Name, Team: team}
		found := false
		for i := range m.Members {
			if m.Members[i].SteamID == sid {
				m.Members[i] = entry
				found = true
				break
			}
		}
		if !found {
			m.Members = append(m.Members, entry)
		}
	}
	if err := g.history.Put(m); err != nil {
		g.log.Error("could not archive match", "match", id, "err", err)
	}
}
