package gc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/greyline-frontress/coordinator/internal/legacy/hostelect"
	"github.com/greyline-frontress/coordinator/internal/legacy/war"
	"github.com/greyline-frontress/coordinator/internal/security"
	"github.com/greyline-frontress/coordinator/internal/steam"
	"github.com/greyline-frontress/coordinator/internal/wire/pb"
)

// onMessage dispatches one inbound envelope. Everything before the handshake is
// rejected except the hello itself, so an unauthenticated peer can do nothing
// but identify itself.
func (s *Server) onMessage(sess *Session, env *pb.CMsgEnvelope) {
	sess.lastSeen = time.Now()

	if sess.steamID == 0 {
		if h := env.GetHello(); h != nil {
			s.onHello(sess, env, h)
			return
		}
		s.rejectHandshake(sess, pb.EResult_RESULT_AUTH_FAILED, "hello required before any other message")
		return
	}

	switch body := env.Body.(type) {
	case *pb.CMsgEnvelope_Hello:
		s.rejectHandshake(sess, pb.EResult_RESULT_FAIL, "already authenticated")
	case *pb.CMsgEnvelope_Ping:
		sess.Reply(env, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_Pong{
			Pong: &pb.CMsgPong{Nonce: proto.Uint64(body.Ping.GetNonce())},
		}})
	case *pb.CMsgEnvelope_WorldStateRequest:
		sess.Reply(env, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_WorldState{WorldState: s.worldState()}})
	case *pb.CMsgEnvelope_HostCapabilities:
		s.onCapabilities(sess, body.HostCapabilities)
	case *pb.CMsgEnvelope_DeployRequest:
		s.onDeploy(sess, env, body.DeployRequest)
	case *pb.CMsgEnvelope_DeployCancel:
		s.onDeployCancel(sess)
	case *pb.CMsgEnvelope_HostReady:
		s.onHostReady(sess, body.HostReady)
	case *pb.CMsgEnvelope_HostFailed:
		s.onHostFailed(sess, body.HostFailed)
	case *pb.CMsgEnvelope_ClientJoinState:
		s.onClientJoinState(sess, body.ClientJoinState)
	case *pb.CMsgEnvelope_HostHeartbeat:
		s.onHostHeartbeat(sess, body.HostHeartbeat)
	case *pb.CMsgEnvelope_ClientHeartbeat:
		s.onClientHeartbeat(sess, body.ClientHeartbeat)
	case *pb.CMsgEnvelope_PlayerLeft:
		s.onPlayerLeft(sess, body.PlayerLeft)
	case *pb.CMsgEnvelope_IntegrityReport:
		s.onIntegrityReport(sess, body.IntegrityReport)
	case *pb.CMsgEnvelope_HostLost:
		s.onHostLost(sess, body.HostLost)
	case *pb.CMsgEnvelope_MatchResult:
		s.onMatchResult(sess, body.MatchResult)
	case *pb.CMsgEnvelope_ResultAttestation:
		s.onResultAttestation(sess, body.ResultAttestation)
	case *pb.CMsgEnvelope_Goodbye:
		sess.Close()
	default:
		s.log.Debug("unhandled message", "steam_id", sess.steamID, "type", fmt.Sprintf("%T", env.Body))
	}
}

// ---------------------------------------------------------------------------
// Handshake
// ---------------------------------------------------------------------------

func (s *Server) rejectHandshake(sess *Session, res pb.EResult, msg string) {
	sess.SendAndClose(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_Welcome{Welcome: &pb.CMsgWelcome{
		Result:  res.Enum(),
		Message: proto.String(msg),
	}}})
}

func (s *Server) onHello(sess *Session, env *pb.CMsgEnvelope, h *pb.CMsgHello) {
	if h.GetProtocolVersion() != s.cfg.ProtocolVersion {
		s.rejectHandshake(sess, pb.EResult_RESULT_INVALID_PROTOCOL,
			"protocol version mismatch: coordinator speaks v"+itoa(int(s.cfg.ProtocolVersion)))
		return
	}
	if len(s.cfg.AcceptedClientVersions) > 0 && !contains(s.cfg.AcceptedClientVersions, h.GetClientVersion()) {
		s.rejectHandshake(sess, pb.EResult_RESULT_INVALID_PROTOCOL,
			"client build "+h.GetClientVersion()+" is not accepted by this coordinator")
		return
	}

	// Ticket validation can hit the Steam Web API, so it must not run on the
	// core goroutine. The result comes back as an event.
	ticket := append([]byte(nil), h.GetAuthTicket()...)
	claimed := h.GetSteamId()
	auth := s.auth
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ident, err := auth.Authenticate(ctx, claimed, ticket)
		s.post(event{kind: evAuthResult, sess: sess, hello: h, req: env, ident: ident, authErr: err})
	}()
}

func (s *Server) onAuthResult(sess *Session, h *pb.CMsgHello, req *pb.CMsgEnvelope, ident *steam.Identity, err error) {
	if _, stillPending := s.pending[sess]; !stillPending {
		// The link died while Steam was being consulted.
		sess.Close()
		return
	}
	if err != nil {
		s.stats.AuthFailures++
		s.log.Info("auth rejected", "claimed", h.GetSteamId(), "err", err)
		s.rejectHandshake(sess, pb.EResult_RESULT_AUTH_FAILED, err.Error())
		return
	}

	now := time.Now()
	player := s.store.Get(ident.SteamID)
	if player.Banned(now) {
		s.rejectHandshake(sess, pb.EResult_RESULT_BANNED,
			"matchmaking ban active until "+player.BannedUntil.UTC().Format(time.RFC3339)+": "+player.BanReason)
		return
	}

	// One link per account. A second login replaces the first rather than
	// running two sessions that would fight over the same match slot.
	if old, ok := s.sessions[ident.SteamID]; ok && old != sess {
		s.log.Info("replacing existing session", "steam_id", ident.SteamID)
		old.steamID = 0 // stop onClosed from tearing down the new session's state
		old.SendAndClose(&pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_Goodbye{Goodbye: &pb.CMsgGoodbye{
			Reason: proto.String("logged in from another session"),
		}}})
	}

	delete(s.pending, sess)
	sess.steamID = ident.SteamID
	sess.verified = ident.Verified
	sess.clientVersion = h.GetClientVersion()
	sess.platform = h.GetPlatform()
	sess.isDedicated = h.GetIsDedicated()
	sess.pingLocation = h.GetPingLocation()
	sess.lastSeen = now
	if c := h.GetCapabilities(); c != nil {
		sess.caps = capsFromProto(c)
		s.oracle.SetPop(ident.SteamID, c.GetSdrPopId())
	}
	s.sessions[ident.SteamID] = sess
	s.store.Update(ident.SteamID, func(p *Player) {
		p.LastSeen = now
		p.LastPop = sess.caps.SDRPopID
	})

	welcome := &pb.CMsgWelcome{
		Result:             pb.EResult_RESULT_OK.Enum(),
		SessionId:          proto.Uint64(sess.id),
		ServerTimeMs:       proto.Uint64(uint64(now.UnixMilli())),
		HeartbeatIntervalS: proto.Uint32(uint32(s.cfg.Timing.HeartbeatInterval.D().Seconds())),
		ProtocolVersion:    proto.Uint32(s.cfg.ProtocolVersion),
	}

	// A player who dropped mid-match gets their connect details back so the
	// client can rejoin without going through the queue again.
	if m := s.matchOf(ident.SteamID); m != nil && m.live() && m.hosted() {
		if slot, ok := m.Roster[ident.SteamID]; ok {
			slot.Present = true
			slot.LeftAt = time.Time{}
			welcome.Rejoin = s.joinMsg(m, slot)
		}
	}
	sess.Reply(req, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_Welcome{Welcome: welcome}})

	s.log.Info("session authenticated",
		"steam_id", ident.SteamID, "verified", ident.Verified,
		"build", sess.clientVersion, "dedicated", sess.isDedicated)
}

func (s *Server) onClosed(sess *Session) {
	delete(s.pending, sess)
	if sess.steamID == 0 {
		return
	}
	if cur, ok := s.sessions[sess.steamID]; !ok || cur != sess {
		return // already replaced by a newer login
	}
	delete(s.sessions, sess.steamID)
	s.log.Info("session closed", "steam_id", sess.steamID)

	m := s.matchOf(sess.steamID)
	if m == nil {
		return
	}
	slot, ok := m.Roster[sess.steamID]
	if !ok {
		return
	}
	slot.Present = false
	slot.LeftAt = time.Now()

	switch {
	case m.over():
		// Nothing to do; the tick will retire the match.
	case sess.steamID == m.HostID:
		s.log.Warn("host link lost", "match", m.ID, "host", sess.steamID)
		s.beginMigration(m, "host coordinator link dropped")
	case m.in(pb.EMatchState_MATCH_FORMING) || m.in(pb.EMatchState_MATCH_HOST_ASSIGNED):
		// The match has not started; drop the player rather than hold a slot
		// nobody is playing in.
		s.removeFromMatch(m, sess.steamID, pb.ELeaveReason_LEAVE_DISCONNECT)
	}
}

// ---------------------------------------------------------------------------
// Capabilities and queueing
// ---------------------------------------------------------------------------

func capsFromProto(c *pb.CMsgHostCapabilities) hostelect.Capabilities {
	return hostelect.Capabilities{
		CanHost:             c.GetCanHost(),
		UploadKbps:          c.GetUploadKbps(),
		CPUScore:            c.GetCpuScore(),
		MemoryMB:            c.GetMemoryMb(),
		SDRPopID:            c.GetSdrPopId(),
		HasPublicIP:         c.GetHasPublicIp(),
		IsDedicated:         c.GetIsDedicated(),
		MaxPlayersSupported: c.GetMaxPlayersSupported(),
	}
}

func (s *Server) onCapabilities(sess *Session, c *pb.CMsgHostCapabilities) {
	sess.caps = capsFromProto(c)
	s.oracle.SetPop(sess.steamID, c.GetSdrPopId())
	s.store.Update(sess.steamID, func(p *Player) { p.LastPop = c.GetSdrPopId() })
}

func (s *Server) onDeploy(sess *Session, env *pb.CMsgEnvelope, req *pb.CMsgDeployRequest) {
	now := time.Now()
	if p := s.store.Get(sess.steamID); p.Banned(now) {
		sess.Reply(env, queueStatusMsg(&pb.CMsgQueueStatus{
			Queued:  proto.Bool(false),
			Message: proto.String("matchmaking ban active: " + p.BanReason),
		}))
		return
	}
	if m := s.matchOf(sess.steamID); m != nil && !m.over() {
		sess.Reply(env, queueStatusMsg(&pb.CMsgQueueStatus{
			Queued:  proto.Bool(false),
			Message: proto.String("already assigned to a battle"),
		}))
		return
	}

	front := req.GetFrontId()
	if front == "" {
		active := s.world.ActiveFronts()
		if len(active) == 0 {
			sess.Reply(env, queueStatusMsg(&pb.CMsgQueueStatus{
				Queued:  proto.Bool(false),
				Message: proto.String("no active front: the war is quiet right now"),
			}))
			return
		}
		front = active[0].ID
	} else if _, ok := s.world.Front(front); !ok {
		sess.Reply(env, queueStatusMsg(&pb.CMsgQueueStatus{
			Queued:  proto.Bool(false),
			Message: proto.String("unknown front " + front),
		}))
		return
	}

	sess.inQueue = true
	sess.queuedAt = now
	sess.frontID = front
	sess.queueSide = int32(req.GetPreferredSide())
	sess.party = append([]uint64(nil), req.GetPartyMembers()...)
	if req.GetAcceptAnySide() {
		sess.queueSide = int32(pb.ESide_SIDE_UNASSIGNED)
	}
	if req.GetPingLocation() != "" {
		sess.pingLocation = req.GetPingLocation()
	}

	s.log.Info("deploy queued", "steam_id", sess.steamID, "front", front,
		"side", pb.ESide(sess.queueSide).String())
	sess.Reply(env, queueStatusMsg(s.queueStatusFor(sess)))
	s.tryFormMatches()
}

func (s *Server) onDeployCancel(sess *Session) {
	if !sess.inQueue {
		return
	}
	sess.inQueue = false
	sess.frontID = ""
	sess.Send(queueStatusMsg(&pb.CMsgQueueStatus{
		Queued:  proto.Bool(false),
		Message: proto.String("deployment cancelled"),
	}))
}

func queueStatusMsg(q *pb.CMsgQueueStatus) *pb.CMsgEnvelope {
	return &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_QueueStatus{QueueStatus: q}}
}

// ---------------------------------------------------------------------------
// Host lifecycle
// ---------------------------------------------------------------------------

func (s *Server) onHostReady(sess *Session, r *pb.CMsgHostReady) {
	m := s.matches[r.GetMatchId()]
	if m == nil || m.HostID != sess.steamID {
		s.log.Warn("host_ready from a non-host", "steam_id", sess.steamID, "match", r.GetMatchId())
		return
	}
	if !m.in(pb.EMatchState_MATCH_HOST_ASSIGNED) {
		return
	}
	// Everything downstream is one "connect" on somebody else's machine, so a
	// host that reports ready with nothing to connect to has not delivered.
	if r.GetConnectAddress() == "" && r.GetGameServerSteamId() == 0 {
		s.log.Warn("host_ready without an endpoint", "match", m.ID, "host", sess.steamID)
		s.hostFailed(m, sess.steamID, "host reported ready without an address or a game server identity")
		return
	}

	m.ConnectAddress = r.GetConnectAddress()
	m.GameServerSteamID = r.GetGameServerSteamId()
	if r.GetMapName() != "" && r.GetMapName() != m.Map {
		// The host booted a different map than it was told to. That breaks the
		// premise of the match, so refuse it rather than quietly accept.
		s.hostFailed(m, sess.steamID, "host loaded "+r.GetMapName()+" instead of "+m.Map)
		return
	}
	m.setState(pb.EMatchState_MATCH_LOADING)
	s.log.Info("host ready", "match", m.ID, "host", sess.steamID,
		"address", m.ConnectAddress, "game_server", m.GameServerSteamID, "map", m.Map)

	for _, slot := range m.slots() {
		if slot.SteamID == m.HostID {
			slot.Connected = true
			continue
		}
		s.send(slot.SteamID, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_JoinBattle{JoinBattle: s.joinMsg(m, slot)}})
	}
}

// joinMsg tells one player where the battle is. It is deliberately the whole
// story: the client issues one connect against what is in here and nothing
// else. Admission is not enforced by this message — a Greyline host takes
// whoever reaches it, and the roster is what the coordinator counts.
func (s *Server) joinMsg(m *Match, slot *RosterSlot) *pb.CMsgJoinBattle {
	return &pb.CMsgJoinBattle{
		MatchId:           proto.String(m.ID),
		FrontId:           proto.String(m.FrontID),
		MapName:           proto.String(m.Map),
		Side:              pbSide(slot.Side).Enum(),
		JoinDeadlineS:     proto.Uint32(uint32(s.cfg.Timing.ClientConnectDeadline.D().Seconds())),
		ConnectAddress:    proto.String(m.ConnectAddress),
		GameServerSteamId: proto.Uint64(m.GameServerSteamID),
		HostSteamId:       proto.Uint64(m.HostID),
	}
}

func (s *Server) onHostFailed(sess *Session, f *pb.CMsgHostFailed) {
	m := s.matches[f.GetMatchId()]
	if m == nil || m.HostID != sess.steamID {
		return
	}
	s.hostFailed(m, sess.steamID, f.GetReason())
}

func (s *Server) onClientJoinState(sess *Session, st *pb.CMsgClientJoinState) {
	m := s.matches[st.GetMatchId()]
	if m == nil {
		return
	}
	slot, ok := m.Roster[sess.steamID]
	if !ok {
		return
	}
	slot.Connected = st.GetConnected()
	if r := st.GetRttMs(); r > 0 {
		slot.RTTMs = r
		s.oracle.Observe(sess.steamID, m.HostID, int(r))
	}
	if !st.GetConnected() && st.GetFailure() != "" {
		s.log.Warn("client failed to reach host",
			"match", m.ID, "steam_id", sess.steamID, "reason", st.GetFailure())
	}
	if m.in(pb.EMatchState_MATCH_LOADING) && m.connectedCount() >= len(m.Order) {
		m.setState(pb.EMatchState_MATCH_LIVE)
		s.stats.MatchesLive++
		s.log.Info("match live", "match", m.ID, "map", m.Map, "players", len(m.Order))
	}
}

// ---------------------------------------------------------------------------
// Heartbeats
// ---------------------------------------------------------------------------

func (s *Server) onHostHeartbeat(sess *Session, hb *pb.CMsgHostHeartbeat) {
	m := s.matches[hb.GetMatchId()]
	if m == nil || m.HostID != sess.steamID {
		return
	}
	if snap := hb.GetSnapshot(); snap != nil {
		// The snapshot is the whole basis of host migration: without a recent
		// one, a migrated match restarts from scratch.
		m.Snapshot = snap
	}
	for _, e := range hb.GetRoster() {
		if slot, ok := m.Roster[e.GetSteamId()]; ok {
			slot.Connected = e.GetConnected()
		}
	}
	if m.in(pb.EMatchState_MATCH_LOADING) && hb.GetState() == pb.EMatchState_MATCH_LIVE {
		m.setState(pb.EMatchState_MATCH_LIVE)
		s.stats.MatchesLive++
	}
}

func (s *Server) onClientHeartbeat(sess *Session, hb *pb.CMsgClientHeartbeat) {
	m := s.matches[hb.GetMatchId()]
	if m == nil {
		return
	}
	slot, ok := m.Roster[sess.steamID]
	if !ok {
		return
	}
	slot.Connected = hb.GetInMatch()
	slot.Present = true
	slot.LeftAt = time.Time{}
	if r := hb.GetRttToHostMs(); r > 0 {
		slot.RTTMs = r
		s.oracle.Observe(sess.steamID, m.HostID, int(r))
	}
}

func (s *Server) onPlayerLeft(sess *Session, pl *pb.CMsgPlayerLeft) {
	m := s.matches[pl.GetMatchId()]
	// Only the host is authoritative about who is on its server.
	if m == nil || m.HostID != sess.steamID {
		return
	}
	target := pl.GetSteamId()
	slot, ok := m.Roster[target]
	if !ok {
		return
	}
	slot.Connected = false
	if m.live() && pl.GetReason() == pb.ELeaveReason_LEAVE_ABANDON {
		s.punishAbandon(m, target)
	}
	s.log.Info("player left match", "match", m.ID, "steam_id", target, "reason", pl.GetReason().String())
}

// ---------------------------------------------------------------------------
// Integrity
// ---------------------------------------------------------------------------

func (s *Server) onIntegrityReport(sess *Session, rep *pb.CMsgIntegrityReport) {
	m := s.matches[rep.GetMatchId()]
	if m == nil {
		return
	}
	fromHost := m.HostID == sess.steamID

	if fromHost {
		if s.cfg.Security.RequireSignedResults &&
			!security.VerifyIntegrityReport(m.Secret, m.ID, rep.GetPolicyDigest(), len(rep.GetViolations()), rep.GetSignature()) {
			// Only the real host holds the match secret, so a bad signature
			// means either tampering in transit or a forged report.
			s.log.Warn("integrity report signature invalid", "match", m.ID, "steam_id", sess.steamID)
			s.flag(m, sess.steamID, "integrity report signature invalid")
			return
		}
		if !equalBytes(rep.GetPolicyDigest(), m.Policy.Digest()) {
			s.abortMatch(m, "host is running a policy other than the one it was issued")
			return
		}
		m.LastIntegrity = time.Now()
		m.MissedIntegrity = 0

		if !m.Policy.AllowPlugins && len(rep.GetLoadedPlugins()) > 0 {
			s.flag(m, m.HostID, "server plugins loaded: "+join(rep.GetLoadedPlugins()))
		}
		if !m.Policy.AllowRCON && rep.GetRconEnabled() {
			s.flag(m, m.HostID, "rcon is enabled on the host")
		}
		if !m.Policy.AllowBots && rep.GetBotCount() > 0 {
			s.flag(m, m.HostID, "bots present in a war match")
		}
	}

	// Observations are re-checked here rather than trusting the host's own
	// violation list. A host that reports a clean sv_cheats while its clients
	// report 1 is caught by the disagreement, not by its own honesty.
	for _, obs := range rep.GetObservations() {
		subject := obs.GetSteamId()
		if subject == 0 {
			subject = m.HostID
		}
		if _, inMatch := m.Roster[subject]; !inMatch {
			continue
		}
		if !fromHost && subject != sess.steamID {
			// A client may only speak about itself and about the server's own
			// replicated values, never about other players.
			continue
		}
		if st := obs.GetQueryStatus(); st != "" && st != "ok" {
			continue // the engine could not answer; not evidence of anything
		}
		rule, expected, ok := m.Policy.Check(obs.GetName(), obs.GetValue())
		if ok {
			continue
		}
		s.log.Warn("policy violation",
			"match", m.ID, "subject", subject, "cvar", obs.GetName(),
			"observed", obs.GetValue(), "expected", expected, "fatal", rule.Fatal)
		if rule.Fatal {
			s.punishViolation(m, subject, obs.GetName()+"="+obs.GetValue()+" (expected "+expected+")")
		} else {
			s.flag(m, subject, obs.GetName()+"="+obs.GetValue()+" (expected "+expected+")")
		}
	}
}

// flag records a non-fatal integrity concern. Enough of them abort the match,
// because a host that keeps drifting out of policy is not running a fair game
// even if no single deviation is damning.
func (s *Server) flag(m *Match, steamID uint64, reason string) {
	m.Flags++
	s.log.Warn("match flagged", "match", m.ID, "steam_id", steamID,
		"reason", reason, "flags", m.Flags)
	s.send(steamID, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_SecurityVerdict{SecurityVerdict: &pb.CMsgSecurityVerdict{
		MatchId: proto.String(m.ID),
		SteamId: proto.Uint64(steamID),
		Action:  proto.String("flag"),
		Reason:  proto.String(reason),
	}}})
	if m.Flags >= s.cfg.Security.FlagsBeforeAbort {
		s.abortMatch(m, "too many integrity flags")
	}
}

// punishViolation acts on a fatal policy breach. A cheating client is removed;
// a cheating host invalidates the whole match, since nothing it reports can be
// trusted afterwards.
func (s *Server) punishViolation(m *Match, steamID uint64, reason string) {
	if steamID == m.HostID {
		s.abortMatch(m, "host policy violation: "+reason)
		return
	}
	until := s.store.Ban(steamID, s.cfg.Security.ViolationBan.D(), "policy violation: "+reason)
	s.send(steamID, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_SecurityVerdict{SecurityVerdict: &pb.CMsgSecurityVerdict{
		MatchId:    proto.String(m.ID),
		SteamId:    proto.Uint64(steamID),
		Action:     proto.String("kick"),
		Reason:     proto.String(reason),
		BanSeconds: proto.Uint32(uint32(time.Until(until).Seconds())),
	}}})
	// The host enforces the kick; it is the only party that can drop a
	// connection on the game server.
	s.send(m.HostID, &pb.CMsgEnvelope{Body: &pb.CMsgEnvelope_SecurityVerdict{SecurityVerdict: &pb.CMsgSecurityVerdict{
		MatchId: proto.String(m.ID),
		SteamId: proto.Uint64(steamID),
		Action:  proto.String("kick"),
		Reason:  proto.String(reason),
	}}})
	s.removeFromMatch(m, steamID, pb.ELeaveReason_LEAVE_KICKED)
}

// ---------------------------------------------------------------------------
// Results
// ---------------------------------------------------------------------------

func (s *Server) onMatchResult(sess *Session, r *pb.CMsgMatchResult) {
	m := s.matches[r.GetMatchId()]
	if m == nil || m.HostID != sess.steamID {
		s.log.Warn("match result from a non-host", "steam_id", sess.steamID, "match", r.GetMatchId())
		return
	}
	if m.Resolved {
		return
	}
	if s.cfg.Security.RequireSignedResults &&
		!security.VerifyResult(m.Secret, m.ID, int32(r.GetOutcome()),
			r.GetRedScore(), r.GetBluScore(), r.GetDurationS(), r.GetSignature()) {
		s.log.Warn("match result signature invalid", "match", m.ID, "host", sess.steamID)
		s.abortMatch(m, "host submitted an unsigned or forged result")
		return
	}
	if o := outcomeFromScores(r.GetRedScore(), r.GetBluScore()); warOutcome(r.GetOutcome()) != o &&
		r.GetOutcome() != pb.EMatchOutcome_OUTCOME_ABANDONED {
		// The declared winner contradicts the scoreline the host itself sent.
		s.abortMatch(m, "host reported an outcome inconsistent with its own scores")
		return
	}
	m.HostResult = r
	m.ResultAt = time.Now()
	s.log.Info("host reported result", "match", m.ID,
		"outcome", r.GetOutcome().String(), "red", r.GetRedScore(), "blu", r.GetBluScore())
	s.tryResolve(m)
}

func (s *Server) onResultAttestation(sess *Session, a *pb.CMsgResultAttestation) {
	m := s.matches[a.GetMatchId()]
	if m == nil || m.Resolved {
		return
	}
	if _, ok := m.Roster[sess.steamID]; !ok || sess.steamID == m.HostID {
		return
	}
	m.Attestations[sess.steamID] = attestation{
		outcome: warOutcome(a.GetOutcome()),
		red:     a.GetRedScore(),
		blu:     a.GetBluScore(),
		at:      time.Now(),
	}
	s.tryResolve(m)
}

// ---------------------------------------------------------------------------
// Host loss reported by clients
// ---------------------------------------------------------------------------

func (s *Server) onHostLost(sess *Session, hl *pb.CMsgHostLost) {
	m := s.matches[hl.GetMatchId()]
	if m == nil || !m.live() || m.in(pb.EMatchState_MATCH_MIGRATING) {
		return
	}
	slot, ok := m.Roster[sess.steamID]
	if !ok || sess.steamID == m.HostID {
		return
	}
	slot.Connected = false

	// One client losing the host means that client has a network problem.
	// A majority losing it means the host is gone. Requiring a majority stops
	// a single player from forcing migrations at will.
	lost := 0
	for _, sl := range m.slots() {
		if sl.SteamID != m.HostID && sl.Present && !sl.Connected {
			lost++
		}
	}
	present := m.nonHostPresent()
	if present > 0 && lost*2 > present {
		s.log.Warn("clients report the host is gone",
			"match", m.ID, "lost", lost, "present", present, "detail", hl.GetDetail())
		s.beginMigration(m, "majority of players lost the host")
	}
}

// ---------------------------------------------------------------------------
// world snapshot
// ---------------------------------------------------------------------------

func (s *Server) worldState() *pb.CMsgWorldState {
	snap := s.world.Snapshot()
	out := &pb.CMsgWorldState{
		Revision:      proto.Uint64(snap.Revision),
		OnlinePlayers: proto.Uint32(uint32(len(s.sessions))),
	}
	for _, n := range snap.Nodes {
		out.Nodes = append(out.Nodes, &pb.CMsgWorldNode{
			Id:          proto.String(n.ID),
			DisplayName: proto.String(n.Name),
			Owner:       pbSide(n.Owner).Enum(),
			State:       proto.String(n.State),
			Adjacent:    append([]string(nil), n.Adjacent...),
		})
	}

	queuedPerFront := map[string]uint32{}
	for _, sess := range s.sessions {
		if sess.inQueue {
			queuedPerFront[sess.frontID]++
		}
	}
	livePerFront := map[string][]*pb.CMsgLiveBattle{}
	for _, m := range s.matches {
		if m.over() {
			continue
		}
		var red, blu uint32
		if m.Snapshot != nil {
			red, blu = m.Snapshot.GetRedScore(), m.Snapshot.GetBluScore()
		}
		livePerFront[m.FrontID] = append(livePerFront[m.FrontID], &pb.CMsgLiveBattle{
			MatchId:    proto.String(m.ID),
			MapName:    proto.String(m.Map),
			State:      m.State.Enum(),
			Players:    proto.Uint32(uint32(m.connectedCount())),
			MaxPlayers: proto.Uint32(uint32(m.MaxPlrs)),
			RedScore:   proto.Uint32(red),
			BluScore:   proto.Uint32(blu),
			ElapsedS:   proto.Uint32(uint32(time.Since(m.CreatedAt).Seconds())),
		})
	}

	for _, f := range snap.Fronts {
		out.Fronts = append(out.Fronts, &pb.CMsgBattleFront{
			Id:            proto.String(f.ID),
			NodeId:        proto.String(f.NodeID),
			DisplayName:   proto.String(f.Name),
			RedPoints:     proto.Uint32(f.RedPoints),
			BluPoints:     proto.Uint32(f.BluPoints),
			PointsToWin:   proto.Uint32(f.PointsToWin),
			Attacker:      pbSide(f.Attacker).Enum(),
			QueuedPlayers: proto.Uint32(queuedPerFront[f.ID]),
			Live:          livePerFront[f.ID],
		})
	}
	out.UnderstrengthSide = pbSide(s.understrengthSide()).Enum()
	return out
}

// understrengthSide reports which side the coordinator would like more players
// on, counting both queued players and players already in a battle.
func (s *Server) understrengthSide() war.Side {
	var red, blu int
	for _, sess := range s.sessions {
		if sess.inQueue {
			switch pb.ESide(sess.queueSide) {
			case pb.ESide_SIDE_RED:
				red++
			case pb.ESide_SIDE_BLU:
				blu++
			}
		}
	}
	for _, m := range s.matches {
		if m.over() {
			continue
		}
		r, b := m.sideCounts()
		red += r
		blu += b
	}
	switch {
	case red > blu:
		return war.SideBlu
	case blu > red:
		return war.SideRed
	default:
		return war.SideNone
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func join(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
