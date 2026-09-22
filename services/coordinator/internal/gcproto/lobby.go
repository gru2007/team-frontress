package gcproto

// CTFLobbyPlayerProto, CSOTFGameServerLobby, CTFLobbyInviteProto and the
// server<->GC lobby-kick messages, field numbers confirmed against
// tf_gcmessages.proto.

// LobbyConnectState mirrors CTFLobbyPlayerProto.ConnectState.
type LobbyConnectState int32

const (
	ConnectInvalid            LobbyConnectState = 0
	ConnectReservationPending LobbyConnectState = 1
	ConnectReserved           LobbyConnectState = 2
	ConnectConnected          LobbyConnectState = 3
	ConnectDisconnected       LobbyConnectState = 5
)

// LobbyPlayerType mirrors CTFLobbyPlayerProto.Type.
type LobbyPlayerType int32

const (
	PlayerInvalid   LobbyPlayerType = 0
	PlayerMatch     LobbyPlayerType = 1
	PlayerStandby   LobbyPlayerType = 2
	PlayerObserving LobbyPlayerType = 3
)

// LobbyPlayer is CTFLobbyPlayerProto (key_field id=1).
type LobbyPlayer struct {
	ID               *uint64  // fixed64 = 1 (key)
	Team             *int32   // enum TF_GC_TEAM = 3
	ConnectState     *int32   // enum ConnectState = 13
	Name             *string  // string = 6
	OriginalPartyID  *uint64  // uint64 = 12
	SquadSurplus     *bool    // bool = 14
	BadgeLevel       *uint32  // uint32 = 15
	LastConnectTime  *uint32  // uint32 = 17
	Type             *int32   // enum Type = 19
	NormalizedRating *float64 // double = 20
	NormalizedUncert *float64 // double = 22
	Rank             *uint32  // uint32 = 21
	ChatSuspension   *bool    // bool = 23
}

func (m *LobbyPlayer) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.ID)
	w.OptEnum(3, m.Team)
	w.OptEnum(13, m.ConnectState)
	w.OptString(6, m.Name)
	w.OptUint64(12, m.OriginalPartyID)
	w.OptBool(14, m.SquadSurplus)
	w.OptUint32(15, m.BadgeLevel)
	w.OptUint32(17, m.LastConnectTime)
	w.OptEnum(19, m.Type)
	w.OptDouble(20, m.NormalizedRating)
	w.OptDouble(22, m.NormalizedUncert)
	w.OptUint32(21, m.Rank)
	w.OptBool(23, m.ChatSuspension)
	return w.Bytes()
}

func UnmarshalLobbyPlayer(data []byte) (*LobbyPlayer, error) {
	m := &LobbyPlayer{}
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
			m.ID = &v
		case 3:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			m.Team = &v
		case 13:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			m.ConnectState = &v
		case 6:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			m.Name = &v
		case 12:
			v, err := r.Varint()
			if err != nil {
				return nil, err
			}
			m.OriginalPartyID = &v
		case 14:
			v, err := r.Bool()
			if err != nil {
				return nil, err
			}
			m.SquadSurplus = &v
		case 15:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.BadgeLevel = &v
		case 17:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.LastConnectTime = &v
		case 19:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			m.Type = &v
		case 20:
			v, err := r.Double()
			if err != nil {
				return nil, err
			}
			m.NormalizedRating = &v
		case 22:
			v, err := r.Double()
			if err != nil {
				return nil, err
			}
			m.NormalizedUncert = &v
		case 21:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.Rank = &v
		case 23:
			v, err := r.Bool()
			if err != nil {
				return nil, err
			}
			m.ChatSuspension = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// LobbyInvite is CTFLobbyInviteProto (key_field lobby_id=1).
type LobbyInvite struct {
	LobbyID    *uint64 // fixed64 = 1 (key)
	MatchGroup *int32  // enum ETFMatchGroup = 2
}

func (m *LobbyInvite) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.LobbyID)
	w.OptEnum(2, m.MatchGroup)
	return w.Bytes()
}

func UnmarshalLobbyInvite(data []byte) (*LobbyInvite, error) {
	m := &LobbyInvite{}
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
			m.LobbyID = &v
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

// LobbyState mirrors CSOTFGameServerLobby.State.
type LobbyState int32

const (
	LobbyUnknown     LobbyState = 0
	LobbyServerSetup LobbyState = 1
	LobbyRun         LobbyState = 2
)

// GameServerLobby is CSOTFGameServerLobby (key_field lobby_id=1). This is
// the sole vehicle for "match found": there is no separate match-found
// message in the real protocol -- the client is watching this SO for
// State transitioning to RUN and Connect becoming non-empty.
type GameServerLobby struct {
	LobbyID            *uint64        // uint64 = 1 (key)
	Members            []*LobbyPlayer // repeated = 2
	ServerID           *uint64        // fixed64 = 6, default 0
	State              *int32         // enum State = 4, default UNKNOWN
	Connect            *string        // string = 5, "ip:port", valid only during RUN
	InitialAvgMMRating *float64       // double = 32
	MannupTourName     *string        // string = 42
	MapName            *string        // string = 38
	MissionName        *string        // string = 39
	MatchGroup         *uint32        // uint32 = 41
	MatchID            *uint64        // uint64 = 30, default 0
	FormedTime         *uint32        // uint32 = 36
	Flags              *uint32        // uint32 = 43
	LateJoinEligible   *bool          // bool = 44
	FixedMatchSize     *uint32        // uint32 = 45
	NextMapsForVote    []uint32       // repeated uint32 = 47
	LobbyMMVersion     *uint32        // uint32 = 48
	PendingMembers     []*LobbyPlayer // repeated = 49
}

func (m *GameServerLobby) Marshal() []byte {
	w := NewWriter()
	w.OptUint64(1, m.LobbyID)
	RepeatedMessage(w, 2, m.Members)
	w.OptFixed64(6, m.ServerID)
	w.OptEnum(4, m.State)
	w.OptString(5, m.Connect)
	w.OptDouble(32, m.InitialAvgMMRating)
	w.OptString(42, m.MannupTourName)
	w.OptString(38, m.MapName)
	w.OptString(39, m.MissionName)
	w.OptUint32(41, m.MatchGroup)
	w.OptUint64(30, m.MatchID)
	w.OptUint32(36, m.FormedTime)
	w.OptUint32(43, m.Flags)
	w.OptBool(44, m.LateJoinEligible)
	w.OptUint32(45, m.FixedMatchSize)
	RepeatedUint32(w, 47, m.NextMapsForVote)
	w.OptUint32(48, m.LobbyMMVersion)
	RepeatedMessage(w, 49, m.PendingMembers)
	return w.Bytes()
}

func UnmarshalGameServerLobby(data []byte) (*GameServerLobby, error) {
	m := &GameServerLobby{}
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
			m.LobbyID = &v
		case 2:
			b, err := r.Bytes()
			if err != nil {
				return nil, err
			}
			p, err := UnmarshalLobbyPlayer(b)
			if err != nil {
				return nil, err
			}
			m.Members = append(m.Members, p)
		case 6:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.ServerID = &v
		case 4:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			m.State = &v
		case 5:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			m.Connect = &v
		case 32:
			v, err := r.Double()
			if err != nil {
				return nil, err
			}
			m.InitialAvgMMRating = &v
		case 42:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			m.MannupTourName = &v
		case 38:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			m.MapName = &v
		case 39:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			m.MissionName = &v
		case 41:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.MatchGroup = &v
		case 30:
			v, err := r.Varint()
			if err != nil {
				return nil, err
			}
			m.MatchID = &v
		case 36:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.FormedTime = &v
		case 43:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.Flags = &v
		case 44:
			v, err := r.Bool()
			if err != nil {
				return nil, err
			}
			m.LateJoinEligible = &v
		case 45:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.FixedMatchSize = &v
		case 47:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.NextMapsForVote = append(m.NextMapsForVote, v)
		case 48:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.LobbyMMVersion = &v
		case 49:
			b, err := r.Bytes()
			if err != nil {
				return nil, err
			}
			p, err := UnmarshalLobbyPlayer(b)
			if err != nil {
				return nil, err
			}
			m.PendingMembers = append(m.PendingMembers, p)
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// --- k_EMsgGCGameServerKickingLobby / Response (game server -> GC) ---

type GameServerKickingLobby struct {
	LobbyID *uint64 // uint64 = 3
	MatchID *uint64 // uint64 = 4
}

func (m *GameServerKickingLobby) Marshal() []byte {
	w := NewWriter()
	w.OptUint64(3, m.LobbyID)
	w.OptUint64(4, m.MatchID)
	return w.Bytes()
}

func UnmarshalGameServerKickingLobby(data []byte) (*GameServerKickingLobby, error) {
	m := &GameServerKickingLobby{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 3:
			v, err := r.Varint()
			if err != nil {
				return nil, err
			}
			m.LobbyID = &v
		case 4:
			v, err := r.Varint()
			if err != nil {
				return nil, err
			}
			m.MatchID = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

type GameServerKickingLobbyResponse struct{}

func (m *GameServerKickingLobbyResponse) Marshal() []byte { return nil }

// --- k_EMsgGC_KickPlayerFromLobby (GC -> game server) ---
//
// "Tell the server to kick a player." Confirmed field layout:
//
//	message CMsgGC_KickPlayerFromLobby { optional uint64 targetID = 1; };
//
// This coordinator only ever sends this (it is the GC telling the server
// what to do), issued as a push alongside a ProcessMatchVoteKickResponse
// once a vote-kick is approved. Unmarshal is kept for symmetry/tests.
type KickPlayerFromLobby struct {
	TargetID *uint64 // uint64 = 1
}

func (m *KickPlayerFromLobby) Marshal() []byte {
	w := NewWriter()
	w.OptUint64(1, m.TargetID)
	return w.Bytes()
}

func UnmarshalKickPlayerFromLobby(data []byte) (*KickPlayerFromLobby, error) {
	m := &KickPlayerFromLobby{}
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
			m.TargetID = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// --- k_EMsgGC_NewMatchForLobbyRequest / Response ---
//
// Game server -> GC: "the match this lobby was running is over, form a new
// one for the same roster." Confirmed field layout:
//
//	message CMsgGCNewMatchForLobbyRequest {
//		optional uint64 current_match_id = 1;
//		optional uint32 next_map_id = 2;
//		optional uint64 lobby_id = 3;
//	}
//	message CMsgGCNewMatchForLobbyResponse { optional bool success = 1; }
type NewMatchForLobbyRequest struct {
	CurrentMatchID *uint64 // uint64 = 1
	NextMapID      *uint32 // uint32 = 2
	LobbyID        *uint64 // uint64 = 3
}

func (m *NewMatchForLobbyRequest) Marshal() []byte {
	w := NewWriter()
	w.OptUint64(1, m.CurrentMatchID)
	w.OptUint32(2, m.NextMapID)
	w.OptUint64(3, m.LobbyID)
	return w.Bytes()
}

func UnmarshalNewMatchForLobbyRequest(data []byte) (*NewMatchForLobbyRequest, error) {
	m := &NewMatchForLobbyRequest{}
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
			m.CurrentMatchID = &v
		case 2:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.NextMapID = &v
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

type NewMatchForLobbyResponse struct {
	Success *bool // bool = 1
}

func (m *NewMatchForLobbyResponse) Marshal() []byte {
	w := NewWriter()
	w.OptBool(1, m.Success)
	return w.Bytes()
}

func UnmarshalNewMatchForLobbyResponse(data []byte) (*NewMatchForLobbyResponse, error) {
	m := &NewMatchForLobbyResponse{}
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
			m.Success = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// --- k_EMsgGCLeaveGameAndPrepareToJoinParty (GC -> client) ---
//
// "Leave your current game; I am about to put you into a party." Confirmed
// field layout:
//
//	message CMsgLeaveGameAndPrepareToJoinParty { optional fixed64 party_id = 1; };
//
// This coordinator only ever sends this, so only Marshal is exercised in
// production; Unmarshal is kept for tests.
type LeaveGameAndPrepareToJoinParty struct {
	PartyID *uint64 // fixed64 = 1
}

func (m *LeaveGameAndPrepareToJoinParty) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.PartyID)
	return w.Bytes()
}

func UnmarshalLeaveGameAndPrepareToJoinParty(data []byte) (*LeaveGameAndPrepareToJoinParty, error) {
	m := &LeaveGameAndPrepareToJoinParty{}
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
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// --- k_EMsgGC_ProcessMatchVoteKick / Response ---
//
// Server -> GC: "players voted to kick someone, here are the votes and
// whether our own tally says the vote should pass by default -- what do
// you want to do." Confirmed field layout:
//
//	message CMsgProcessMatchVoteKick {
//		message Vote { optional fixed64 steam_id = 1; optional bool vote_yay = 2; };
//		optional fixed64          match_id           = 1;
//		optional fixed64          initiator_steam_id = 2;
//		optional fixed64          target_steam_id    = 3;
//		optional TFVoteKickReason reason             = 4;
//		repeated Vote             votes              = 5;
//		optional bool             default_pass       = 6;
//	}
//	message CMsgProcessMatchVoteKickResponse { optional bool rip = 1; };
type VoteKickVote struct {
	SteamID *uint64 // fixed64 = 1
	VoteYay *bool   // bool = 2
}

func (m *VoteKickVote) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.SteamID)
	w.OptBool(2, m.VoteYay)
	return w.Bytes()
}

func UnmarshalVoteKickVote(data []byte) (*VoteKickVote, error) {
	m := &VoteKickVote{}
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
			v, err := r.Bool()
			if err != nil {
				return nil, err
			}
			m.VoteYay = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

type ProcessMatchVoteKick struct {
	MatchID          *uint64 // fixed64 = 1
	InitiatorSteamID *uint64 // fixed64 = 2
	TargetSteamID    *uint64 // fixed64 = 3
	Reason           *int32  // enum TFVoteKickReason = 4
	Votes            []*VoteKickVote
	DefaultPass      *bool // bool = 6
}

func (m *ProcessMatchVoteKick) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.MatchID)
	w.OptFixed64(2, m.InitiatorSteamID)
	w.OptFixed64(3, m.TargetSteamID)
	w.OptEnum(4, m.Reason)
	RepeatedMessage(w, 5, m.Votes)
	w.OptBool(6, m.DefaultPass)
	return w.Bytes()
}

func UnmarshalProcessMatchVoteKick(data []byte) (*ProcessMatchVoteKick, error) {
	m := &ProcessMatchVoteKick{}
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
			m.MatchID = &v
		case 2:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.InitiatorSteamID = &v
		case 3:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			m.TargetSteamID = &v
		case 4:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			m.Reason = &v
		case 5:
			b, err := r.Bytes()
			if err != nil {
				return nil, err
			}
			v, err := UnmarshalVoteKickVote(b)
			if err != nil {
				return nil, err
			}
			m.Votes = append(m.Votes, v)
		case 6:
			v, err := r.Bool()
			if err != nil {
				return nil, err
			}
			m.DefaultPass = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

type ProcessMatchVoteKickResponse struct {
	Rip *bool // bool = 1
}

func (m *ProcessMatchVoteKickResponse) Marshal() []byte {
	w := NewWriter()
	w.OptBool(1, m.Rip)
	return w.Bytes()
}

func UnmarshalProcessMatchVoteKickResponse(data []byte) (*ProcessMatchVoteKickResponse, error) {
	m := &ProcessMatchVoteKickResponse{}
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
			m.Rip = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}
