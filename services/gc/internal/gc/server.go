package gc

import (
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/players"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/sink"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// Server is a dedicated server connected to the GC.
type Server struct {
	SteamID   uint64
	Session   *session.Session
	Name      string
	Community string
	Region    string
	Connect   string
	LobbyID   uint64
	// Ready is the server's own word that it is empty and in matchmaking
	// mode. Only a ready server with no lobby is handed a new match.
	Ready         bool
	LastStatus    time.Time
	Map           string
	LateJoinSlots int
	// Protected: the server runs an Easy Anti-Cheat protected session, by
	// its last heartbeat.
	Protected bool
	// RestUntil keeps a server that just finished a match out of the pool for
	// a moment. Its game is still tearing the old match down, and a new lobby
	// arriving in that window is one the stock server code refuses by
	// quitting.
	RestUntil time.Time
}

func (s *Server) idle(now time.Time) bool {
	return s.Session != nil && !s.Session.Closed() && s.Ready && s.Connect != "" && s.LobbyID == 0 &&
		!now.Before(s.RestUntil)
}

// serverRest is how long a server sits out after a match.
const serverRest = 30 * time.Second

// ServerConnected takes a server into the pool, or back to the match it was
// running before it or the GC restarted. It runs on the loop.
func (g *GC) ServerConnected(s *session.Session) uint64 {
	srv := g.servers[s.SteamID]
	if srv == nil {
		srv = &Server{SteamID: s.SteamID}
		g.servers[s.SteamID] = srv
	}
	srv.Session = s
	srv.Name = s.ServerName
	srv.Community = s.CommunityID
	srv.Region = s.Region
	srv.Connect = s.Connect
	srv.Ready = false // Wait for a status from this session.
	srv.LastStatus = time.Time{}
	srv.LateJoinSlots = 0

	// Back to its match, if it had one.
	if l := g.lobbies[srv.LobbyID]; l != nil && l.ServerID == srv.SteamID {
		l.serverLostAt = time.Time{}
		g.sendConditions(srv, l)
		g.so.Refresh(srv.SteamID)
		g.log.Info("server reconnected to its match", "server", srv.SteamID, "lobby", l.ID)
		return l.ID
	}
	srv.LobbyID = 0

	g.log.Info("server connected", "server", srv.SteamID, "name", srv.Name, "community", srv.Community,
		"connect", srv.Connect)
	return 0
}

func (g *GC) serverGone(s *session.Session) {
	srv := g.servers[s.SteamID]
	if srv == nil || srv.Session != s {
		return
	}
	srv.Session = nil
	srv.Ready = false
	srv.LateJoinSlots = 0
	if srv.LobbyID == 0 {
		delete(g.servers, s.SteamID)
	}
	g.log.Info("server disconnected", "server", s.SteamID, "lobby", srv.LobbyID)
}

func (g *GC) serverLobby(s *session.Session, lobbyID uint64) (*Server, *Lobby) {
	srv := g.servers[s.SteamID]
	if srv == nil || srv.Session != s {
		return nil, nil
	}
	l := g.lobbies[srv.LobbyID]
	if l == nil || (lobbyID != 0 && lobbyID != l.ID) || l.ServerID != srv.SteamID {
		return srv, nil
	}
	return srv, l
}

func init() {
	onServer(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus, (*GC).handleServerStatus)
	onServer(gcpb.ETFGCMsg_k_EMsgGC_Match_Result, (*GC).handleMatchResult)
	onServer(gcpb.ETFGCMsg_k_EMsgGCGameServerKickingLobby, (*GC).handleKickingLobby)
	onServer(gcpb.ETFGCMsg_k_EMsgGCPlayerLeftMatch, (*GC).handlePlayerLeft)
	onServer(gcpb.ETFGCMsg_k_EMsgGCVoteKickPlayerRequest, (*GC).handleVoteKickRequest)
	onServer(gcpb.ETFGCMsg_k_EMsgGC_ProcessMatchVoteKick, (*GC).handleVoteKick)
	onServer(gcpb.ETFGCMsg_k_EMsgGC_ChangeMatchPlayerTeamsRequest, (*GC).handleChangeTeams)
	onServer(gcpb.ETFGCMsg_k_EMsgGC_NewMatchForLobbyRequest, (*GC).handleNewMatch)
	onServer(gcpb.ETFGCMsg_k_EMsgGCMvMVictory, func(g *GC, s *session.Session, m *wire.Msg) {
		g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCMvMVictoryReply), &gcpb.CMsgMvMMannUpVictoryReply{})
	})
}

// handleServerStatus is the server's heartbeat: what map it is on, who is
// connected, who it is holding a seat for.
func (g *GC) handleServerStatus(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGameServerMatchmakingStatus
	if !g.parse(m, &msg) {
		return
	}
	srv := g.servers[s.SteamID]
	if srv == nil || srv.Session != s {
		return
	}
	firstHeartbeat := srv.LastStatus.IsZero()
	srv.LastStatus = g.now()
	srv.Map = msg.GetMap()
	if protected := msg.GetAnticheatProtected(); protected != srv.Protected || firstHeartbeat {
		srv.Protected = protected
		g.log.Info("server anti-cheat", "server", srv.SteamID, "protected", protected)
	}
	// Empty, in matchmaking mode, and with no match of its own any more.
	wasReady := srv.Ready
	state := msg.GetMatchmakingState()
	srv.Ready = state == gcpb.ServerMatchmakingState_ServerMatchmakingState_EMPTY &&
		msg.GetMatchGroup() < 0 && srv.Connect != ""
	if firstHeartbeat {
		g.log.Info("first server heartbeat", "server", srv.SteamID, "state", state.String(),
			"match_group", msg.GetMatchGroup(), "map", srv.Map, "connect", srv.Connect, "ready", srv.Ready)
	} else if wasReady != srv.Ready {
		g.log.Info("server readiness changed", "server", srv.SteamID, "ready", srv.Ready,
			"state", state.String(), "map", srv.Map, "region", srv.Region, "connect", srv.Connect,
			"server_version", msg.GetServerVersion())
	}

	l := g.lobbies[srv.LobbyID]
	if l == nil || l.ServerID != srv.SteamID {
		return
	}

	requesting := state == gcpb.ServerMatchmakingState_ServerMatchmakingState_ACTIVE_MATCH_REQUESTING_LATE_JOIN
	active := requesting || state == gcpb.ServerMatchmakingState_ServerMatchmakingState_ACTIVE_MATCH
	if !active || (msg.MatchGroup != nil && msg.GetMatchGroup() != l.Group) || !sameMap(l.Map, msg.GetMap()) {
		srv.LateJoinSlots = 0
		return
	}
	readyForLobby := msg.LobbyMmVersion != nil && msg.GetLobbyMmVersion() == l.MMVersion
	if l.State == lobbySetup && l.PreviousMatchID != 0 &&
		(!readyForLobby || msg.GetGameState() == gcpb.TF_GC_GameState_TF_GC_GAMESTATE_DISCONNECT) {
		return
	}
	// A heartbeat for an older roster cannot replenish seats we just assigned.
	if !requesting {
		srv.LateJoinSlots = 0
	} else if msg.LobbyMmVersion != nil && msg.GetLobbyMmVersion() == l.MMVersion {
		srv.LateJoinSlots = int(msg.GetSlotsFree())
	}

	changed := srv.Connect != "" && srv.Connect != l.Connect
	if changed {
		l.Connect = srv.Connect
	}
	for _, sp := range msg.GetPlayers() {
		mem := l.member(sp.GetSteamId())
		if mem == nil {
			continue
		}
		next := mem.Connect
		switch sp.GetConnectState() {
		case gcpb.CMsgGameServerMatchmakingStatus_RESERVED:
			// RESERVED for somebody who was connected means the server is
			// holding their seat after they dropped.
			if mem.Connect == gcpb.CTFLobbyPlayerProto_CONNECTED || mem.Connect == gcpb.CTFLobbyPlayerProto_DISCONNECTED {
				next = gcpb.CTFLobbyPlayerProto_DISCONNECTED
			} else {
				next = gcpb.CTFLobbyPlayerProto_RESERVED
			}
		case gcpb.CMsgGameServerMatchmakingStatus_CONNECTED:
			next = gcpb.CTFLobbyPlayerProto_CONNECTED
			l.everConnected = true
		}
		if next != mem.Connect {
			mem.Connect = next
			changed = true
		}
	}

	legacySetup := msg.LobbyMmVersion == nil && l.PreviousMatchID == 0
	if l.State == lobbySetup && (readyForLobby || legacySetup) {
		g.goLive(l)
		return
	}
	if changed {
		g.lobbyChanged(l)
	}
}

func (g *GC) handleMatchResult(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGC_Match_Result
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGC_Match_ResultResponse), &gcpb.CMsgGC_Match_ResultResponse{})
	if !g.parse(m, &msg) {
		return
	}
	_, l := g.serverLobby(s, 0)
	if l == nil || l.MatchID != msg.GetMatchId() || l.resultSeen ||
		(msg.MatchGroup != nil && msg.GetMatchGroup() != l.Group) {
		return
	}
	l.resultSeen = true
	result := g.newMatchResult(l, &msg)
	winner := result.WinningTeam
	var ps []players.Participant
	results := map[uint64]players.Result{}
	gameTeamOf := map[uint64]uint32{}
	ratingBefore := map[uint64]int{}
	xpBefore := map[uint64]int{}
	for _, rp := range result.Players {
		id, _ := strconv.ParseUint(rp.SteamID, 10, 64)
		if l.State == lobbyRun && rp.Flags&matchPlayerAbandoned != 0 {
			g.players.Abandoned(id, "", l.MatchID)
		}
		if rp.Excluded {
			continue
		}
		gameTeamOf[id] = rp.Team
		res := players.Draw
		if winner == 2 || winner == 3 {
			res = players.Loss
			if rp.Team == winner {
				res = players.Win
			}
		}
		ps = append(ps, players.Participant{SteamID: id, Team: int(rp.Team - 2), Result: res})
		results[id] = res
		before := g.players.Get(id)
		ratingBefore[id], xpBefore[id] = before.Rating, before.XP()
	}
	ratings := g.players.RecordMatch(l.MatchID, ps)
	for i := range result.Players {
		rp := &result.Players[i]
		id, _ := strconv.ParseUint(rp.SteamID, 10, 64)
		if rec, ok := ratings[id]; ok {
			rp.Rating, rp.RatingChange = rec.Rating, rec.Rating-ratingBefore[id]
		}
	}
	g.creditQuests(l, &msg, results)
	g.creditClans(l, results)

	if result.Completed() && len(results) > 0 && l.War != nil && g.war != nil && l.War.Campaign == g.war.Campaign() && (winner == 2 || winner == 3) {
		// Which side the attacker's players ended up on is the server's word,
		// not ours: a competitive match swaps sides at halftime.
		attackerWon := false
		votes := 0
		for _, mem := range l.Members {
			if int32(mem.Team) != l.War.AttackerTeam {
				continue
			}
			if t, ok := gameTeamOf[mem.SteamID]; ok {
				if t == winner {
					votes++
				} else {
					votes--
				}
			}
		}
		attackerWon = votes > 0
		ownerBefore := g.war.Owner(l.War.NodeID)
		winnerBefore := g.war.Snapshot().Winner
		if err := g.war.ApplyResult(l.War.FrontID, strconv.FormatUint(l.MatchID, 10), attackerWon); err != nil {
			g.log.Warn("war: could not apply result", "match", l.MatchID, "err", err)
		} else {
			g.warFeed(l, attackerWon, ownerBefore)
			if winnerBefore == "" && g.war.Snapshot().Winner != "" {
				g.stopWarQueues()
			}
		}
	}
	// A battle on the war's order pays everyone in it, won or lost.
	if l.War != nil && l.War.Order {
		for id := range results {
			g.players.Bonus(id, "order:"+strconv.FormatUint(l.MatchID, 10), orderBonusXP)
		}
	}

	for i := range result.Players {
		p := &result.Players[i]
		id, _ := strconv.ParseUint(p.SteamID, 10, 64)
		if before, ok := xpBefore[id]; ok {
			p.XPChange = g.players.Get(id).XP() - before
		}
	}
	g.archiveMatch(l, result, "")
	for _, p := range result.Players {
		id, _ := strconv.ParseUint(p.SteamID, 10, 64)
		g.publishRatings(id)
		if g.isOnline(id) {
			g.publishMatchStats(id, l.Group)
		}
	}
	g.log.Info("match result", "lobby", l.ID, "match", l.MatchID, "winner", winner,
		"red", msg.GetRedScore(), "blu", msg.GetBlueScore(), "players", len(ps))
	g.hooks.Emit(sink.Event{Type: "match_result", At: g.now(), Data: g.matchSummary(l, map[string]any{
		"winning_team": winner,
		"status":       msg.GetStatus().String(),
		"red_score":    msg.GetRedScore(),
		"blue_score":   msg.GetBlueScore(),
		"duration":     msg.GetDuration(),
		"players":      resultPlayers(msg.GetPlayers()),
	})})
}

func resultPlayers(rps []*gcpb.CMsgGC_Match_Result_Player) []map[string]any {
	out := make([]map[string]any, 0, len(rps))
	for _, p := range rps {
		out = append(out, map[string]any{
			"steam_id": strconv.FormatUint(p.GetSteamId(), 10),
			"team":     p.GetTeam(),
			"score":    p.GetScore(),
			"kills":    p.GetKills(),
			"deaths":   p.GetDeaths(),
			"damage":   p.GetDamage(),
			"healing":  p.GetHealing(),
			"support":  p.GetSupport(),
			"leave":    p.GetLeaveReason().String(),
		})
	}
	return out
}

// handleKickingLobby is the server finished with its match.
func (g *GC) handleKickingLobby(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGameServerKickingLobby
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCGameServerKickingLobbyResponse), &gcpb.CMsgGameServerKickingLobbyResponse{})
	if !g.parse(m, &msg) {
		return
	}
	if _, l := g.serverLobby(s, msg.GetLobbyId()); l != nil && l.MatchID == msg.GetMatchId() {
		g.endLobby(l, "the server ended the match", l.State == lobbySetup)
	}
}

func (g *GC) handlePlayerLeft(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPlayerLeftMatch
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCPlayerLeftMatchResponse), &gcpb.CMsgPlayerLeftMatchResponse{})
	if !g.parse(m, &msg) {
		return
	}
	_, l := g.serverLobby(s, msg.GetLobbyId())
	if l == nil || l.MatchID != msg.GetMatchId() {
		return
	}
	id := msg.GetSteamId()
	// GC_REMOVED acknowledges an exit we already processed. The player may
	// have been assigned a new reservation in the meantime.
	if l.member(id) == nil || msg.GetLeaveReason() == gcpb.TFMatchLeaveReason_TFMatchLeaveReason_GC_REMOVED {
		return
	}
	if msg.GetWasAbandon() && l.State == lobbyRun {
		g.players.Abandoned(id, "", l.MatchID)
		g.publishRatings(id)
	}
	// Let the server send its final result before it tears down the lobby.
	g.dropMember(l, id)
	g.log.Info("player left match", "lobby", l.ID, "steamid", id,
		"reason", msg.GetLeaveReason().String(), "abandon", msg.GetWasAbandon())
}

func (g *GC) handleVoteKickRequest(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGC_TFVoteKickPlayerRequest
	reply := &gcpb.CMsgGC_VoteKickPlayerRequestResponse{Allowed: proto.Bool(false)}
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCVoteKickPlayerRequestResponse), reply)
	_, l := g.serverLobby(s, 0)
	if !g.parse(m, &msg) || l == nil || l.State != lobbyRun || l.resultSeen || l.MatchID != msg.GetMatchId() {
		return
	}
	voter, target := l.member(msg.GetVoterId()), l.member(msg.GetTargetId())
	*reply.Allowed = voter != nil && target != nil && voter != target &&
		msg.GetReason() >= gcpb.TFVoteKickReason_TFVoteKickReason_Other &&
		msg.GetReason() <= gcpb.TFVoteKickReason_TFVoteKickReason_Scamming
}

func (g *GC) handleVoteKick(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgProcessMatchVoteKick
	reply := &gcpb.CMsgProcessMatchVoteKickResponse{Rip: proto.Bool(false)}
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGC_ProcessMatchVoteKickResponse), reply)
	_, l := g.serverLobby(s, 0)
	if !g.parse(m, &msg) || l == nil || l.State != lobbyRun || l.resultSeen || l.MatchID != msg.GetMatchId() ||
		!msg.GetDefaultPass() || msg.GetInitiatorSteamId() == msg.GetTargetSteamId() {
		return
	}
	id := msg.GetTargetSteamId()
	if l.kicked[id] {
		*reply.Rip = true
		return
	}
	if l.member(id) == nil {
		// A player can leave while the server's vote is still in progress.
		archived, _ := g.history.Get(lobbyKey(l.MatchID))
		found := false
		for _, p := range archived.Members {
			found = found || p.SteamID == lobbyKey(id)
		}
		if !found {
			return
		}
	}
	if l.kicked == nil {
		l.kicked = make(map[uint64]bool)
	}
	l.kicked[id] = true
	if l.member(id) != nil {
		g.dropMember(l, id)
	} else {
		g.archiveMatch(l, nil, "")
	}
	*reply.Rip = true
}

// handleChangeTeams is the server rebalancing. The lobby has to agree, or the
// next lobby update would put everybody back.
func (g *GC) handleChangeTeams(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGCChangeMatchPlayerTeamsRequest
	if !g.parse(m, &msg) {
		g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGC_ChangeMatchPlayerTeamsResponse), &gcpb.CMsgGCChangeMatchPlayerTeamsResponse{})
		return
	}
	ok := false
	if _, l := g.serverLobby(s, msg.GetLobbyId()); l != nil && l.MatchID == msg.GetMatchId() && l.War == nil {
		teams := make(map[*Member]mm.Team, len(msg.GetMember()))
		for _, ch := range msg.GetMember() {
			mem := l.member(ch.GetMemberId())
			team := mm.Team(ch.GetNewTeam())
			if mem == nil || (team != mm.Red && team != mm.Blu) {
				g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGC_ChangeMatchPlayerTeamsResponse), &gcpb.CMsgGCChangeMatchPlayerTeamsResponse{Success: proto.Bool(false)})
				return
			}
			teams[mem] = team
		}
		for mem, team := range teams {
			mem.Team = team
		}
		g.lobbyChanged(l)
		ok = true
	}
	g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGC_ChangeMatchPlayerTeamsResponse), &gcpb.CMsgGCChangeMatchPlayerTeamsResponse{
		Success: proto.Bool(ok),
	})
}

// --- the pool -----------------------------------------------------------------

// The GC plays only on servers connected to it with a server key: the pool.
// How big the war is and how many battles run at once follow from it (and
// from how many people are online).

// poolSize is how many servers are connected.
func (g *GC) poolSize() int {
	n := 0
	for _, srv := range g.servers {
		if srv.Session != nil && !srv.Session.Closed() {
			n++
		}
	}
	return n
}

// freeServers is how many servers could take a new match of this group now.
func (g *GC) freeServers(region string) int {
	n := 0
	for _, srv := range g.servers {
		if g.serverEligible(srv, g.now()) && (region == "" || srv.Region == region) {
			n++
		}
	}
	return n
}
