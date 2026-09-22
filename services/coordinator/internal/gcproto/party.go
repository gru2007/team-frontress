package gcproto

// This file implements the party shared objects and criteria types, field
// numbers confirmed against src/game/shared/tf/tf_gcmessages.proto. Fields
// marked deprecated/commented out in the real .proto are intentionally
// omitted here -- they are dead on the wire in the real game too.

// ETFMatchGroup mirrors the proto enum of the same name. Values match
// wire.MatchGroup already defined in this coordinator's internal/wire
// package; gcparty converts between the two at the boundary rather than
// importing wire into gcproto.
type ETFMatchGroup int32

const (
	MatchGroupInvalid     ETFMatchGroup = -1
	MatchGroupMvMPractice ETFMatchGroup = 0
	MatchGroupMvMMannUp   ETFMatchGroup = 1
	MatchGroupLadder6v6   ETFMatchGroup = 2
	MatchGroupLadder9v9   ETFMatchGroup = 3
	MatchGroupLadder12v12 ETFMatchGroup = 4
	MatchGroupCasual6v6   ETFMatchGroup = 5
	MatchGroupCasual9v9   ETFMatchGroup = 6
	MatchGroupCasual12v12 ETFMatchGroup = 7
)

// CasualMatchCriteria is CTFCasualMatchCriteria.
type CasualMatchCriteria struct {
	SelectedMapsBits []uint32 // repeated fixed32 = 3
}

func (m *CasualMatchCriteria) Marshal() []byte {
	w := NewWriter()
	RepeatedFixed32(w, 3, m.SelectedMapsBits)
	return w.Bytes()
}

func unmarshalCasualMatchCriteria(data []byte) (*CasualMatchCriteria, error) {
	m := &CasualMatchCriteria{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		if field == 3 {
			v, err := r.Fixed32()
			if err != nil {
				return nil, err
			}
			m.SelectedMapsBits = append(m.SelectedMapsBits, v)
			continue
		}
		if err := r.Skip(wt); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// PerPlayerMatchCriteria is CTFPerPlayerMatchCriteriaProto.
type PerPlayerMatchCriteria struct {
	MvMSquadSurplus *bool // bool = 1
}

func (m *PerPlayerMatchCriteria) Marshal() []byte {
	w := NewWriter()
	w.OptBool(1, m.MvMSquadSurplus)
	return w.Bytes()
}

func unmarshalPerPlayerMatchCriteria(data []byte) (*PerPlayerMatchCriteria, error) {
	m := &PerPlayerMatchCriteria{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		if field == 1 {
			v, err := r.Bool()
			if err != nil {
				return nil, err
			}
			m.MvMSquadSurplus = &v
			continue
		}
		if err := r.Skip(wt); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// GroupMatchCriteria is CTFGroupMatchCriteriaProto.
type GroupMatchCriteria struct {
	LateJoinOK         *bool                 // bool   = 5
	CustomPingTolerance *uint32              // uint32 = 13, default 0
	MvMMannupTour      *string               // string = 10
	MvMMannupMissions  []string              // repeated string = 15
	MvMBootcampMissions []string             // repeated string = 16
	CasualCriteria     *CasualMatchCriteria  // = 12
}

func (m *GroupMatchCriteria) Marshal() []byte {
	w := NewWriter()
	w.OptBool(5, m.LateJoinOK)
	w.OptUint32(13, m.CustomPingTolerance)
	w.OptString(10, m.MvMMannupTour)
	RepeatedString(w, 15, m.MvMMannupMissions)
	RepeatedString(w, 16, m.MvMBootcampMissions)
	OptMessage(w, 12, m.CasualCriteria)
	return w.Bytes()
}

func UnmarshalGroupMatchCriteria(data []byte) (*GroupMatchCriteria, error) {
	m := &GroupMatchCriteria{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 5:
			v, err := r.Bool()
			if err != nil {
				return nil, err
			}
			m.LateJoinOK = &v
		case 13:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.CustomPingTolerance = &v
		case 10:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			m.MvMMannupTour = &v
		case 15:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			m.MvMMannupMissions = append(m.MvMMannupMissions, v)
		case 16:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			m.MvMBootcampMissions = append(m.MvMBootcampMissions, v)
		case 12:
			b, err := r.Bytes()
			if err != nil {
				return nil, err
			}
			sub, err := unmarshalCasualMatchCriteria(b)
			if err != nil {
				return nil, err
			}
			m.CasualCriteria = sub
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// ETFSyncedMMMenuStep mirrors the proto enum.
type ETFSyncedMMMenuStep int32

const (
	MenuStepInvalid                 ETFSyncedMMMenuStep = -1
	MenuStepNone                    ETFSyncedMMMenuStep = 0
	MenuStepConfiguringMode         ETFSyncedMMMenuStep = 1
	MenuStepMvMSelectingMode        ETFSyncedMMMenuStep = 2
	MenuStepMvMSelectingTour        ETFSyncedMMMenuStep = 3
	MenuStepMvMSelectingMissions    ETFSyncedMMMenuStep = 4
)

// SyncedMMUIState is TFSyncedMMUIState -- what the party leader's UI is
// currently doing, synced to other members so their menu can follow along.
type SyncedMMUIState struct {
	MenuStep   *int32 // enum ETFSyncedMMMenuStep = 1, default None(0)
	MatchGroup *int32 // enum ETFMatchGroup = 2, default Invalid(-1)
}

func (m *SyncedMMUIState) Marshal() []byte {
	w := NewWriter()
	w.OptEnum(1, m.MenuStep)
	w.OptEnum(2, m.MatchGroup)
	return w.Bytes()
}

func unmarshalSyncedMMUIState(data []byte) (*SyncedMMUIState, error) {
	m := &SyncedMMUIState{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			m.MenuStep = &v
		case 2:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			m.MatchGroup = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// PartyOptions is CTFPartyOptions -- the message clients send to describe
// (and, per overwrite_existing, replace or merge into) their party's
// matchmaking criteria.
type PartyOptions struct {
	OverwriteExisting *bool                   // bool = 1
	GroupCriteria     *GroupMatchCriteria     // = 2
	PlayerCriteria    *PerPlayerMatchCriteria // = 3
	PlayerUIState     *SyncedMMUIState        // = 5
}

func (m *PartyOptions) Marshal() []byte {
	w := NewWriter()
	w.OptBool(1, m.OverwriteExisting)
	OptMessage(w, 2, m.GroupCriteria)
	OptMessage(w, 3, m.PlayerCriteria)
	OptMessage(w, 5, m.PlayerUIState)
	return w.Bytes()
}

func UnmarshalPartyOptions(data []byte) (*PartyOptions, error) {
	m := &PartyOptions{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			v, err := r.Bool()
			if err != nil {
				return nil, err
			}
			m.OverwriteExisting = &v
		case 2:
			b, err := r.Bytes()
			if err != nil {
				return nil, err
			}
			sub, err := UnmarshalGroupMatchCriteria(b)
			if err != nil {
				return nil, err
			}
			m.GroupCriteria = sub
		case 3:
			b, err := r.Bytes()
			if err != nil {
				return nil, err
			}
			sub, err := unmarshalPerPlayerMatchCriteria(b)
			if err != nil {
				return nil, err
			}
			m.PlayerCriteria = sub
		case 5:
			b, err := r.Bytes()
			if err != nil {
				return nil, err
			}
			sub, err := unmarshalSyncedMMUIState(b)
			if err != nil {
				return nil, err
			}
			m.PlayerUIState = sub
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// PendingPartyMemberType mirrors TFPendingPartyMember.EType.
type PendingPartyMemberType int32

const (
	PendingInvited         PendingPartyMemberType = 0
	PendingRequestedToJoin PendingPartyMemberType = 1
)

// PendingPartyMember is TFPendingPartyMember: an invite or join request
// against this party that has not resolved yet.
type PendingPartyMember struct {
	SteamID *uint64 // fixed64 = 1
	Type    *int32  // enum EType = 2, default Invited(0)
	Inviter *uint64 // fixed64 = 3
}

func (m *PendingPartyMember) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.SteamID)
	w.OptEnum(2, m.Type)
	w.OptFixed64(3, m.Inviter)
	return w.Bytes()
}

func unmarshalPendingPartyMember(data []byte) (*PendingPartyMember, error) {
	m := &PendingPartyMember{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.SteamID = &v
		case 2:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			m.Type = &v
		case 3:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.Inviter = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// PartyMemberActivity is CSOTFPartyMember.Activity.
type PartyMemberActivity struct {
	LobbyID           *uint64 // fixed64 = 1
	LobbyMatchGroup   *int32  // enum = 2
	MultiqueueBlocked *bool   // bool = 3
	Online            *bool   // bool = 4
	ClientVersion     *uint32 // uint32 = 5
}

func (m *PartyMemberActivity) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.LobbyID)
	w.OptEnum(2, m.LobbyMatchGroup)
	w.OptBool(3, m.MultiqueueBlocked)
	w.OptBool(4, m.Online)
	w.OptUint32(5, m.ClientVersion)
	return w.Bytes()
}

// PartyMember is CSOTFPartyMember -- the active (non-deprecated) subset.
// steamid (field 1 in older revisions) is commented out in the real
// .proto: a member's identity comes from CSOTFParty.member_ids, which is a
// parallel array to CSOTFParty.members (same order, same length). SteamID
// here is therefore Go-side bookkeeping only, used by gcparty to build that
// parallel array -- it is deliberately never written to the wire.
type PartyMember struct {
	SteamID            *uint64 // NOT marshaled; see above
	OwnsTicket         *bool // bool = 2
	CompletedMissions  *uint32
	BadgeLevel         *uint32
	CompetitiveAccess  *bool // bool = 9
	Experience         *uint32
	PlayerCriteria     *PerPlayerMatchCriteria
	Activity           *PartyMemberActivity
	CasualBanned       *bool
	RankedBanned       *bool
	CasualLowPriority  *bool
	RankedLowPriority  *bool
	LobbyStandby       *bool
}

func (m *PartyMember) Marshal() []byte {
	w := NewWriter()
	w.OptBool(2, m.OwnsTicket)
	w.OptUint32(3, m.CompletedMissions)
	w.OptUint32(4, m.BadgeLevel)
	w.OptBool(9, m.CompetitiveAccess)
	w.OptUint32(14, m.Experience)
	OptMessage(w, 16, m.PlayerCriteria)
	OptMessage(w, 17, m.Activity)
	w.OptBool(18, m.CasualBanned)
	w.OptBool(19, m.RankedBanned)
	w.OptBool(20, m.CasualLowPriority)
	w.OptBool(21, m.RankedLowPriority)
	w.OptBool(22, m.LobbyStandby)
	return w.Bytes()
}

// PartyQueueEntry is CSOTFParty.QueueEntry.
type PartyQueueEntry struct {
	MatchGroup *int32  // enum ETFMatchGroup = 1
	QueuedTime *uint32 // fixed32 = 2
}

func (m *PartyQueueEntry) Marshal() []byte {
	w := NewWriter()
	w.OptEnum(1, m.MatchGroup)
	w.OptFixed32(2, m.QueuedTime)
	return w.Bytes()
}

// Party is CSOTFParty -- the SO object pushed to every member describing
// party membership, matchmaking queue state, and criteria. Deprecated
// fields from the real .proto (game_mode, pending_invites, the old state
// enum, all the pre-multiqueue search_* fields) are intentionally not
// implemented, matching the live client's own schema.
type Party struct {
	PartyID                 *uint64            // uint64 = 1
	LeaderID                *uint64            // fixed64 = 2
	MemberIDs               []uint64           // repeated fixed64 = 3
	Members                 []*PartyMember     // repeated = 13
	AssociatedLobbyID       *uint64            // uint64 = 35
	AssociatedLobbyMatchGrp *int32             // enum = 40
	MatchmakingQueues       []*PartyQueueEntry // repeated = 43
	GroupCriteria           *GroupMatchCriteria // = 37
	CasualBannedTime        *uint32            // uint32 = 18
	CasualLowPriorityTime   *uint32            // uint32 = 20
	RankedBannedTime        *uint32            // uint32 = 41
	RankedLowPriorityTime   *uint32            // uint32 = 42
	LeaderUIState           *SyncedMMUIState   // = 44
	PendingMembers          []*PendingPartyMember // repeated = 39
}

func (m *Party) Marshal() []byte {
	w := NewWriter()
	w.OptUint64(1, m.PartyID)
	w.OptFixed64(2, m.LeaderID)
	RepeatedFixed64(w, 3, m.MemberIDs)
	RepeatedMessage(w, 13, m.Members)
	w.OptUint64(35, m.AssociatedLobbyID)
	w.OptEnum(40, m.AssociatedLobbyMatchGrp)
	RepeatedMessage(w, 43, m.MatchmakingQueues)
	OptMessage(w, 37, m.GroupCriteria)
	w.OptUint32(18, m.CasualBannedTime)
	w.OptUint32(20, m.CasualLowPriorityTime)
	w.OptUint32(41, m.RankedBannedTime)
	w.OptUint32(42, m.RankedLowPriorityTime)
	OptMessage(w, 44, m.LeaderUIState)
	RepeatedMessage(w, 39, m.PendingMembers)
	return w.Bytes()
}

// PartyInviteType mirrors CSOTFPartyInvite.Type.
type PartyInviteType int32

const (
	InvitePending      PartyInviteType = 1
	InvitePendingJoin  PartyInviteType = 2
)

// PartyInviteMember is CSOTFPartyInvite.PartyMember.
type PartyInviteMember struct {
	SteamID *uint64 // fixed64 = 2
}

func (m *PartyInviteMember) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(2, m.SteamID)
	return w.Bytes()
}

// PartyInvite is CSOTFPartyInvite -- pushed (SO create) to a player who has
// been invited to a party, or who has requested to join one. group_id is
// the target party's ID; type distinguishes an incoming invite from an
// incoming join request the party leader must act on.
type PartyInvite struct {
	GroupID  *uint64              // uint64 = 1
	Inviter  *uint64              // fixed64 = 2
	Members  []*PartyInviteMember // repeated = 4
	Type     *int32               // enum Type = 5
}

func (m *PartyInvite) Marshal() []byte {
	w := NewWriter()
	w.OptUint64(1, m.GroupID)
	w.OptFixed64(2, m.Inviter)
	RepeatedMessage(w, 4, m.Members)
	w.OptEnum(5, m.Type)
	return w.Bytes()
}
