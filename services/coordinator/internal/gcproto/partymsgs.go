package gcproto

// This file implements the client<->GC party/matchmaking control messages,
// field-for-field from src/game/shared/tf/tf_gcmessages.proto. See emsg.go
// for the EMsg each one rides under.

// --- k_EMsgGCParty_SetOptions / SetOptionsResponse ---

type PartySetOptions struct {
	PartyID *uint64
	Options *PartyOptions
}

func (m *PartySetOptions) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.PartyID)
	OptMessage(w, 2, m.Options)
	return w.Bytes()
}

func UnmarshalPartySetOptions(data []byte) (*PartySetOptions, error) {
	m := &PartySetOptions{}
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
			m.PartyID = &v
		case 2:
			b, err := r.Bytes()
			if err != nil {
				return nil, err
			}
			sub, err := UnmarshalPartyOptions(b)
			if err != nil {
				return nil, err
			}
			m.Options = sub
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

type PartySetOptionsResponse struct{}

func (m *PartySetOptionsResponse) Marshal() []byte { return nil }

// --- k_EMsgGCParty_QueueForMatch / QueueForMatchResponse ---

type PartyQueueForMatch struct {
	PartyID     *uint64
	FinalOptions *PartyOptions
	MatchGroup  *int32
}

func (m *PartyQueueForMatch) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.PartyID)
	OptMessage(w, 2, m.FinalOptions)
	w.OptEnum(3, m.MatchGroup)
	return w.Bytes()
}

func UnmarshalPartyQueueForMatch(data []byte) (*PartyQueueForMatch, error) {
	m := &PartyQueueForMatch{}
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
			m.PartyID = &v
		case 2:
			b, err := r.Bytes()
			if err != nil {
				return nil, err
			}
			sub, err := UnmarshalPartyOptions(b)
			if err != nil {
				return nil, err
			}
			m.FinalOptions = sub
		case 3:
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

type PartyQueueForMatchResponse struct{}

func (m *PartyQueueForMatchResponse) Marshal() []byte { return nil }

// --- k_EMsgGCParty_RemoveFromQueue / RemoveFromQueueResponse ---

type PartyRemoveFromQueue struct {
	PartyID    *uint64
	MatchGroup *int32
}

func (m *PartyRemoveFromQueue) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.PartyID)
	w.OptEnum(2, m.MatchGroup)
	return w.Bytes()
}

func UnmarshalPartyRemoveFromQueue(data []byte) (*PartyRemoveFromQueue, error) {
	m := &PartyRemoveFromQueue{}
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
			m.PartyID = &v
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

type PartyRemoveFromQueueResponse struct{}

func (m *PartyRemoveFromQueueResponse) Marshal() []byte { return nil }

// --- k_EMsgGCParty_InvitePlayer ---

type PartyInvitePlayer struct {
	PartyID                 *uint64
	PlayerID                *uint64
	ExpectingRequestToJoin *bool
}

func (m *PartyInvitePlayer) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.PartyID)
	w.OptFixed64(2, m.PlayerID)
	w.OptBool(3, m.ExpectingRequestToJoin)
	return w.Bytes()
}

func UnmarshalPartyInvitePlayer(data []byte) (*PartyInvitePlayer, error) {
	m := &PartyInvitePlayer{}
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
			m.PartyID = &v
		case 2:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.PlayerID = &v
		case 3:
			v, err := r.Bool()
			if err != nil {
				return nil, err
			}
			m.ExpectingRequestToJoin = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// --- k_EMsgGCParty_RequestJoinPlayer ---

type PartyRequestJoinPlayer struct {
	CurrentPartyID  *uint64
	JoinPlayerID    *uint64
	JoinPartyID     *uint64
	ExpectingInvite *bool
}

func (m *PartyRequestJoinPlayer) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.CurrentPartyID)
	w.OptFixed64(2, m.JoinPlayerID)
	w.OptFixed64(3, m.JoinPartyID)
	w.OptBool(4, m.ExpectingInvite)
	return w.Bytes()
}

func UnmarshalPartyRequestJoinPlayer(data []byte) (*PartyRequestJoinPlayer, error) {
	m := &PartyRequestJoinPlayer{}
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
			m.CurrentPartyID = &v
		case 2:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.JoinPlayerID = &v
		case 3:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.JoinPartyID = &v
		case 4:
			v, err := r.Bool()
			if err != nil {
				return nil, err
			}
			m.ExpectingInvite = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// --- k_EMsgGCParty_ClearPendingPlayer / ClearPendingPlayerResponse ---

type PartyClearPendingPlayer struct {
	PartyID         *uint64
	PendingPlayerID *uint64
}

func (m *PartyClearPendingPlayer) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.PartyID)
	w.OptFixed64(2, m.PendingPlayerID)
	return w.Bytes()
}

func UnmarshalPartyClearPendingPlayer(data []byte) (*PartyClearPendingPlayer, error) {
	m := &PartyClearPendingPlayer{}
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
			m.PartyID = &v
		case 2:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.PendingPlayerID = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

type PartyClearPendingPlayerResponse struct{}

func (m *PartyClearPendingPlayerResponse) Marshal() []byte { return nil }

// --- k_EMsgGCParty_ClearOtherPartyRequest / ClearOtherPartyRequestResponse ---

type PartyClearOtherPartyRequest struct {
	OtherPartyID *uint64
}

func (m *PartyClearOtherPartyRequest) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.OtherPartyID)
	return w.Bytes()
}

func UnmarshalPartyClearOtherPartyRequest(data []byte) (*PartyClearOtherPartyRequest, error) {
	m := &PartyClearOtherPartyRequest{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		if field == 1 {
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.OtherPartyID = &v
			continue
		}
		if err := r.Skip(wt); err != nil {
			return nil, err
		}
	}
	return m, nil
}

type PartyClearOtherPartyRequestResponse struct{}

func (m *PartyClearOtherPartyRequestResponse) Marshal() []byte { return nil }

// --- k_EMsgGCParty_PromoteToLeader ---

type PartyPromoteToLeader struct {
	PartyID     *uint64
	NewLeaderID *uint64
}

func (m *PartyPromoteToLeader) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.PartyID)
	w.OptFixed64(2, m.NewLeaderID)
	return w.Bytes()
}

func UnmarshalPartyPromoteToLeader(data []byte) (*PartyPromoteToLeader, error) {
	m := &PartyPromoteToLeader{}
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
			m.PartyID = &v
		case 2:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.NewLeaderID = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// --- k_EMsgGCParty_KickMember ---

type PartyKickMember struct {
	PartyID  *uint64
	TargetID *uint64
}

func (m *PartyKickMember) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.PartyID)
	w.OptFixed64(2, m.TargetID)
	return w.Bytes()
}

func UnmarshalPartyKickMember(data []byte) (*PartyKickMember, error) {
	m := &PartyKickMember{}
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
			m.PartyID = &v
		case 2:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.TargetID = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// --- k_EMsgGCParty_MMError (GC -> client, out-of-flow queue kicks) ---

type PartyMMErrorType int32

const (
	MMErrorQueueKickNoPing PartyMMErrorType = 1
	MMErrorQueueKickAuth   PartyMMErrorType = 2
)

type PartyMMError struct {
	Type *int32
}

func (m *PartyMMError) Marshal() []byte {
	w := NewWriter()
	w.OptEnum(1, m.Type)
	return w.Bytes()
}

// --- Party chat: k_EMsgGCParty_SendChat / ChatMsg ---

type PartyChatType int32

const (
	ChatInvalid                PartyChatType = 0
	ChatMemberChat              PartyChatType = 1
	ChatSyntheticMemberJoin     PartyChatType = 1000
	ChatSyntheticMemberLeave    PartyChatType = 1001
	ChatSyntheticSendFailed     PartyChatType = 1002
	ChatSyntheticMemberOnline   PartyChatType = 1003
	ChatSyntheticMemberOffline  PartyChatType = 1004
)

type PartySendChat struct {
	PartyID *uint64
	Msg     *string
}

func (m *PartySendChat) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.PartyID)
	w.OptString(2, m.Msg)
	return w.Bytes()
}

func UnmarshalPartySendChat(data []byte) (*PartySendChat, error) {
	m := &PartySendChat{}
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
			m.PartyID = &v
		case 2:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			m.Msg = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

type PartyChatMsg struct {
	Type    *int32
	ActorID *uint64
	Msg     *string
}

func (m *PartyChatMsg) Marshal() []byte {
	w := NewWriter()
	w.OptEnum(1, m.Type)
	w.OptFixed64(2, m.ActorID)
	w.OptString(3, m.Msg)
	return w.Bytes()
}

// --- k_EMsgGCExitMatchmaking ---
// CMsgExitMatchmaking lives near CSOTFGameServerLobby in the real .proto,
// not with the other Party_* messages, but it is the client's leave/abandon
// signal so it belongs with matchmaking control here.
type ExitMatchmaking struct {
	ExplicitAbandon *bool
	PartyID         *uint64
	LobbyID         *uint64
}

func (m *ExitMatchmaking) Marshal() []byte {
	w := NewWriter()
	w.OptBool(1, m.ExplicitAbandon)
	w.OptUint64(2, m.PartyID)
	w.OptUint64(3, m.LobbyID)
	return w.Bytes()
}

func UnmarshalExitMatchmaking(data []byte) (*ExitMatchmaking, error) {
	m := &ExitMatchmaking{}
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
			m.ExplicitAbandon = &v
		case 2:
			v, err := r.Varint()
			if err != nil {
				return nil, err
			}
			m.PartyID = &v
		case 3:
			v, err := r.Varint()
			if err != nil {
				return nil, err
			}
			m.LobbyID = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// KickedFromMatchmakingQueue is CMsgKickedFromMatchmakingQueue (empty body).
type KickedFromMatchmakingQueue struct{}

func (m *KickedFromMatchmakingQueue) Marshal() []byte { return nil }

// --- k_EMsgGCMatchmakingProgress ---
// Minimal subset: the fields that matter for a client's "estimated wait"
// UI. total/matching *_active_players and *_empty_gameservers are omitted;
// this coordinator does not track a worldwide/near-you split and sending
// zero for all of them would be actively misleading rather than merely
// incomplete.
type MatchmakingProgress struct {
	AvgWaitTimeNew     *uint32 // = 4
	AvgWaitTimeJoinLate *uint32 // = 5
	YourWaitTime       *uint32 // = 6
	UrgencyPct         *uint32 // = 1
}

func (m *MatchmakingProgress) Marshal() []byte {
	w := NewWriter()
	w.OptUint32(4, m.AvgWaitTimeNew)
	w.OptUint32(5, m.AvgWaitTimeJoinLate)
	w.OptUint32(6, m.YourWaitTime)
	w.OptUint32(1, m.UrgencyPct)
	return w.Bytes()
}

// --- k_EMsgGC_AcceptLobbyInvite / AcceptLobbyInviteReply ---

type AcceptLobbyInvite struct {
	InvitedLobbyID          *uint64
	AbandoningMatchID       *uint64
	AbandoningInviteLobbyIDs []uint64
}

func (m *AcceptLobbyInvite) Marshal() []byte {
	w := NewWriter()
	w.OptUint64(1, m.InvitedLobbyID)
	w.OptUint64(2, m.AbandoningMatchID)
	RepeatedUint64(w, 3, m.AbandoningInviteLobbyIDs)
	return w.Bytes()
}

func UnmarshalAcceptLobbyInvite(data []byte) (*AcceptLobbyInvite, error) {
	m := &AcceptLobbyInvite{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			v, err := r.Varint()
			if err != nil {
				return nil, err
			}
			m.InvitedLobbyID = &v
		case 2:
			v, err := r.Varint()
			if err != nil {
				return nil, err
			}
			m.AbandoningMatchID = &v
		case 3:
			v, err := r.Varint()
			if err != nil {
				return nil, err
			}
			m.AbandoningInviteLobbyIDs = append(m.AbandoningInviteLobbyIDs, v)
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

type AcceptLobbyInviteReply struct{}

func (m *AcceptLobbyInviteReply) Marshal() []byte { return nil }
