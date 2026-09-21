// Package gc is the authoritative Valve-compatible game coordinator surface.
// The game still speaks its native EMsg/protobuf protocol; only the transport
// is replaced by /v1/gc/exchange.
package gc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/gcwire"
	"github.com/gru2007/team-frontress/services/coordinator/internal/maps"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/steamauth"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
	"google.golang.org/protobuf/proto"
)

const (
	msgSOCreate           = 21
	msgSOUpdate           = 22
	msgSODestroy          = 23
	msgCacheSubscribed    = 24
	msgClientWelcome      = 4004
	msgServerWelcome      = 4005
	msgClientHello        = 4006
	msgServerHello        = 4007
	msgSetOptions         = 6554
	msgSetOptionsReply    = 6555
	msgQueue              = 6556
	msgQueueReply         = 6557
	msgRemoveQueue        = 6558
	msgRemoveQueueReply   = 6559
	msgInvitePlayer       = 6560
	msgRequestJoin        = 6561
	msgSendChat           = 6562
	msgChat               = 6563
	msgQueueStandby       = 6567
	msgQueueStandbyReply  = 6568
	msgRemoveStandby      = 6569
	msgRemoveStandbyReply = 6570
	msgClearPending       = 6571
	msgClearPendingReply  = 6572
	msgClearOther         = 6573
	msgClearOtherReply    = 6574
	msgPromoteLeader      = 6575
	msgKickMember         = 6576
	msgAcceptLobby        = 6578
	msgAcceptLobbyReply   = 6579
	msgServerStatus       = 6295 // k_EMsgGCGameServerMatchmakingStatus
	msgServerKicking      = 6299
	msgMatchResult        = 6512
	msgMatchResultReply   = 6520
	msgServerKickReply    = 6521
	msgPlayerLeft         = 6522
	msgPlayerLeftReply    = 6523
	msgVoteKick           = 6581
	msgVoteKickReply      = 6582
	msgServerUpdate       = 6587
	msgNewMatch           = 6537
	msgNewMatchReply      = 6538
	msgChangeTeams        = 6539
	msgChangeTeamsReply   = 6540
	partyType             = 2003
	lobbyType             = 2004
	partyInviteType       = 2006
)

var (
	ErrBadRequest   = errors.New("invalid GC exchange")
	ErrUnauthorized = errors.New("GC authentication failed")
	ErrConflict     = errors.New("GC session conflict")
)

type Matchmaker interface {
	Enqueue(*mm.Ticket) (*mm.Ticket, error)
	Cancel(string) error
	Status(string) (wire.QueueStatus, error)
	MatchAssignment(string) (*wire.Assignment, bool)
	ReportResult(context.Context, wire.MatchResult) error
}

type ratingProvider interface {
	Ratings(context.Context, []wire.SteamID) (map[wire.SteamID]int, error)
}

type activeGameRecoverer interface {
	RecoverActive(context.Context, wire.MatchGroup, wire.SteamID, []wire.AssignedPlayer) (*mm.Ticket, bool, error)
}

type Server struct {
	secret   string
	mm       Matchmaker
	verifier steamauth.Verifier
	log      *slog.Logger

	mu        sync.Mutex
	sessions  map[string]*session
	instances map[string]string
	parties   map[wire.SteamID]*party
	ready     map[string]bool
	invites   map[wire.SteamID]map[uint64]*partyInvite
}

type session struct {
	id             string
	role           string
	steamID        wire.SteamID
	matchID        string
	serverID       uint64
	lastSeen       time.Time
	out            []gcwire.Message
	sent           []gcwire.Message
	instanceID     string
	clientSequence uint64
	serverSequence uint64
	sentSequence   uint64
	ackedSequence  uint64
	welcome        bool
	version        uint64
	lobbyID        uint64
	lobbyHash      string
	assignmentHash string
	standbyTicket  string
	standbyGroup   wire.MatchGroup
}

type party struct {
	id           uint64
	leader       wire.SteamID
	members      []wire.SteamID
	criteria     *gcproto.CTFGroupMatchCriteriaProto
	tickets      map[wire.MatchGroup]string
	queuedAt     map[wire.MatchGroup]uint32
	lobbyID      uint64
	lobbyGroup   wire.MatchGroup
	lobbyMatchID string
	pending      map[wire.SteamID]gcproto.TFPendingPartyMember_EType
	standby      map[wire.SteamID]bool
}

type partyInvite struct {
	partyID uint64
	inviter wire.SteamID
	typ     gcproto.CSOTFPartyInvite_Type
}

func New(secret string, m Matchmaker, verifier steamauth.Verifier, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{secret: secret, mm: m, verifier: verifier, log: log,
		sessions: map[string]*session{}, instances: map[string]string{}, parties: map[wire.SteamID]*party{}, ready: map[string]bool{},
		invites: map[wire.SteamID]map[uint64]*partyInvite{}}
}

func (s *Server) Exchange(ctx context.Context, req gcwire.ExchangeRequest) (gcwire.ExchangeResponse, error) {
	if req.Protocol != 1 && req.Protocol != 2 {
		return gcwire.ExchangeResponse{}, fmt.Errorf("%w: unsupported protocol %d", ErrBadRequest, req.Protocol)
	}
	if len(req.Messages) > gcwire.MaxMessages {
		return gcwire.ExchangeResponse{}, fmt.Errorf("%w: too many messages", ErrBadRequest)
	}
	packets := make([]gcwire.Packet, 0, len(req.Messages))
	batchBytes := 0
	for _, raw := range req.Messages {
		batchBytes += len(raw.Data)
		if batchBytes > gcwire.MaxBatchBytes {
			return gcwire.ExchangeResponse{}, fmt.Errorf("%w: message batch too large", ErrBadRequest)
		}
		packet, err := gcwire.Decode(raw)
		if err != nil {
			return gcwire.ExchangeResponse{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
		packets = append(packets, packet)
	}
	if req.Protocol == 2 {
		if req.InstanceID == "" || len(req.InstanceID) > 64 || req.ClientSequence == 0 {
			return gcwire.ExchangeResponse{}, fmt.Errorf("%w: protocol 2 requires instance_id and client_sequence", ErrBadRequest)
		}
	}
	// Steam Web API verification can take hundreds of milliseconds. New
	// sessions authenticate before taking the coordinator state lock so one
	// login cannot pause every established GC connection.
	var candidate *session
	if req.SessionID == "" {
		var err error
		candidate, err = s.authenticateSession(ctx, req)
		if err != nil {
			return gcwire.ExchangeResponse{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneSessionsLocked(time.Now().Add(-2 * time.Minute))

	sess, ok := s.sessions[req.SessionID]
	if !ok && req.Protocol == 2 && req.SessionID != "" {
		return gcwire.ExchangeResponse{}, fmt.Errorf("%w: session expired", ErrConflict)
	}
	if !ok && req.Protocol == 2 {
		if id := s.instances[instanceKey(req)]; id != "" {
			sess, ok = s.sessions[id]
			if !ok {
				delete(s.instances, instanceKey(req))
			}
		}
	}
	if !ok {
		if candidate == nil {
			var err error
			candidate, err = s.authenticateSession(ctx, req)
			if err != nil {
				return gcwire.ExchangeResponse{}, err
			}
		}
		sess = s.installSessionLocked(candidate, req)
	} else if sess.role != req.Role || (sess.role == "client" && string(sess.steamID) != req.SteamID) || (sess.role == "server" && sess.matchID != req.MatchID) || (req.Protocol == 2 && sess.instanceID != req.InstanceID) {
		return gcwire.ExchangeResponse{}, fmt.Errorf("%w: identity changed", ErrConflict)
	}
	sess.lastSeen = time.Now()

	if req.Protocol == 2 {
		if req.AckServerSequence > sess.serverSequence {
			return gcwire.ExchangeResponse{}, fmt.Errorf("%w: acknowledgement is ahead of the server", ErrConflict)
		}
		if req.AckServerSequence > sess.ackedSequence {
			if req.AckServerSequence != sess.sentSequence {
				return gcwire.ExchangeResponse{}, fmt.Errorf("%w: acknowledgement skipped a response", ErrConflict)
			}
			sess.ackedSequence = req.AckServerSequence
			sess.sent = nil
			sess.sentSequence = 0
		}
		if req.ClientSequence < sess.clientSequence || req.ClientSequence > sess.clientSequence+1 {
			return gcwire.ExchangeResponse{}, fmt.Errorf("%w: unexpected client sequence", ErrConflict)
		}
		if req.ClientSequence == sess.clientSequence {
			return s.responseLocked(sess, req.ClientSequence), nil
		}
	}

	for _, packet := range packets {
		if err := s.handleLocked(ctx, sess, packet); err != nil {
			return gcwire.ExchangeResponse{}, err
		}
	}
	if req.Protocol == 2 {
		sess.clientSequence = req.ClientSequence
	}
	s.syncLocked(sess)
	if req.Protocol == 2 {
		return s.responseLocked(sess, req.ClientSequence), nil
	}
	out := append([]gcwire.Message(nil), sess.out...)
	sess.out = sess.out[:0]
	return gcwire.ExchangeResponse{SessionID: sess.id, Connected: true, PollAfter: 250, Messages: out}, nil
}

func (s *Server) responseLocked(sess *session, clientSequence uint64) gcwire.ExchangeResponse {
	if len(sess.sent) == 0 && len(sess.out) != 0 {
		sess.serverSequence++
		sess.sentSequence = sess.serverSequence
		sess.sent = append([]gcwire.Message(nil), sess.out...)
		sess.out = sess.out[:0]
	}
	return gcwire.ExchangeResponse{
		SessionID: sess.id, Connected: sess.welcome, PollAfter: 250,
		ClientSequence: clientSequence, ServerSequence: sess.serverSequence,
		Messages: append([]gcwire.Message(nil), sess.sent...),
	}
}

func instanceKey(req gcwire.ExchangeRequest) string {
	identity := req.SteamID
	if req.Role == "server" {
		identity = req.MatchID
	}
	return req.Role + "\x00" + identity + "\x00" + req.InstanceID
}

func (s *Server) authenticateSession(ctx context.Context, req gcwire.ExchangeRequest) (*session, error) {
	if req.Role != "client" && req.Role != "server" {
		return nil, errors.New("role must be client or server")
	}
	sess := &session{id: randomID(), role: req.Role, instanceID: req.InstanceID, lastSeen: time.Now()}
	if req.Role == "client" {
		claimed := wire.SteamID(req.SteamID)
		id, err := s.verifier.Verify(ctx, claimed, req.Ticket)
		if err != nil || id != claimed {
			return nil, fmt.Errorf("%w: Steam ticket rejected: %v", ErrUnauthorized, err)
		}
		sess.steamID = id
	} else {
		if subtle.ConstantTimeCompare([]byte(req.ServerToken), []byte(s.secret)) != 1 {
			return nil, fmt.Errorf("%w: server token rejected", ErrUnauthorized)
		}
		if req.MatchID == "" {
			return nil, errors.New("server match_id is required")
		}
		sess.matchID = req.MatchID
		sess.serverID, _ = strconv.ParseUint(req.SteamID, 10, 64)
	}
	return sess, nil
}

func (s *Server) installSessionLocked(sess *session, req gcwire.ExchangeRequest) *session {
	if sess.role == "client" && s.parties[sess.steamID] == nil {
		u, _ := strconv.ParseUint(string(sess.steamID), 10, 64)
		s.parties[sess.steamID] = &party{id: u, leader: sess.steamID, members: []wire.SteamID{sess.steamID}, tickets: map[wire.MatchGroup]string{}, queuedAt: map[wire.MatchGroup]uint32{}, pending: map[wire.SteamID]gcproto.TFPendingPartyMember_EType{}, standby: map[wire.SteamID]bool{}}
	}
	s.sessions[sess.id] = sess
	if req.Protocol == 2 {
		s.instances[instanceKey(req)] = sess.id
	}
	return sess
}

func (s *Server) handleLocked(ctx context.Context, sess *session, packet gcwire.Packet) error {
	switch packet.Type {
	case msgClientHello:
		if sess.role != "client" {
			return errors.New("client hello from server session")
		}
		sess.welcome = true
		if err := s.push(sess, msgClientWelcome, nil, &gcproto.CMsgClientWelcome{Version: proto.Uint32(1)}); err != nil {
			return err
		}
		return s.pushCacheLocked(sess)
	case msgServerHello:
		if sess.role != "server" {
			return errors.New("server hello from client session")
		}
		sess.welcome = true
		if err := s.push(sess, msgServerWelcome, nil, &gcproto.CMsgServerWelcome{ActiveVersion: proto.Uint32(1), MinAllowedVersion: proto.Uint32(1)}); err != nil {
			return err
		}
		return s.pushServerCacheLocked(sess)
	case msgSetOptions:
		var req gcproto.CMsgPartySetOptions
		if err := proto.Unmarshal(packet.Body, &req); err != nil {
			return err
		}
		p := s.parties[sess.steamID]
		if p == nil || p.leader != sess.steamID || req.GetPartyId() != p.id {
			return errors.New("not party leader")
		}
		if req.Options != nil && req.Options.GetGroupCriteria() != nil {
			p.criteria = proto.Clone(req.Options.GetGroupCriteria()).(*gcproto.CTFGroupMatchCriteriaProto)
		}
		if err := s.push(sess, msgSetOptionsReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartySetOptionsResponse{}); err != nil {
			return err
		}
		return s.pushPartyUpdateLocked(p)
	case msgQueue:
		return s.queueLocked(ctx, sess, packet)
	case msgRemoveQueue:
		return s.removeQueueLocked(sess, packet)
	case msgInvitePlayer:
		return s.invitePlayerLocked(sess, packet)
	case msgRequestJoin:
		return s.requestJoinLocked(sess, packet)
	case msgSendChat:
		return s.sendChatLocked(sess, packet)
	case msgQueueStandby:
		return s.queueStandbyLocked(sess, packet)
	case msgRemoveStandby:
		if sess.standbyTicket != "" {
			_ = s.mm.Cancel(sess.standbyTicket)
			sess.standbyTicket = ""
		}
		if p := s.parties[sess.steamID]; p != nil {
			delete(p.standby, sess.steamID)
			_ = s.pushPartyUpdateLocked(p)
		}
		return s.push(sess, msgRemoveStandbyReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartyRemoveFromStandbyQueueResponse{})
	case msgClearPending:
		return s.clearPendingLocked(sess, packet)
	case msgClearOther:
		return s.clearOtherLocked(sess, packet)
	case msgPromoteLeader:
		return s.promoteLocked(sess, packet)
	case msgKickMember:
		return s.kickLocked(sess, packet)
	case msgAcceptLobby:
		return s.push(sess, msgAcceptLobbyReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgAcceptLobbyInviteReply{})
	case msgServerStatus:
		if sess.role != "server" {
			return errors.New("server status from client session")
		}
		var status gcproto.CMsgGameServerMatchmakingStatus
		if err := proto.Unmarshal(packet.Body, &status); err != nil {
			return err
		}
		if assignment, ok := s.mm.MatchAssignment(sess.matchID); ok && len(assignment.Roster) > 0 {
			acknowledged := make(map[uint64]bool, len(status.Players))
			for _, player := range status.Players {
				acknowledged[player.GetSteamId()] = player.GetConnectState() != gcproto.CMsgGameServerMatchmakingStatus_INVALID
			}
			ready := true
			for _, expected := range assignment.Roster {
				id, _ := strconv.ParseUint(string(expected.SteamID), 10, 64)
				if !acknowledged[id] {
					ready = false
					break
				}
			}
			s.ready[sess.matchID] = ready
		}
		return nil
	case msgServerUpdate:
		if sess.role != "server" {
			return errors.New("server data from client session")
		}
		var update gcproto.CMsgGameServerData
		if err := proto.Unmarshal(packet.Body, &update); err != nil {
			return err
		}
		if update.GetServerSteamid() != 0 {
			if sess.serverID != 0 && sess.serverID != update.GetServerSteamid() {
				return fmt.Errorf("%w: game server SteamID changed", ErrConflict)
			}
			sess.serverID = update.GetServerSteamid()
		}
		return nil
	case msgMatchResult:
		if sess.role != "server" {
			return errors.New("match result from client session")
		}
		var result gcproto.CMsgGC_Match_Result
		if err := proto.Unmarshal(packet.Body, &result); err != nil {
			return err
		}
		winner := wire.TeamUnassigned
		if result.GetWinningTeam() == 2 {
			winner = wire.TeamRed
		}
		if result.GetWinningTeam() == 3 {
			winner = wire.TeamBlu
		}
		players := make([]wire.AssignedPlayer, 0, len(result.GetPlayers()))
		for _, reported := range result.GetPlayers() {
			if reported.GetSteamId() == 0 {
				continue
			}
			players = append(players, wire.AssignedPlayer{
				SteamID: wire.SteamID(strconv.FormatUint(reported.GetSteamId(), 10)),
				Team:    wire.Team(reported.GetTeam()),
			})
		}
		assignment, _ := s.mm.MatchAssignment(sess.matchID)
		if err := s.mm.ReportResult(ctx, wire.MatchResult{MatchID: sess.matchID, Winner: winner, RedScore: int(result.GetRedScore()), BluScore: int(result.GetBlueScore()), Aborted: result.GetStatus() != gcproto.CMsgGC_Match_Result_MATCH_SUCCEEDED, Players: players}); err != nil {
			return err
		}
		delete(s.ready, sess.matchID)
		s.finishMatchLocked(sess.matchID, assignment)
		return s.push(sess, msgMatchResultReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgGC_Match_ResultResponse{})
	case msgServerKicking:
		if sess.role != "server" {
			return errors.New("server lobby close from client session")
		}
		return s.push(sess, msgServerKickReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgGameServerKickingLobbyResponse{})
	case msgPlayerLeft:
		return s.push(sess, msgPlayerLeftReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPlayerLeftMatchResponse{})
	case msgVoteKick:
		var request gcproto.CMsgProcessMatchVoteKick
		if err := proto.Unmarshal(packet.Body, &request); err != nil {
			return err
		}
		return s.push(sess, msgVoteKickReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgProcessMatchVoteKickResponse{Rip: proto.Bool(request.GetDefaultPass())})
	case msgNewMatch:
		return s.push(sess, msgNewMatchReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgGCNewMatchForLobbyResponse{Success: proto.Bool(false)})
	case msgChangeTeams:
		return s.push(sess, msgChangeTeamsReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgGCChangeMatchPlayerTeamsResponse{Success: proto.Bool(false)})
	default:
		// Unknown Valve messages are intentionally ignored. The client may send
		// inventory/econ traffic that belongs to Valve and is not authoritative
		// for Frontress matchmaking.
		return nil
	}
}

func (s *Server) queueLocked(ctx context.Context, sess *session, packet gcwire.Packet) error {
	var req gcproto.CMsgPartyQueueForMatch
	if err := proto.Unmarshal(packet.Body, &req); err != nil {
		return err
	}
	p := s.parties[sess.steamID]
	if p == nil || p.leader != sess.steamID || req.GetPartyId() != p.id {
		return errors.New("not party leader")
	}
	group := wire.MatchGroup(req.GetMatchGroup())
	if req.GetFinalOptions().GetGroupCriteria() != nil {
		p.criteria = proto.Clone(req.GetFinalOptions().GetGroupCriteria()).(*gcproto.CTFGroupMatchCriteriaProto)
	}
	players := make([]wire.AssignedPlayer, 0, len(p.members))
	for _, id := range p.members {
		players = append(players, wire.AssignedPlayer{SteamID: id})
	}
	if recoverer, ok := s.mm.(activeGameRecoverer); ok {
		if recovered, found, err := recoverer.RecoverActive(ctx, group, p.leader, players); err != nil {
			s.log.Info("GC queue request refused", "leader", p.leader, "group", group, "err", err)
			if err := s.push(sess, msgQueueReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartyQueueForMatchResponse{}); err != nil {
				return err
			}
			return s.pushPartyUpdateLocked(p)
		} else if found {
			p.tickets[group] = recovered.ID
			p.queuedAt[group] = uint32(time.Now().Unix())
			if err := s.push(sess, msgQueueReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartyQueueForMatchResponse{}); err != nil {
				return err
			}
			return s.pushPartyUpdateLocked(p)
		}
	}
	if provider, ok := s.mm.(ratingProvider); ok {
		ids := make([]wire.SteamID, 0, len(players))
		for _, player := range players {
			ids = append(ids, player.SteamID)
		}
		if ratings, err := provider.Ratings(ctx, ids); err == nil {
			for i := range players {
				players[i].Rating = ratings[players[i].SteamID]
			}
		}
	}
	lateJoin := true
	if p.criteria != nil && p.criteria.LateJoinOk != nil {
		lateJoin = p.criteria.GetLateJoinOk()
	}
	var selectedMaps []string
	if p.criteria != nil && p.criteria.GetCasualCriteria() != nil {
		selectedMaps = maps.SelectedFromBits(p.criteria.GetCasualCriteria().GetSelectedMapsBits())
	}
	t, err := s.mm.Enqueue(&mm.Ticket{MatchGroup: group, Leader: p.leader, Players: players, Maps: selectedMaps, LateJoinOK: lateJoin})
	if err != nil {
		s.log.Info("GC queue request refused", "leader", p.leader, "group", group, "err", err)
		if err := s.push(sess, msgQueueReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartyQueueForMatchResponse{}); err != nil {
			return err
		}
		return s.pushPartyUpdateLocked(p)
	}
	p.tickets[group] = t.ID
	p.queuedAt[group] = uint32(time.Now().Unix())
	if err := s.push(sess, msgQueueReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartyQueueForMatchResponse{}); err != nil {
		return err
	}
	return s.pushPartyUpdateLocked(p)
}

func (s *Server) removeQueueLocked(sess *session, packet gcwire.Packet) error {
	var req gcproto.CMsgPartyRemoveFromQueue
	if err := proto.Unmarshal(packet.Body, &req); err != nil {
		return err
	}
	p := s.parties[sess.steamID]
	group := wire.MatchGroup(req.GetMatchGroup())
	if p == nil || p.leader != sess.steamID {
		return errors.New("not party leader")
	}
	if id := p.tickets[group]; id != "" {
		_ = s.mm.Cancel(id)
	}
	delete(p.tickets, group)
	delete(p.queuedAt, group)
	if err := s.push(sess, msgRemoveQueueReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartyRemoveFromQueueResponse{}); err != nil {
		return err
	}
	return s.pushPartyUpdateLocked(p)
}

func randomID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }

func (s *Server) pruneSessionsLocked(cutoff time.Time) {
	for id, sess := range s.sessions {
		if sess.lastSeen.Before(cutoff) {
			if sess.standbyTicket != "" {
				_ = s.mm.Cancel(sess.standbyTicket)
			}
			if p := s.parties[sess.steamID]; p != nil {
				delete(p.standby, sess.steamID)
				_ = s.pushPartyUpdateLocked(p)
			}
			delete(s.sessions, id)
			if sess.instanceID != "" {
				identity := string(sess.steamID)
				if sess.role == "server" {
					identity = sess.matchID
				}
				delete(s.instances, sess.role+"\x00"+identity+"\x00"+sess.instanceID)
			}
		}
	}
}
