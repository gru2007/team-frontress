package gc

import (
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/greyline-frontress/coordinator/internal/hostelect"
	"github.com/greyline-frontress/coordinator/internal/war"
	"github.com/greyline-frontress/coordinator/internal/wire/pb"
)

// resultGrace is how long the coordinator waits for client attestations after
// the host reports a result before deciding with whatever arrived. Without it,
// one player closing the game the instant a match ends would stall the war.
const resultGrace = 20 * time.Second

// ---------------------------------------------------------------------------
// Match formation
// ---------------------------------------------------------------------------

// tryFormMatches walks every front with queued players and forms whatever
// matches the current population supports.
//
// The size policy follows the design constraint that the game must stay
// playable at a very small online count: take the largest configured team size
// the queue can fill, but only settle for a size below the maximum once the
// longest-waiting player has waited out form_wait. That way eight players
// online become one 4v4 rather than two 2v2s, while four players are not left
// staring at a queue forever.
func (s *Server) tryFormMatches() {
	byFront := map[string][]*Session{}
	for _, sess := range s.sessions {
		if sess.inQueue && sess.frontID != "" {
			byFront[sess.frontID] = append(byFront[sess.frontID], sess)
		}
	}
	for frontID, queued := range byFront {
		for {
			if !s.formOne(frontID, &queued) {
				break
			}
		}
	}
}

func (s *Server) formOne(frontID string, queued *[]*Session) bool {
	front, ok := s.world.Front(frontID)
	if !ok || !front.Active {
		return false
	}
	if s.liveOnFront(frontID) >= s.cfg.Match.MaxConcurrentPerFront {
		return false
	}

	// Longest wait first: nobody should be skipped over by a later arrival.
	sort.SliceStable(*queued, func(i, j int) bool {
		return (*queued)[i].queuedAt.Before((*queued)[j].queuedAt)
	})
	pool := *queued
	if len(pool) < 2*s.cfg.Match.MinTeamSize {
		return false
	}

	sizes := s.cfg.SortedTeamSizes()
	maxSize := sizes[len(sizes)-1]

	teamSize := 0
	for _, sz := range sizes {
		if 2*sz <= len(pool) {
			teamSize = sz
		}
	}
	if teamSize == 0 {
		return false
	}
	if teamSize < maxSize {
		waited := time.Since(pool[0].queuedAt)
		if waited < s.cfg.Match.FormWait.D() {
			return false // hold out for a bigger battle a little longer
		}
	}

	total := 2 * teamSize
	mapEntry, ok := s.world.SelectMap(frontID, total)
	if !ok {
		s.log.Warn("no map fits queued population",
			"front", frontID, "players", total)
		return false
	}

	picked := pool[:total]
	red, blu := assignSides(picked, teamSize)

	id := newMatchID()
	m := newMatch(id, frontID, mapEntry, total, s.policy, s.keys.MatchSecret(id))
	for _, sess := range red {
		m.add(sess.steamID, war.SideRed)
	}
	for _, sess := range blu {
		m.add(sess.steamID, war.SideBlu)
	}

	s.matches[id] = m
	for _, sess := range picked {
		sess.inQueue = false
		s.byPlayer[sess.steamID] = id
	}
	*queued = pool[total:]
	s.stats.MatchesFormed++

	s.log.Info("match formed", "match", id, "front", frontID,
		"map", mapEntry.Map, "mode", mapEntry.Mode, "size", itoa(teamSize)+"v"+itoa(teamSize))

	if !s.assignHost(m, false) {
		s.dissolve(m, "no player in this roster can host the battle")
		return false
	}
	return true
}

// assignSides splits the picked players into two teams, honouring stated
// preferences first and filling the rest to balance. Party grouping is not
// modelled yet; when it is, it belongs here.
func assignSides(picked []*Session, teamSize int) (red, blu []*Session) {
	var undecided []*Session
	for _, sess := range picked {
		switch pb.ESide(sess.queueSide) {
		case pb.ESide_SIDE_RED:
			if len(red) < teamSize {
				red = append(red, sess)
			} else {
				undecided = append(undecided, sess)
			}
		case pb.ESide_SIDE_BLU:
			if len(blu) < teamSize {
				blu = append(blu, sess)
			} else {
				undecided = append(undecided, sess)
			}
		default:
			undecided = append(undecided, sess)
		}
	}
	for _, sess := range undecided {
		if len(red) <= len(blu) && len(red) < teamSize {
			red = append(red, sess)
		} else {
			blu = append(blu, sess)
		}
	}
	return red, blu
}

func (s *Server) liveOnFront(frontID string) int {
	n := 0
	for _, m := range s.matches {
		if m.FrontID == frontID && !m.over() {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Host assignment and migration
// ---------------------------------------------------------------------------

// assignHost elects a host and tells it to boot the map. Returns false when no
// roster member is eligible.
func (s *Server) assignHost(m *Match, isMigration bool) bool {
	winner, ranked, ok := s.elect.Elect(m.candidates(s.sessions, s.store, isMigration))
	m.Ranking = ranked
	if !ok {
		s.log.Warn("host election found no eligible candidate",
			"match", m.ID, "ranking", hostelect.Describe(ranked))
		return false
	}

	prev := m.HostID
	m.HostID = winner.SteamID
	for _, slot := range m.slots() {
		slot.IsHost = slot.SteamID == winner.SteamID
		if isMigration {
			slot.Connected = false
		}
	}
	// The game server is gone, but the lobby is not: Steam keeps it alive and
	// hands ownership to a surviving member. Clearing LobbyID here would strand
	// every player in a room the coordinator no longer knows about.
	m.GameServerSteamID = 0
	m.setState(pb.EMatchState_MATCH_HOST_ASSIGNED)

	if winner.Degraded {
		// Worth its own line: a run of these means the queue is pairing people
		// who have no good host between them, which is a formation problem
		// rather than a hardware one.
		s.log.Warn("host elected below the preferred floors",
			"match", m.ID, "host", winner.SteamID, "shortfall", winner.Disqualify,
			"worst_rtt_ms", winner.WorstRTT)
	}
	s.log.Info("host elected", "match", m.ID, "host", winner.SteamID,
		"score", winner.Total, "worst_rtt_ms", winner.WorstRTT, "degraded", winner.Degraded,
		"migration", isMigration, "previous_host", prev,
		"ranking", hostelect.Describe(ranked))

	assign := &pb.CMsgAssignHost{
		MatchId:       proto.String(m.ID),
		FrontId:       proto.String(m.FrontID),
		MapName:       proto.String(m.Map),
		GameMode:      proto.String(m.Mode),
		MaxPlayers:    proto.Uint32(uint32(m.MaxPlrs)),
		Policy:        m.Policy.ToProto(),
		BootDeadlineS: proto.Uint32(uint32(s.cfg.Timing.HostBootDeadline.D().Seconds())),
		MatchSecret:   m.Secret,
		IsMigration:   proto.Bool(isMigration),
		LobbyId:       proto.Uint64(m.LobbyID),
	}
	for _, slot := range m.slots() {
		assign.Roster = append(assign.Roster, &pb.CMsgRosterEntry{
			SteamId:   proto.Uint64(slot.SteamID),
			Side:      pbSide(slot.Side).Enum(),
			Connected: proto.Bool(slot.Connected),
			IsHost:    proto.Bool(slot.IsHost),
		})
	}
	if isMigration && m.Snapshot != nil {
		assign.Restore = m.Snapshot
	}
	if !s.send(m.HostID, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_AssignHost{AssignHost: assign}}) {
		// The elected host's link died between ranking and sending.
		s.hostFailed(m, m.HostID, "host link disappeared before assignment was delivered")
		return m.HostID != 0 && !m.over()
	}
	return true
}

// hostFailed records that a host did not deliver, then tries the next best
// candidate. The failed host is excluded for the rest of this match.
func (s *Server) hostFailed(m *Match, hostID uint64, reason string) {
	if m.over() {
		return
	}
	s.log.Warn("host failed", "match", m.ID, "host", hostID, "reason", reason)
	m.FailedHosts[hostID] = true
	s.store.Update(hostID, func(p *Player) {
		p.History.HostedFailed++
		p.History.LastPenaltyAt = time.Now().Unix()
	})

	if m.live() {
		s.beginMigration(m, reason)
		return
	}
	if !s.assignHost(m, false) {
		s.dissolve(m, "no player in this roster can host the battle")
	}
}

// beginMigration stands the match back up on a new host, restoring the last
// snapshot the old host sent. Clients hold rather than being sent back to the
// queue, which is the whole point: a host dropping should cost a loading
// screen, not the battle.
func (s *Server) beginMigration(m *Match, reason string) {
	if m.over() || m.in(pb.EMatchState_MATCH_MIGRATING) {
		return
	}
	if !m.CanMigrate {
		// Only the round scoreline survives a change of host. In a mode whose
		// state is something else — MvM's wave and credits, a stopwatch time —
		// resuming would hand players a game that silently lost most of itself.
		s.abortMatch(m, "this game mode cannot be resumed on another host")
		return
	}
	if m.Migrations >= s.cfg.Match.MaxMigrations {
		s.abortMatch(m, "host migration limit reached")
		return
	}
	m.Migrations++
	s.stats.Migrations++
	oldHost := m.HostID
	m.FailedHosts[oldHost] = true
	m.setState(pb.EMatchState_MATCH_MIGRATING)

	s.log.Warn("migrating host", "match", m.ID, "old_host", oldHost,
		"attempt", m.Migrations, "reason", reason,
		"have_snapshot", m.Snapshot != nil)

	// Losing the host mid-match is the abandon case that matters most: the
	// other nine players lose their battle because of it.
	if slot, ok := m.Roster[oldHost]; ok && !slot.Present {
		s.punishAbandon(m, oldHost)
	}

	if !s.assignHost(m, true) {
		s.abortMatch(m, "no remaining player can take over as host")
		return
	}

	// Hold for exactly as long as the coordinator gives the new host to come
	// up. Holding longer leaves players staring at a battle that has already
	// been abandoned; holding less sends them away just before it recovers.
	hold := uint32(s.cfg.Timing.HostBootDeadline.D().Seconds())
	for _, slot := range m.slots() {
		if slot.SteamID == m.HostID {
			continue
		}
		s.send(slot.SteamID, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_MigrateHost{MigrateHost: &pb.CMsgMigrateHost{
			MatchId:        proto.String(m.ID),
			NewHostSteamId: proto.Uint64(m.HostID),
			HoldSeconds:    proto.Uint32(hold),
			Message:        proto.String("host lost — restoring the battle on a new host"),
		}}})
	}
}

// ---------------------------------------------------------------------------
// Leaving, aborting, dissolving
// ---------------------------------------------------------------------------

func (s *Server) removeFromMatch(m *Match, steamID uint64, reason pb.ELeaveReason) {
	if _, ok := m.Roster[steamID]; !ok {
		return
	}
	delete(m.Roster, steamID)
	for i, id := range m.Order {
		if id == steamID {
			m.Order = append(m.Order[:i], m.Order[i+1:]...)
			break
		}
	}
	if cur, ok := s.byPlayer[steamID]; ok && cur == m.ID {
		delete(s.byPlayer, steamID)
	}
	s.send(m.HostID, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_PlayerLeft{PlayerLeft: &pb.CMsgPlayerLeft{
		MatchId: proto.String(m.ID),
		SteamId: proto.Uint64(steamID),
		Reason:  reason.Enum(),
	}}})

	if len(m.Order) < 2*s.cfg.Match.MinTeamSize && m.live() {
		s.abortMatch(m, "too few players remain to continue the battle")
	}
}

func (s *Server) punishAbandon(m *Match, steamID uint64) {
	until := s.store.Ban(steamID, s.cfg.Match.AbandonBanDuration.D(), "abandoned a live battle")
	s.store.Update(steamID, func(p *Player) {
		p.Abandons++
		p.History.Abandons++
		p.History.LastPenaltyAt = time.Now().Unix()
	})
	s.log.Info("abandon penalty applied", "match", m.ID, "steam_id", steamID, "until", until)
	s.send(steamID, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_SecurityVerdict{SecurityVerdict: &pb.CMsgSecurityVerdict{
		MatchId:    proto.String(m.ID),
		SteamId:    proto.Uint64(steamID),
		Action:     proto.String("flag"),
		Reason:     proto.String("abandoned a live battle"),
		BanSeconds: proto.Uint32(uint32(time.Until(until).Seconds())),
	}}})
}

// abortMatch ends a match without it counting towards the war.
func (s *Server) abortMatch(m *Match, reason string) {
	if m.over() {
		return
	}
	m.setState(pb.EMatchState_MATCH_ABORTED)
	m.Resolved = true
	s.stats.Aborted++
	s.log.Warn("match aborted", "match", m.ID, "reason", reason)

	s.broadcastMatch(m, func(slot *RosterSlot) *pb.CMsgEnvelope {
		return &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_MatchOver{MatchOver: &pb.CMsgMatchOver{
			MatchId:           proto.String(m.ID),
			Outcome:           pb.EMatchOutcome_OUTCOME_UNKNOWN.Enum(),
			Disputed:          proto.Bool(false),
			CountedTowardsWar: proto.Bool(false),
			Message:           proto.String("battle aborted: " + reason),
		}}}
	})
	s.releaseRoster(m)
}

// dissolve tears down a match that never started and puts its players back in
// the queue, so a failed formation is invisible to them beyond a short wait.
func (s *Server) dissolve(m *Match, reason string) {
	m.setState(pb.EMatchState_MATCH_ABORTED)
	m.Resolved = true
	s.log.Warn("match dissolved before start", "match", m.ID, "reason", reason)
	for _, slot := range m.slots() {
		delete(s.byPlayer, slot.SteamID)
		if sess, ok := s.sessions[slot.SteamID]; ok {
			sess.inQueue = true
			sess.queuedAt = time.Now()
			sess.Send(queueStatusMsg(&pb.CMsgQueueStatus{
				Queued:  proto.Bool(true),
				FrontId: proto.String(m.FrontID),
				Message: proto.String(reason + " — searching again"),
			}))
		}
	}
}

func (s *Server) releaseRoster(m *Match) {
	for _, slot := range m.slots() {
		if cur, ok := s.byPlayer[slot.SteamID]; ok && cur == m.ID {
			delete(s.byPlayer, slot.SteamID)
		}
	}
}

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

// tryResolve finalises a match once the host has reported and enough clients
// have corroborated, or the corroboration window has passed.
func (s *Server) tryResolve(m *Match) {
	if m.Resolved || m.HostResult == nil {
		return
	}
	witnesses := m.nonHostPresent()
	needed := int(math.Ceil(s.cfg.Match.ResultQuorum * float64(witnesses)))
	if needed < 1 && witnesses > 0 {
		needed = 1
	}
	if len(m.Attestations) < needed && time.Since(m.ResultAt) < resultGrace {
		return
	}
	s.resolve(m, witnesses, needed)
}

func (s *Server) resolve(m *Match, witnesses, needed int) {
	m.Resolved = true
	m.setState(pb.EMatchState_MATCH_FINISHED)

	hostOutcome := warOutcome(m.HostResult.GetOutcome())
	agree, disagree := 0, 0
	for _, a := range m.Attestations {
		if a.outcome == hostOutcome && a.red == m.HostResult.GetRedScore() && a.blu == m.HostResult.GetBluScore() {
			agree++
		} else {
			disagree++
		}
	}

	// A match counts when nobody contradicts the host and enough witnesses
	// back it up. With no witnesses left there is nothing to corroborate, so
	// the result stands but is recorded as unwitnessed.
	disputed := disagree > 0 || (witnesses > 0 && agree < needed)
	counted := !disputed && hostOutcome != war.OutcomeUnknown && hostOutcome != war.OutcomeAbandoned

	var update *pb.CMsgWarUpdate
	if counted {
		up, err := s.world.ApplyResult(m.FrontID, hostOutcome)
		if err != nil {
			s.log.Error("war layer rejected result", "match", m.ID, "err", err)
			counted = false
		} else {
			s.stats.Counted++
			update = &pb.CMsgWarUpdate{
				FrontId:      proto.String(up.FrontID),
				RedPoints:    proto.Uint32(up.RedPoints),
				BluPoints:    proto.Uint32(up.BluPoints),
				PointsToWin:  proto.Uint32(up.PointsToWin),
				NodeCaptured: proto.Bool(up.NodeCaptured),
				NewOwner:     pbSide(up.NewOwner).Enum(),
				Headline:     proto.String(up.Headline),
			}
		}
	}
	if disputed {
		s.stats.Disputed++
	}

	s.log.Info("match resolved", "match", m.ID,
		"outcome", hostOutcome, "agree", agree, "disagree", disagree,
		"witnesses", witnesses, "needed", needed,
		"disputed", disputed, "counted", counted)

	// Credit the host only for a match that actually produced a result.
	s.store.Update(m.HostID, func(p *Player) { p.History.HostedOK++ })
	for _, slot := range m.slots() {
		s.store.Update(slot.SteamID, func(p *Player) { p.Matches++ })
	}

	msg := "battle recorded"
	switch {
	case disputed:
		msg = "result disputed — this battle does not count towards the war"
	case !counted:
		msg = "battle ended without advancing the front"
	case update != nil:
		msg = update.GetHeadline()
	}
	s.broadcastMatch(m, func(slot *RosterSlot) *pb.CMsgEnvelope {
		return &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_MatchOver{MatchOver: &pb.CMsgMatchOver{
			MatchId:           proto.String(m.ID),
			Outcome:           pbOutcome(hostOutcome).Enum(),
			Disputed:          proto.Bool(disputed),
			CountedTowardsWar: proto.Bool(counted),
			WarUpdate:         update,
			Message:           proto.String(msg),
		}}}
	})
	s.releaseRoster(m)
}

// ---------------------------------------------------------------------------
// Tick
// ---------------------------------------------------------------------------

func (s *Server) onTick() {
	now := time.Now()

	for _, sess := range s.sessions {
		if now.Sub(sess.lastSeen) > s.cfg.Timing.SessionTimeout.D() {
			s.log.Info("session timed out", "steam_id", sess.steamID)
			sess.Close()
		}
	}

	for id, m := range s.matches {
		switch m.State {
		case pb.EMatchState_MATCH_HOST_ASSIGNED:
			if now.Sub(m.StateSince) > s.cfg.Timing.HostBootDeadline.D() {
				s.hostFailed(m, m.HostID, "host did not report ready within the boot deadline")
			}
		case pb.EMatchState_MATCH_LOADING:
			if now.Sub(m.StateSince) > s.cfg.Timing.ClientConnectDeadline.D() {
				s.finishLoading(m)
			}
		case pb.EMatchState_MATCH_MIGRATING:
			if now.Sub(m.StateSince) > s.cfg.Timing.MigrationHold.D() {
				s.hostFailed(m, m.HostID, "replacement host did not come up in time")
			}
		case pb.EMatchState_MATCH_LIVE:
			s.checkLiveMatch(m, now)
		case pb.EMatchState_MATCH_FINISHED, pb.EMatchState_MATCH_ABORTED:
			if now.Sub(m.StateSince) > 60*time.Second {
				delete(s.matches, id)
			}
		}
	}

	s.tryFormMatches()
	s.publishQueueStatus()
}

// finishLoading drops players who never reached the host and starts the match
// if enough of them did.
func (s *Server) finishLoading(m *Match) {
	var missing []uint64
	for _, slot := range m.slots() {
		if !slot.Connected {
			missing = append(missing, slot.SteamID)
		}
	}
	for _, id := range missing {
		s.log.Info("dropping player who never connected", "match", m.ID, "steam_id", id)
		s.removeFromMatch(m, id, pb.ELeaveReason_LEAVE_TIMEOUT)
	}
	if m.over() {
		return
	}
	if len(m.Order) < 2*s.cfg.Match.MinTeamSize {
		s.abortMatch(m, "not enough players reached the host")
		return
	}
	m.setState(pb.EMatchState_MATCH_LIVE)
	s.stats.MatchesLive++
	s.log.Info("match live after loading window", "match", m.ID, "players", len(m.Order))
}

func (s *Server) checkLiveMatch(m *Match, now time.Time) {
	// A host that stops sending integrity reports is no longer proving it runs
	// the policy, so treat sustained silence as a failure of the contract.
	interval := s.cfg.Security.IntegrityReportInterval.D()
	if interval > 0 && !m.LastIntegrity.IsZero() {
		missed := int(now.Sub(m.LastIntegrity) / interval)
		if missed > m.MissedIntegrity {
			m.MissedIntegrity = missed
			if missed > s.cfg.Security.IntegrityGrace {
				s.abortMatch(m, "host stopped reporting match integrity")
				return
			}
		}
	}

	// Free slots held for players whose reconnect grace has run out.
	grace := s.cfg.Timing.ReconnectGrace.D()
	for _, slot := range m.slots() {
		if slot.Present || slot.LeftAt.IsZero() {
			continue
		}
		if now.Sub(slot.LeftAt) > grace {
			s.punishAbandon(m, slot.SteamID)
			s.removeFromMatch(m, slot.SteamID, pb.ELeaveReason_LEAVE_ABANDON)
		}
	}
	if m.over() {
		return
	}
	if m.HostResult != nil {
		s.tryResolve(m)
	}
}

// publishQueueStatus keeps queued clients informed. Without this the DEPLOY
// screen has nothing to show while the coordinator waits for more players.
func (s *Server) publishQueueStatus() {
	for _, sess := range s.sessions {
		if !sess.inQueue {
			continue
		}
		// Once a second is too chatty; the client only needs a periodic nudge.
		if int(time.Since(sess.queuedAt).Seconds())%5 != 0 {
			continue
		}
		sess.Send(queueStatusMsg(s.queueStatusFor(sess)))
	}
}

func (s *Server) queueStatusFor(sess *Session) *pb.CMsgQueueStatus {
	inQueue, ahead := 0, 0
	for _, other := range s.sessions {
		if !other.inQueue || other.frontID != sess.frontID {
			continue
		}
		inQueue++
		if other.queuedAt.Before(sess.queuedAt) {
			ahead++
		}
	}
	needed := 2 * s.cfg.Match.MinTeamSize
	remaining := needed - inQueue
	if remaining < 0 {
		remaining = 0
	}
	msg := "searching for a battle"
	if remaining > 0 {
		msg = "waiting for " + itoa(remaining) + " more mercenaries"
	}
	return &pb.CMsgQueueStatus{
		Queued:         proto.Bool(true),
		FrontId:        proto.String(sess.frontID),
		Side:           pb.ESide(sess.queueSide).Enum(),
		Position:       proto.Uint32(uint32(ahead + 1)),
		PlayersInQueue: proto.Uint32(uint32(inQueue)),
		PlayersNeeded:  proto.Uint32(uint32(remaining)),
		Message:        proto.String(msg),
	}
}
