package gcproto

// EMsg is a Steam/GC message type. On the wire it is an int32 that may have
// ProtoBufFlag set; GetEMsg (below) is how the real client strips that bit.
type EMsg int32

// ProtoBufFlag is k_EMsgProtoBufFlag, confirmed in
// src/public/gcsdk/msgbase.h: `const uint32 k_EMsgProtoBufFlag = 0x80000000;`
//
// Every message this coordinator's HTTP transport carries is protobuf (the
// C++ transport in frontress_gc.cpp only ever builds CProtoBufMsg), so this
// bit is always set on the wire and must be masked off to read the EMsg and
// set again when writing one.
const ProtoBufFlag uint32 = 0x80000000

// FlagProto sets ProtoBufFlag on an EMsg for the wire.
func FlagProto(e EMsg) uint32 { return uint32(e) | ProtoBufFlag }

// UnflagProto strips ProtoBufFlag from a wire value, per
// ProtoBufMsgHeader_t::GetEMsg() in gcmsg.h.
func UnflagProto(v uint32) EMsg { return EMsg(v &^ ProtoBufFlag) }

// --- EGCBaseClientMsg (src/gcsdk/gcsystemmsgs.proto) ---
// These are the live (non-deprecated) handshake values; base_gcmessages.proto
// carries the same numbers commented out under their old EGCBaseMsg names.
const (
	EMsgGCClientWelcome EMsg = 4004 // GC -> client
	EMsgGCClientHello   EMsg = 4006 // client -> GC
)

// --- EGCBaseMsg (src/game/shared/base_gcmessages.proto) ---
const (
	EMsgGCServerAvailable       EMsg = 4506
	EMsgGCClientConnectToServer EMsg = 4507
	EMsgGCGameServerInfo        EMsg = 4508
	EMsgGCLANServerAvailable    EMsg = 4511
)

// --- ESOMsg (src/gcsdk/gcsystemmsgs.proto) ---
// The shared-object cache push mechanism. This is how party/lobby state
// (and, in the stock game, inventory) reaches the client: not a bespoke
// "party changed" message, but a generic SO cache update naming a type_id
// from EGCTFProtoObjectTypes (tf_gcmessages.h) and carrying the serialized
// CSOTFParty/CSOTFGameServerLobby/CSOTFPartyInvite bytes.
const (
	EMsgSOCreate                   EMsg = 21
	EMsgSOUpdate                   EMsg = 22
	EMsgSODestroy                  EMsg = 23
	EMsgSOCacheSubscribed          EMsg = 24
	EMsgSOCacheUnsubscribed        EMsg = 25
	EMsgSOUpdateMultiple           EMsg = 26
	EMsgSOCacheSubscriptionCheck   EMsg = 27
	EMsgSOCacheSubscriptionRefresh EMsg = 28
	EMsgSOCacheSubscribedUpToDate  EMsg = 29
)

// --- ETFGCMsg (src/game/shared/tf/tf_gcmessages.proto) ---
// Only the party/lobby/matchmaking subset this coordinator implements.
const (
	EMsgGCReadyUp                         EMsg = 6270
	EMsgGCKickedFromMatchmakingQueue      EMsg = 6271
	EMsgGCExitMatchmaking                 EMsg = 6289
	EMsgGCMatchmakingProgress             EMsg = 6293
	EMsgGCGameServerMatchmakingStatus     EMsg = 6295
	EMsgGCGameServerKickingLobby          EMsg = 6299
	EMsgGCLeaveGameAndPrepareToJoinParty  EMsg = 6300
	EMsgGCGameServerKickingLobbyResponse  EMsg = 6521
	EMsgGCKickPlayerFromLobby             EMsg = 6531
	EMsgGCNewMatchForLobbyRequest         EMsg = 6537
	EMsgGCNewMatchForLobbyResponse        EMsg = 6538
	EMsgGCPartySetOptions                 EMsg = 6554
	EMsgGCPartySetOptionsResponse         EMsg = 6555
	EMsgGCPartyQueueForMatch              EMsg = 6556
	EMsgGCPartyQueueForMatchResponse      EMsg = 6557
	EMsgGCPartyRemoveFromQueue            EMsg = 6558
	EMsgGCPartyRemoveFromQueueResponse    EMsg = 6559
	EMsgGCPartyInvitePlayer               EMsg = 6560
	EMsgGCPartyRequestJoinPlayer          EMsg = 6561
	EMsgGCPartySendChat                   EMsg = 6562
	EMsgGCPartyChatMsg                    EMsg = 6563
	EMsgGCPartyQueueForStandby            EMsg = 6567
	EMsgGCPartyQueueForStandbyResponse    EMsg = 6568
	EMsgGCPartyRemoveFromStandbyQueue     EMsg = 6569
	EMsgGCPartyRemoveFromStandbyQueueResp EMsg = 6570
	EMsgGCPartyClearPendingPlayer         EMsg = 6571
	EMsgGCPartyClearPendingPlayerResponse EMsg = 6572
	EMsgGCPartyClearOtherPartyRequest     EMsg = 6573
	EMsgGCPartyClearOtherPartyRequestResp EMsg = 6574
	EMsgGCPartyPromoteToLeader            EMsg = 6575
	EMsgGCPartyKickMember                 EMsg = 6576
	EMsgGCAcceptLobbyInvite               EMsg = 6578
	EMsgGCAcceptLobbyInviteReply          EMsg = 6579
	EMsgGCProcessMatchVoteKick            EMsg = 6581 // game server -> GC
	EMsgGCProcessMatchVoteKickResponse    EMsg = 6582 // GC -> game server
	EMsgGCPartyMMError                    EMsg = 6586 // GC -> client, out-of-flow queue kicks
)

// SO cache type IDs, confirmed in
// src/game/shared/tf/tf_gcmessages.h (EGCTFProtoObjectTypes):
//
//	k_EProtoObjectTypesGameBase   = 2000
//	k_EProtoObjectTFParty         = 2003
//	k_EProtoObjectTFGameServerLobby = 2004
//	k_EProtoObjectTFPartyInvite   = 2006
//	k_EProtoObjectTFLobbyInvite   = 2008
//
// These are the type_id values that go inside CMsgSOCacheSubscribed /
// CMsgSOMultipleObjects when pushing party, lobby, or invite objects.
const (
	SOTypeTFParty           int32 = 2003
	SOTypeTFGameServerLobby int32 = 2004
	SOTypeTFPartyInvite     int32 = 2006
	SOTypeTFLobbyInvite     int32 = 2008
)
