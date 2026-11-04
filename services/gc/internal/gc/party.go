package gc

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/socache"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// MaxPartySize is what the stock party panel is built for.
const MaxPartySize = 6

// msgNotice is ours: a line of text the game shows the player in a dialog.
// Its body is a CMsgPartyChatMsg, whose msg field is the text.
const msgNotice uint32 = 51000

type pending struct {
	SteamID uint64
	Type    gcpb.TFPendingPartyMember_EType
	Inviter uint64
}

// Party is a group that queues together. Every online player is in one, a
// party of one included -- that is what the stock client expects.
type Party struct {
	ID       uint64
	Leader   uint64
	Members  []uint64
	Pending  []pending
	Criteria *gcpb.CTFGroupMatchCriteriaProto
	UIState  *gcpb.TFSyncedMMUIState
	// PlayerCriteria is each member's own settings.
	PlayerCriteria map[uint64]*gcpb.CTFPerPlayerMatchCriteriaProto
	// Queues is when the party entered each queue it stands in.
	Queues map[int32]time.Time
	// Standby is the members asking to be let into the party's match.
	Standby map[uint64]bool

	// inviteHolders is who has an invite object for this party, so stale ones
	// are destroyed.
	inviteHolders map[uint64]bool
}

func (p *Party) has(id uint64) bool { return slices.Contains(p.Members, id) }

func (p *Party) pendingFor(id uint64) (pending, bool) {
	for _, pm := range p.Pending {
		if pm.SteamID == id {
			return pm, true
		}
	}
	return pending{}, false
}

func (p *Party) dropPending(id uint64) bool {
	n := len(p.Pending)
	p.Pending = slices.DeleteFunc(p.Pending, func(pm pending) bool { return pm.SteamID == id })
	return len(p.Pending) != n
}

func partyKey(id uint64) string { return strconv.FormatUint(id, 10) }

// newParty puts a player in a party of their own, leaving whatever party
// they were in.
func (g *GC) newParty(leader uint64) *Party {
	pl := g.player(leader)
	if old := g.parties[pl.PartyID]; old != nil && old.has(leader) {
		g.removeFromParty(old, leader)
	}
	p := &Party{
		ID:             newID(),
		Leader:         leader,
		Members:        []uint64{leader},
		PlayerCriteria: map[uint64]*gcpb.CTFPerPlayerMatchCriteriaProto{},
		Queues:         map[int32]time.Time{},
		Standby:        map[uint64]bool{},
		inviteHolders:  map[uint64]bool{},
	}
	g.parties[p.ID] = p
	pl.PartyID = p.ID
	return p
}

// removeFromParty takes a member out. The party loses its queue places --
// it is not the party that queued any more -- and a party nobody is left in
// is gone.
func (g *GC) removeFromParty(p *Party, id uint64) {
	if !p.has(id) {
		return
	}
	p.Members = slices.DeleteFunc(p.Members, func(m uint64) bool { return m == id })
	delete(p.PlayerCriteria, id)
	delete(p.Standby, id)
	g.so.Delete(id, socache.TypeParty, partyKey(p.ID))
	if pl := g.online[id]; pl != nil && pl.PartyID == p.ID {
		pl.PartyID = 0
	}
	if len(p.Members) == 0 {
		g.disbandParty(p)
		return
	}
	if p.Leader == id {
		p.Leader = p.Members[0]
	}
	clear(p.Queues)
	g.publishParty(p)
}

func (g *GC) disbandParty(p *Party) {
	for _, m := range p.Members {
		g.so.Delete(m, socache.TypeParty, partyKey(p.ID))
	}
	for id := range p.inviteHolders {
		g.so.Delete(id, socache.TypePartyInvite, partyKey(p.ID))
	}
	delete(g.parties, p.ID)
}

// joinParty moves a player into a party.
func (g *GC) joinParty(p *Party, id uint64) error {
	if p.has(id) {
		return nil
	}
	if len(p.Members) >= MaxPartySize {
		return fmt.Errorf("that party is full")
	}
	pl := g.player(id)
	if old := g.parties[pl.PartyID]; old != nil {
		g.removeFromParty(old, id)
	}
	p.dropPending(id)
	p.Members = append(p.Members, id)
	pl.PartyID = p.ID
	clear(p.Queues)
	g.publishParty(p)
	return nil
}

// publishParty writes the party into every member's cache and its invites
// into every invitee's.
func (g *GC) publishParty(p *Party) {
	msg := g.partyProto(p)
	for _, m := range p.Members {
		g.so.Put(m, socache.TypeParty, partyKey(p.ID), msg)
	}

	want := map[uint64]bool{}
	for _, pm := range p.Pending {
		want[pm.SteamID] = true
		inv := &gcpb.CSOTFPartyInvite{
			GroupId: proto.Uint64(p.ID),
			Inviter: proto.Uint64(pm.Inviter),
		}
		if pm.Type == gcpb.TFPendingPartyMember_RequestedToJoin {
			inv.Type = gcpb.CSOTFPartyInvite_PENDING_JOIN_REQUEST.Enum()
			inv.Inviter = proto.Uint64(p.Leader)
		} else {
			inv.Type = gcpb.CSOTFPartyInvite_PENDING_INVITE.Enum()
		}
		for _, m := range p.Members {
			inv.Members = append(inv.Members, &gcpb.CSOTFPartyInvite_PartyMember{Steamid: proto.Uint64(m)})
		}
		g.so.Put(pm.SteamID, socache.TypePartyInvite, partyKey(p.ID), inv)
	}
	for id := range p.inviteHolders {
		if !want[id] {
			g.so.Delete(id, socache.TypePartyInvite, partyKey(p.ID))
		}
	}
	p.inviteHolders = want
	g.publishLooks(p)
}

func (g *GC) partyProto(p *Party) *gcpb.CSOTFParty {
	msg := &gcpb.CSOTFParty{
		PartyId:       proto.Uint64(p.ID),
		LeaderId:      proto.Uint64(p.Leader),
		GroupCriteria: p.Criteria,
		LeaderUiState: p.UIState,
	}
	var lobby *Lobby
	for _, m := range p.Members {
		rec := g.players.Get(m)
		member := &gcpb.CSOTFPartyMember{
			CompetitiveAccess: proto.Bool(true),
			Experience:        proto.Uint32(uint32(rec.XP())),
			BadgeLevel:        proto.Uint32(uint32(rec.Level())),
			PlayerCriteria:    p.PlayerCriteria[m],
			LobbyStandby:      proto.Bool(p.Standby[m]),
			Activity:          &gcpb.CSOTFPartyMember_Activity{Online: proto.Bool(g.isOnline(m))},
		}
		if l := g.lobbyOf(m); l != nil {
			member.Activity.LobbyId = proto.Uint64(l.ID)
			member.Activity.LobbyMatchGroup = gcpb.ETFMatchGroup(l.Group).Enum()
			if lobby == nil || m == p.Leader {
				lobby = l
			}
		}
		msg.MemberIds = append(msg.MemberIds, m)
		msg.Members = append(msg.Members, member)
	}
	if lobby != nil {
		msg.AssociatedLobbyId = proto.Uint64(lobby.ID)
		msg.AssociatedLobbyMatchGroup = gcpb.ETFMatchGroup(lobby.Group).Enum()
	}
	groups := make([]int32, 0, len(p.Queues))
	for grp := range p.Queues {
		groups = append(groups, grp)
	}
	slices.Sort(groups)
	for _, grp := range groups {
		msg.MatchmakingQueues = append(msg.MatchmakingQueues, &gcpb.CSOTFParty_QueueEntry{
			MatchGroup: gcpb.ETFMatchGroup(grp).Enum(),
			QueuedTime: proto.Uint32(uint32(p.Queues[grp].Unix())),
		})
	}
	for _, pm := range p.Pending {
		msg.PendingMembers = append(msg.PendingMembers, &gcpb.TFPendingPartyMember{
			Steamid: proto.Uint64(pm.SteamID),
			Type:    pm.Type.Enum(),
			Inviter: proto.Uint64(pm.Inviter),
		})
	}
	return msg
}

func (g *GC) isOnline(id uint64) bool {
	p := g.online[id]
	return p != nil && p.Online
}

// partyOf is the party a session's player is in, if the message names it.
// A message naming another party is refused: a player only acts on their own.
func (g *GC) partyOf(s *session.Session, named uint64) *Party {
	pl := g.online[s.SteamID]
	if pl == nil {
		return nil
	}
	p := g.parties[pl.PartyID]
	if p == nil || (named != 0 && named != p.ID) {
		return nil
	}
	return p
}

func (g *GC) partyChat(p *Party, typ gcpb.ETFPartyChatType, actor uint64, text string) {
	msg := &gcpb.CMsgPartyChatMsg{Type: typ.Enum(), Msg: proto.String(text)}
	if actor != 0 {
		msg.ActorId = proto.Uint64(actor)
	}
	for _, m := range p.Members {
		g.send(m, uint32(gcpb.ETFGCMsg_k_EMsgGCParty_ChatMsg), msg)
	}
}

// notice shows a player a message from the GC.
func (g *GC) notice(id uint64, text string) {
	g.send(id, msgNotice, &gcpb.CMsgPartyChatMsg{Msg: proto.String(text)})
}

// tickParties drops parties nobody has been online in for a while, and takes
// queue places away from parties with a member who went quiet.
func (g *GC) tickParties() {
	now := g.now()
	grace := g.cfg.Timing.OfflineGrace()
	for _, p := range g.parties {
		allGone := true
		for _, m := range p.Members {
			pl := g.online[m]
			if pl != nil && pl.Online {
				allGone = false
				continue
			}
			if len(p.Queues) > 0 && pl != nil && now.Sub(pl.OfflineSince) > grace {
				clear(p.Queues)
				clear(p.Standby)
				g.publishParty(p)
			}
		}
		if allGone && g.allOfflineLonger(p, 10*time.Minute) && g.partyLobby(p) == nil {
			for _, m := range p.Members {
				delete(g.online, m)
			}
			g.disbandParty(p)
		}
	}
}

func (g *GC) allOfflineLonger(p *Party, d time.Duration) bool {
	now := g.now()
	for _, m := range p.Members {
		pl := g.online[m]
		if pl != nil && (pl.Online || now.Sub(pl.OfflineSince) < d) {
			return false
		}
	}
	return true
}

// --- handlers ----------------------------------------------------------------

func init() {
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_SetOptions, (*GC).handleSetOptions)
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_InvitePlayer, (*GC).handleInvite)
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_RequestJoinPlayer, (*GC).handleRequestJoin)
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_ClearPendingPlayer, (*GC).handleClearPending)
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_ClearOtherPartyRequest, (*GC).handleClearOther)
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_PromoteToLeader, (*GC).handlePromote)
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_KickMember, (*GC).handleKick)
	onClient(gcpb.ETFGCMsg_k_EMsgGCParty_SendChat, (*GC).handleChat)
	onClient(gcpb.ETFGCMsg_k_EMsgGCExitMatchmaking, (*GC).handleExitMatchmaking)
}

func (g *GC) handleSetOptions(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartySetOptions
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCParty_SetOptionsResponse), &gcpb.CMsgPartySetOptionsResponse{})
	if !g.parse(m, &msg) {
		return
	}
	p := g.partyOf(s, msg.GetPartyId())
	if p == nil {
		return
	}
	opts := msg.GetOptions()
	if opts.GetGroupCriteria() != nil && s.SteamID == p.Leader {
		if opts.GetOverwriteExisting() || p.Criteria == nil {
			p.Criteria = proto.Clone(opts.GetGroupCriteria()).(*gcpb.CTFGroupMatchCriteriaProto)
		} else {
			proto.Merge(p.Criteria, opts.GetGroupCriteria())
		}
	}
	if opts.GetPlayerUistate() != nil && s.SteamID == p.Leader {
		p.UIState = proto.Clone(opts.GetPlayerUistate()).(*gcpb.TFSyncedMMUIState)
	}
	if opts.GetPlayerCriteria() != nil {
		p.PlayerCriteria[s.SteamID] = proto.Clone(opts.GetPlayerCriteria()).(*gcpb.CTFPerPlayerMatchCriteriaProto)
	}
	g.publishParty(p)
}

func (g *GC) handleInvite(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyInvitePlayer
	if !g.parse(m, &msg) {
		return
	}
	p := g.partyOf(s, msg.GetPartyId())
	target := msg.GetPlayerId()
	if p == nil || s.SteamID != p.Leader || target == s.SteamID || !steamauth.ValidIndividual(target) {
		return
	}
	// Inviting somebody who asked to join is accepting them.
	if pm, ok := p.pendingFor(target); ok && pm.Type == gcpb.TFPendingPartyMember_RequestedToJoin {
		if err := g.joinParty(p, target); err != nil {
			g.notice(s.SteamID, err.Error())
		}
		return
	}
	if p.has(target) {
		return
	}
	if len(p.Members)+len(p.Pending) >= MaxPartySize {
		g.notice(s.SteamID, "Your party is full.")
		return
	}
	p.dropPending(target)
	p.Pending = append(p.Pending, pending{SteamID: target, Type: gcpb.TFPendingPartyMember_Invited, Inviter: s.SteamID})
	g.publishParty(p)
}

func (g *GC) handleRequestJoin(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyRequestJoinPlayer
	if !g.parse(m, &msg) {
		return
	}
	target := g.online[msg.GetJoinPlayerId()]
	if target == nil || msg.GetJoinPlayerId() == s.SteamID {
		g.notice(s.SteamID, "That player is not online in Team Frontress right now.")
		return
	}
	p := g.parties[target.PartyID]
	if p == nil || (msg.GetJoinPartyId() != 0 && msg.GetJoinPartyId() != p.ID) {
		return
	}
	if p.has(s.SteamID) {
		return
	}
	// Asking to join a party that invited you is accepting the invite.
	if pm, ok := p.pendingFor(s.SteamID); ok && pm.Type == gcpb.TFPendingPartyMember_Invited {
		if err := g.joinParty(p, s.SteamID); err != nil {
			g.notice(s.SteamID, err.Error())
		}
		return
	}
	if len(p.Members)+len(p.Pending) >= MaxPartySize {
		g.notice(s.SteamID, "That party is full.")
		return
	}
	p.dropPending(s.SteamID)
	p.Pending = append(p.Pending, pending{SteamID: s.SteamID, Type: gcpb.TFPendingPartyMember_RequestedToJoin, Inviter: s.SteamID})
	g.publishParty(p)
}

func (g *GC) handleClearPending(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyClearPendingPlayer
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCParty_ClearPendingPlayerResponse), &gcpb.CMsgPartyClearPendingPlayerResponse{})
	if !g.parse(m, &msg) {
		return
	}
	p := g.partyOf(s, msg.GetPartyId())
	if p == nil || s.SteamID != p.Leader {
		return
	}
	if p.dropPending(msg.GetPendingPlayerId()) {
		g.publishParty(p)
	}
}

func (g *GC) handleClearOther(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyClearOtherPartyRequest
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGCParty_ClearOtherPartyRequestResponse), &gcpb.CMsgPartyClearOtherPartyRequestResponse{})
	if !g.parse(m, &msg) {
		return
	}
	p := g.parties[msg.GetOtherPartyId()]
	if p == nil {
		return
	}
	if p.dropPending(s.SteamID) {
		g.publishParty(p)
	}
}

func (g *GC) handlePromote(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyPromoteToLeader
	if !g.parse(m, &msg) {
		return
	}
	p := g.partyOf(s, msg.GetPartyId())
	if p == nil || s.SteamID != p.Leader || !p.has(msg.GetNewLeaderId()) {
		return
	}
	p.Leader = msg.GetNewLeaderId()
	g.publishParty(p)
}

func (g *GC) handleKick(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyKickMember
	if !g.parse(m, &msg) {
		return
	}
	p := g.partyOf(s, msg.GetPartyId())
	target := msg.GetTargetId()
	if p == nil || s.SteamID != p.Leader || target == s.SteamID || !p.has(target) {
		return
	}
	g.newParty(target) // leaves p
	if np := g.parties[g.player(target).PartyID]; np != nil {
		g.publishParty(np)
	}
}

func (g *GC) handleChat(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartySendChat
	if !g.parse(m, &msg) {
		return
	}
	p := g.partyOf(s, msg.GetPartyId())
	text := msg.GetMsg()
	if p == nil || text == "" {
		return
	}
	if len(text) > 512 {
		text = text[:512]
	}
	g.partyChat(p, gcpb.ETFPartyChatType_k_eTFPartyChatType_MemberChat, s.SteamID, text)
}

// handleExitMatchmaking is the client leaving: its match (lobby_id), its party
// (party_id), or both.
func (g *GC) handleExitMatchmaking(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgExitMatchmaking
	if !g.parse(m, &msg) {
		return
	}
	if lid := msg.GetLobbyId(); lid != 0 {
		if l := g.lobbies[lid]; l != nil {
			g.leaveLobby(l, s.SteamID, msg.GetExplicitAbandon())
		}
	}
	if pid := msg.GetPartyId(); pid != 0 {
		if p := g.parties[pid]; p != nil && p.has(s.SteamID) && len(p.Members) > 1 {
			np := g.newParty(s.SteamID)
			g.publishParty(np)
		}
	}
}

// --- restrictions -------------------------------------------------------------

// admissible checks a party against a group's restrictions. The error is
// player-facing.
func (g *GC) admissible(p *Party, grp config.MatchGroupConfig) error {
	r := grp.Restrictions
	if n := len(p.Members); n > grp.PartyCap() {
		return fmt.Errorf("%s takes parties of at most %d; yours has %d", grp.Name, grp.PartyCap(), n)
	}
	if r.MinPartySize > 0 && len(p.Members) < r.MinPartySize {
		return fmt.Errorf("%s needs a party of at least %d", grp.Name, r.MinPartySize)
	}
	now := g.now()
	for _, id := range p.Members {
		pl := g.online[id]
		if pl == nil || !pl.Online {
			return fmt.Errorf("everybody in the party has to be online")
		}
		if g.lobbyOf(id) != nil {
			return fmt.Errorf("somebody in the party is still in a match")
		}
		if r.RequireVerifiedAuth && !pl.Verified {
			return fmt.Errorf("%s needs a verified Steam login", grp.Name)
		}
		sid := strconv.FormatUint(id, 10)
		if slices.Contains(r.BannedSteamIDs, sid) {
			return fmt.Errorf("a member of the party may not play %s", grp.Name)
		}
		if len(r.AllowedSteamIDs) > 0 && !slices.Contains(r.AllowedSteamIDs, sid) {
			return fmt.Errorf("%s is invite-only and a member of the party is not on the list", grp.Name)
		}
		rec := g.players.Get(id)
		if r.MinMatchesPlayed > 0 && rec.Matches < r.MinMatchesPlayed {
			return fmt.Errorf("%s needs %d finished matches; a member of the party has %d", grp.Name, r.MinMatchesPlayed, rec.Matches)
		}
		if r.MaxAbandons > 0 && rec.Abandons >= r.MaxAbandons {
			return fmt.Errorf("a member of the party has abandoned too many matches to play %s", grp.Name)
		}
		if r.AbandonCooldownMins > 0 && !rec.LastAbandon.IsZero() {
			until := rec.LastAbandon.Add(time.Duration(r.AbandonCooldownMins) * time.Minute)
			if now.Before(until) {
				return fmt.Errorf("a member of the party abandoned a match and can queue again in %d minutes",
					int(until.Sub(now).Minutes())+1)
			}
		}
	}
	return nil
}
