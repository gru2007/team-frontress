package gc

import (
	"errors"
	"strconv"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/gcwire"
	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
	"google.golang.org/protobuf/proto"
)

func (s *Server) partyByIDLocked(id uint64) *party {
	for _, p := range s.parties {
		if p != nil && p.id == id {
			return p
		}
	}
	return nil
}

func partyContains(p *party, id wire.SteamID) bool {
	for _, member := range p.members {
		if member == id {
			return true
		}
	}
	return false
}

func (s *Server) invitePlayerLocked(sess *session, packet gcwire.Packet) error {
	var req gcproto.CMsgPartyInvitePlayer
	if err := proto.Unmarshal(packet.Body, &req); err != nil {
		return err
	}
	p := s.parties[sess.steamID]
	target := wire.SteamID(strconv.FormatUint(req.GetPlayerId(), 10))
	if p == nil || p.leader != sess.steamID || (req.GetPartyId() != 0 && req.GetPartyId() != p.id) || target == sess.steamID {
		return errors.New("invalid party invite")
	}
	if req.GetExpectingRequestToJoin() {
		if p.pending[target] != gcproto.TFPendingPartyMember_RequestedToJoin {
			return nil
		}
		return s.joinPartyLocked(target, p)
	}
	p.pending[target] = gcproto.TFPendingPartyMember_Invited
	s.setInviteLocked(target, p, sess.steamID, gcproto.CSOTFPartyInvite_PENDING_INVITE)
	return s.pushPartyUpdateLocked(p)
}

func (s *Server) requestJoinLocked(sess *session, packet gcwire.Packet) error {
	var req gcproto.CMsgPartyRequestJoinPlayer
	if err := proto.Unmarshal(packet.Body, &req); err != nil {
		return err
	}
	var p *party
	if req.GetJoinPartyId() != 0 {
		p = s.partyByIDLocked(req.GetJoinPartyId())
	} else if req.GetJoinPlayerId() != 0 {
		p = s.parties[wire.SteamID(strconv.FormatUint(req.GetJoinPlayerId(), 10))]
	}
	if p == nil || partyContains(p, sess.steamID) {
		return nil
	}
	if req.GetExpectingInvite() {
		if p.pending[sess.steamID] != gcproto.TFPendingPartyMember_Invited {
			return nil
		}
		return s.joinPartyLocked(sess.steamID, p)
	}
	p.pending[sess.steamID] = gcproto.TFPendingPartyMember_RequestedToJoin
	s.setInviteLocked(sess.steamID, p, p.leader, gcproto.CSOTFPartyInvite_PENDING_JOIN_REQUEST)
	return s.pushPartyUpdateLocked(p)
}

func (s *Server) sendChatLocked(sess *session, packet gcwire.Packet) error {
	var req gcproto.CMsgPartySendChat
	if err := proto.Unmarshal(packet.Body, &req); err != nil {
		return err
	}
	p := s.parties[sess.steamID]
	if p == nil || req.GetPartyId() != p.id || req.GetMsg() == "" {
		return nil
	}
	actor, _ := strconv.ParseUint(string(sess.steamID), 10, 64)
	typ := gcproto.ETFPartyChatType_k_eTFPartyChatType_MemberChat
	chat := &gcproto.CMsgPartyChatMsg{Type: &typ, ActorId: proto.Uint64(actor), Msg: proto.String(req.GetMsg())}
	for _, client := range s.sessions {
		if client.role == "client" && partyContains(p, client.steamID) {
			if err := s.push(client, msgChat, nil, chat); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) queueStandbyLocked(sess *session, packet gcwire.Packet) error {
	var req gcproto.CMsgPartyQueueForStandby
	if err := proto.Unmarshal(packet.Body, &req); err != nil {
		return err
	}
	p := s.parties[sess.steamID]
	if p == nil || req.GetPartyId() != p.id || p.lobbyMatchID == "" || (req.GetPartyLobbyId() != 0 && req.GetPartyLobbyId() != p.lobbyID) {
		return errors.New("party has no active match")
	}
	if sess.standbyTicket != "" {
		_ = s.mm.Cancel(sess.standbyTicket)
	}
	t, err := s.mm.Enqueue(&mm.Ticket{
		MatchGroup:     p.lobbyGroup,
		Leader:         sess.steamID,
		Players:        []wire.AssignedPlayer{{SteamID: sess.steamID}},
		LateJoinOK:     true,
		StandbyMatchID: p.lobbyMatchID,
	})
	if err != nil {
		s.log.Info("GC standby request refused", "player", sess.steamID, "match", p.lobbyMatchID, "err", err)
		return s.push(sess, msgQueueStandbyReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartyQueueForStandbyResponse{})
	}
	sess.standbyTicket = t.ID
	sess.standbyGroup = p.lobbyGroup
	p.standby[sess.steamID] = true
	if err := s.push(sess, msgQueueStandbyReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartyQueueForStandbyResponse{}); err != nil {
		return err
	}
	return s.pushPartyUpdateLocked(p)
}

func (s *Server) clearPendingLocked(sess *session, packet gcwire.Packet) error {
	var req gcproto.CMsgPartyClearPendingPlayer
	if err := proto.Unmarshal(packet.Body, &req); err != nil {
		return err
	}
	p := s.parties[sess.steamID]
	if p == nil || p.leader != sess.steamID || req.GetPartyId() != p.id {
		return errors.New("not party leader")
	}
	target := wire.SteamID(strconv.FormatUint(req.GetPendingPlayerId(), 10))
	delete(p.pending, target)
	s.removeInviteLocked(target, p.id)
	if err := s.push(sess, msgClearPendingReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartyClearPendingPlayerResponse{}); err != nil {
		return err
	}
	return s.pushPartyUpdateLocked(p)
}

func (s *Server) clearOtherLocked(sess *session, packet gcwire.Packet) error {
	var req gcproto.CMsgPartyClearOtherPartyRequest
	if err := proto.Unmarshal(packet.Body, &req); err != nil {
		return err
	}
	if p := s.partyByIDLocked(req.GetOtherPartyId()); p != nil {
		delete(p.pending, sess.steamID)
		_ = s.pushPartyUpdateLocked(p)
	}
	s.removeInviteLocked(sess.steamID, req.GetOtherPartyId())
	return s.push(sess, msgClearOtherReply, gcwire.ReplyHeader(packet.Header), &gcproto.CMsgPartyClearOtherPartyRequestResponse{})
}

func (s *Server) promoteLocked(sess *session, packet gcwire.Packet) error {
	var req gcproto.CMsgPartyPromoteToLeader
	if err := proto.Unmarshal(packet.Body, &req); err != nil {
		return err
	}
	p := s.parties[sess.steamID]
	newLeader := wire.SteamID(strconv.FormatUint(req.GetNewLeaderId(), 10))
	if p == nil || p.leader != sess.steamID || req.GetPartyId() != p.id || !partyContains(p, newLeader) {
		return errors.New("invalid party promotion")
	}
	p.leader = newLeader
	return s.pushPartyUpdateLocked(p)
}

func (s *Server) kickLocked(sess *session, packet gcwire.Packet) error {
	var req gcproto.CMsgPartyKickMember
	if err := proto.Unmarshal(packet.Body, &req); err != nil {
		return err
	}
	p := s.parties[sess.steamID]
	target := wire.SteamID(strconv.FormatUint(req.GetTargetId(), 10))
	if p == nil || p.leader != sess.steamID || req.GetPartyId() != p.id || target == p.leader || !partyContains(p, target) {
		return errors.New("invalid party kick")
	}
	return s.removeMemberLocked(target, p)
}

func (s *Server) joinPartyLocked(id wire.SteamID, target *party) error {
	old := s.parties[id]
	if old != nil && old != target {
		if old.leader == id && len(old.members) > 1 {
			// A leader joining another group disbands their old party. This keeps
			// every remaining client on a valid singleton object with a unique key.
			members := append([]wire.SteamID(nil), old.members...)
			for _, member := range members {
				if err := s.replaceWithSingletonLocked(member, old); err != nil {
					return err
				}
			}
		} else if err := s.removeMemberLocked(id, old); err != nil {
			return err
		}
	}
	if current := s.parties[id]; current != nil && current != target {
		for _, sess := range s.sessions {
			if sess.role == "client" && sess.steamID == id {
				if err := s.pushPartyObjectLocked(sess, current, msgSODestroy); err != nil {
					return err
				}
			}
		}
	}
	if !partyContains(target, id) {
		target.members = append(target.members, id)
	}
	s.parties[id] = target
	for _, sess := range s.sessions {
		if sess.role == "client" && sess.steamID == id {
			if err := s.pushPartyObjectLocked(sess, target, msgSOCreate); err != nil {
				return err
			}
		}
	}
	delete(target.pending, id)
	s.removeInviteLocked(id, target.id)
	return s.pushPartyUpdateLocked(target)
}

func (s *Server) removeMemberLocked(id wire.SteamID, p *party) error {
	delete(p.standby, id)
	for i, member := range p.members {
		if member == id {
			p.members = append(p.members[:i], p.members[i+1:]...)
			break
		}
	}
	if p.leader == id && len(p.members) > 0 {
		p.leader = p.members[0]
	}
	if err := s.replaceWithSingletonLocked(id, p); err != nil {
		return err
	}
	return s.pushPartyUpdateLocked(p)
}

func (s *Server) replaceWithSingletonLocked(id wire.SteamID, old *party) error {
	for _, sess := range s.sessions {
		if sess.role == "client" && sess.steamID == id {
			if err := s.pushPartyObjectLocked(sess, old, msgSODestroy); err != nil {
				return err
			}
		}
	}
	u, _ := strconv.ParseUint(string(id), 10, 64)
	solo := &party{id: u, leader: id, members: []wire.SteamID{id}, tickets: map[wire.MatchGroup]string{}, queuedAt: map[wire.MatchGroup]uint32{}, pending: map[wire.SteamID]gcproto.TFPendingPartyMember_EType{}, standby: map[wire.SteamID]bool{}}
	s.parties[id] = solo
	for _, sess := range s.sessions {
		if sess.role == "client" && sess.steamID == id {
			if err := s.pushPartyObjectLocked(sess, solo, msgSOCreate); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) inviteProtoLocked(p *party, invite *partyInvite) *gcproto.CSOTFPartyInvite {
	out := &gcproto.CSOTFPartyInvite{GroupId: proto.Uint64(p.id), Type: &invite.typ}
	inviter, _ := strconv.ParseUint(string(invite.inviter), 10, 64)
	out.Inviter = proto.Uint64(inviter)
	for _, id := range p.members {
		u, _ := strconv.ParseUint(string(id), 10, 64)
		out.Members = append(out.Members, &gcproto.CSOTFPartyInvite_PartyMember{Steamid: proto.Uint64(u)})
	}
	return out
}

func (s *Server) setInviteLocked(owner wire.SteamID, p *party, inviter wire.SteamID, typ gcproto.CSOTFPartyInvite_Type) {
	if s.invites[owner] == nil {
		s.invites[owner] = map[uint64]*partyInvite{}
	}
	invite := &partyInvite{partyID: p.id, inviter: inviter, typ: typ}
	s.invites[owner][p.id] = invite
	obj := s.inviteProtoLocked(p, invite)
	for _, sess := range s.sessions {
		if sess.role == "client" && sess.steamID == owner {
			_ = s.pushSO(sess, msgSOCreate, partyInviteType, obj)
		}
	}
}

func (s *Server) removeInviteLocked(owner wire.SteamID, partyID uint64) {
	invite := s.invites[owner][partyID]
	if invite == nil {
		return
	}
	p := s.partyByIDLocked(partyID)
	if p != nil {
		obj := s.inviteProtoLocked(p, invite)
		for _, sess := range s.sessions {
			if sess.role == "client" && sess.steamID == owner {
				_ = s.pushSO(sess, msgSODestroy, partyInviteType, obj)
			}
		}
	}
	delete(s.invites[owner], partyID)
}
