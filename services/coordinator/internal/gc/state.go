package gc

import (
	"strconv"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/gcwire"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
	"google.golang.org/protobuf/proto"
)

func (s *Server) push(sess *session, typ uint32, header *gcproto.CMsgProtoBufHeader, body proto.Message) error {
	m, err := gcwire.Encode(typ, header, body)
	if err == nil {
		sess.out = append(sess.out, m)
	}
	return err
}

func (s *Server) partyProto(p *party) (*gcproto.CSOTFParty, error) {
	out := &gcproto.CSOTFParty{PartyId: proto.Uint64(p.id), GroupCriteria: p.criteria}
	for _, id := range p.members {
		u, err := strconv.ParseUint(string(id), 10, 64)
		if err != nil {
			return nil, err
		}
		out.MemberIds = append(out.MemberIds, u)
		out.Members = append(out.Members, &gcproto.CSOTFPartyMember{CompetitiveAccess: proto.Bool(true), LobbyStandby: proto.Bool(p.standby[id])})
	}
	leader, _ := strconv.ParseUint(string(p.leader), 10, 64)
	out.LeaderId = proto.Uint64(leader)
	if p.lobbyID != 0 {
		out.AssociatedLobbyId = proto.Uint64(p.lobbyID)
		g := gcproto.ETFMatchGroup(p.lobbyGroup)
		out.AssociatedLobbyMatchGroup = &g
	}
	for group := range p.tickets {
		g := gcproto.ETFMatchGroup(group)
		out.MatchmakingQueues = append(out.MatchmakingQueues, &gcproto.CSOTFParty_QueueEntry{MatchGroup: &g, QueuedTime: proto.Uint32(p.queuedAt[group])})
	}
	for id, pendingType := range p.pending {
		u, err := strconv.ParseUint(string(id), 10, 64)
		if err != nil {
			return nil, err
		}
		t := pendingType
		out.PendingMembers = append(out.PendingMembers, &gcproto.TFPendingPartyMember{Steamid: proto.Uint64(u), Type: &t})
	}
	return out, nil
}

func subscribed(owner uint64, version uint64, typ int32, objects ...proto.Message) (*gcproto.CMsgSOCacheSubscribed, error) {
	entry := &gcproto.CMsgSOCacheSubscribed_SubscribedType{TypeId: proto.Int32(typ)}
	for _, object := range objects {
		b, err := proto.Marshal(object)
		if err != nil {
			return nil, err
		}
		entry.ObjectData = append(entry.ObjectData, b)
	}
	return &gcproto.CMsgSOCacheSubscribed{Owner: proto.Uint64(owner), Version: proto.Uint64(version), Objects: []*gcproto.CMsgSOCacheSubscribed_SubscribedType{entry}}, nil
}

func (s *Server) pushCacheLocked(sess *session) error {
	p := s.parties[sess.steamID]
	party, err := s.partyProto(p)
	if err != nil {
		return err
	}
	owner, _ := strconv.ParseUint(string(sess.steamID), 10, 64)
	sess.version++
	cache, err := subscribed(owner, sess.version, partyType, party)
	if err != nil {
		return err
	}
	if err := s.push(sess, msgCacheSubscribed, nil, cache); err != nil {
		return err
	}
	if len(s.invites[sess.steamID]) == 0 {
		return nil
	}
	objects := make([]proto.Message, 0, len(s.invites[sess.steamID]))
	for partyID, invite := range s.invites[sess.steamID] {
		if p := s.partyByIDLocked(partyID); p != nil {
			objects = append(objects, s.inviteProtoLocked(p, invite))
		}
	}
	if len(objects) == 0 {
		return nil
	}
	sess.version++
	inviteCache, err := subscribed(owner, sess.version, partyInviteType, objects...)
	if err != nil {
		return err
	}
	return s.push(sess, msgCacheSubscribed, nil, inviteCache)
}

func (s *Server) pushSO(sess *session, messageType uint32, objectType int32, object proto.Message) error {
	b, err := proto.Marshal(object)
	if err != nil {
		return err
	}
	sess.version++
	owner, _ := strconv.ParseUint(string(sess.steamID), 10, 64)
	obj := &gcproto.CMsgSOSingleObject{Owner: proto.Uint64(owner), TypeId: proto.Int32(objectType), ObjectData: b, Version: proto.Uint64(sess.version)}
	return s.push(sess, messageType, nil, obj)
}

func (s *Server) pushPartyObjectLocked(sess *session, p *party, messageType uint32) error {
	party, err := s.partyProto(p)
	if err != nil {
		return err
	}
	return s.pushSO(sess, messageType, partyType, party)
}

func (s *Server) pushPartyUpdateLocked(p *party) error {
	party, err := s.partyProto(p)
	if err != nil {
		return err
	}
	for _, sess := range s.sessions {
		if sess.role != "client" {
			continue
		}
		member := false
		for _, id := range p.members {
			if id == sess.steamID {
				member = true
				break
			}
		}
		if !member {
			continue
		}
		if err := s.pushSO(sess, msgSOUpdate, partyType, party); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) syncLocked(sess *session) {
	if sess.role == "server" {
		s.syncServerLocked(sess)
		return
	}
	if sess.role != "client" {
		return
	}
	p := s.parties[sess.steamID]
	if p == nil {
		return
	}
	tickets := make(map[wire.MatchGroup]string, len(p.tickets)+1)
	for group, ticketID := range p.tickets {
		tickets[group] = ticketID
	}
	if sess.standbyTicket != "" {
		tickets[sess.standbyGroup] = sess.standbyTicket
	}
	for group, ticketID := range tickets {
		status, err := s.mm.Status(ticketID)
		if err != nil {
			continue
		}
		if status.State != wire.QueueStateAssigned || status.Assignment == nil {
			continue
		}
		newLobbyID := lobbyID(status.Assignment.MatchID)
		if p.lobbyID != newLobbyID || p.lobbyGroup != group || p.lobbyMatchID != status.Assignment.MatchID {
			p.lobbyID = newLobbyID
			p.lobbyGroup = group
			p.lobbyMatchID = status.Assignment.MatchID
			_ = s.pushPartyUpdateLocked(p)
		}
		if !assignmentContains(status.Assignment, sess.steamID) || !s.ready[status.Assignment.MatchID] {
			continue
		}
		lobby := lobbyProto(status.Assignment, gcproto.CSOTFGameServerLobby_RUN, true)
		if lobby.GetLobbyId() == sess.lobbyID {
			continue
		}
		sess.lobbyID = lobby.GetLobbyId()
		sess.version++
		b, err := proto.Marshal(lobby)
		if err != nil {
			continue
		}
		owner, _ := strconv.ParseUint(string(sess.steamID), 10, 64)
		obj := &gcproto.CMsgSOSingleObject{Owner: proto.Uint64(owner), TypeId: proto.Int32(lobbyType), ObjectData: b, Version: proto.Uint64(sess.version)}
		_ = s.push(sess, msgSOCreate, nil, obj)
		if ticketID == sess.standbyTicket {
			sess.standbyTicket = ""
			delete(p.standby, sess.steamID)
			_ = s.pushPartyUpdateLocked(p)
		}
	}
}

func assignmentContains(a *wire.Assignment, id wire.SteamID) bool {
	for _, player := range a.Roster {
		if player.SteamID == id {
			return true
		}
	}
	return false
}

func (s *Server) finishMatchLocked(matchID string, assignment *wire.Assignment) {
	for _, p := range s.parties {
		if p == nil || p.lobbyMatchID != matchID {
			continue
		}
		if assignment != nil {
			lobby := lobbyProto(assignment, gcproto.CSOTFGameServerLobby_RUN, true)
			for _, sess := range s.sessions {
				if sess.role == "client" && partyContains(p, sess.steamID) && sess.lobbyID == p.lobbyID {
					_ = s.pushSO(sess, msgSODestroy, lobbyType, lobby)
					sess.lobbyID = 0
				}
			}
		}
		delete(p.tickets, p.lobbyGroup)
		delete(p.queuedAt, p.lobbyGroup)
		p.lobbyID = 0
		p.lobbyMatchID = ""
		_ = s.pushPartyUpdateLocked(p)
	}
}

func (s *Server) pushServerCacheLocked(sess *session) error {
	a, ok := s.mm.MatchAssignment(sess.matchID)
	if !ok {
		return nil
	}
	lobby := lobbyProto(a, gcproto.CSOTFGameServerLobby_SERVERSETUP, false)
	b, _ := proto.Marshal(lobby)
	sess.lobbyHash = string(b)
	sess.assignmentHash = string(b)
	cache, err := subscribed(sess.serverID, 1, lobbyType, lobby)
	if err != nil {
		return err
	}
	return s.push(sess, msgCacheSubscribed, nil, cache)
}

func (s *Server) syncServerLocked(sess *session) {
	a, ok := s.mm.MatchAssignment(sess.matchID)
	if !ok {
		return
	}
	setup := lobbyProto(a, gcproto.CSOTFGameServerLobby_SERVERSETUP, false)
	setupBytes, err := proto.Marshal(setup)
	if err != nil {
		return
	}
	if string(setupBytes) != sess.assignmentHash {
		sess.assignmentHash = string(setupBytes)
		s.ready[sess.matchID] = false
	}
	state := gcproto.CSOTFGameServerLobby_SERVERSETUP
	if s.ready[sess.matchID] {
		state = gcproto.CSOTFGameServerLobby_RUN
	}
	lobby := lobbyProto(a, state, s.ready[sess.matchID])
	b, err := proto.Marshal(lobby)
	if err != nil || string(b) == sess.lobbyHash {
		return
	}
	sess.lobbyHash = string(b)
	sess.version++
	obj := &gcproto.CMsgSOSingleObject{Owner: proto.Uint64(sess.serverID), TypeId: proto.Int32(lobbyType), ObjectData: b, Version: proto.Uint64(sess.version)}
	_ = s.push(sess, msgSOUpdate, nil, obj)
}

func lobbyProto(a *wire.Assignment, state gcproto.CSOTFGameServerLobby_State, acknowledged bool) *gcproto.CSOTFGameServerLobby {
	id := lobbyID(a.MatchID)
	group := uint32(a.MatchGroup)
	size := uint32(len(a.Roster))
	l := &gcproto.CSOTFGameServerLobby{LobbyId: proto.Uint64(id), MatchId: proto.Uint64(id), MatchGroup: &group, MapName: proto.String(a.Map), Connect: proto.String(a.Connect), State: &state, FixedMatchSize: &size}
	for _, player := range a.Roster {
		steamID, _ := strconv.ParseUint(string(player.SteamID), 10, 64)
		team := gcproto.TF_GC_TEAM_TF_GC_TEAM_DEFENDERS
		if player.Team == wire.TeamBlu {
			team = gcproto.TF_GC_TEAM_TF_GC_TEAM_INVADERS
		}
		connect := gcproto.CTFLobbyPlayerProto_RESERVATION_PENDING
		if acknowledged {
			connect = gcproto.CTFLobbyPlayerProto_RESERVED
		}
		typ := gcproto.CTFLobbyPlayerProto_MATCH_PLAYER
		l.Members = append(l.Members, &gcproto.CTFLobbyPlayerProto{Id: &steamID, Name: proto.String(player.Name), Team: &team, ConnectState: &connect, Type: &typ})
	}
	return l
}

func lobbyID(matchID string) uint64 {
	id, _ := strconv.ParseUint(matchID, 16, 64)
	if id == 0 {
		id, _ = strconv.ParseUint(matchID, 10, 64)
	}
	return id
}
