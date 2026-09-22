package gcproto

// This file implements the generic shared-object cache push messages, field
// for field from src/gcsdk/gcsdk_gcmessages.proto. This is the real
// mechanism the GC uses to deliver party/lobby state to a client: there is
// no bespoke "party changed" or "match found" message type. The client
// subscribes once (implicitly, via CGCSOCacheSubscribedJob already present
// in the untouched GCSDK client code) and the coordinator pushes
// CMsgSOCacheSubscribed whenever an owned object (a CSOTFParty or
// CSOTFGameServerLobby) is created or changes, and CMsgSOMultipleObjects /
// individual SO update messages for incremental changes.

// SOIDOwner is CMsgSOIDOwner.
type SOIDOwner struct {
	Type *uint32 // uint32 = 1
	ID   *uint64 // uint64 = 2
}

func (o *SOIDOwner) Marshal() []byte {
	w := NewWriter()
	if o == nil {
		return w.Bytes()
	}
	w.OptUint32(1, o.Type)
	w.OptUint64(2, o.ID)
	return w.Bytes()
}

func unmarshalSOIDOwner(data []byte) (*SOIDOwner, error) {
	o := &SOIDOwner{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			o.Type = &v
		case 2:
			v, err := r.Varint()
			if err != nil {
				return nil, err
			}
			o.ID = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return o, nil
}

// SOCacheSubscribedType is CMsgSOCacheSubscribed.SubscribedType: one SO type
// and every currently-owned object of that type, each pre-serialized.
type SOCacheSubscribedType struct {
	TypeID     *int32   // int32          = 1
	ObjectData [][]byte // repeated bytes = 2
}

func (t *SOCacheSubscribedType) Marshal() []byte {
	w := NewWriter()
	w.OptInt32(1, t.TypeID)
	RepeatedBytes(w, 2, t.ObjectData)
	return w.Bytes()
}

// NewSubscribedType is a convenience constructor for one SO type entry
// carrying a single serialized object -- the common case when pushing a
// party or lobby update to its one owner.
func NewSubscribedType(typeID int32, objects ...[]byte) *SOCacheSubscribedType {
	return &SOCacheSubscribedType{TypeID: &typeID, ObjectData: objects}
}

// SOCacheSubscribed is CMsgSOCacheSubscribed (k_ESOMsg_CacheSubscribed=24).
// GC -> client. owner is the SteamID this cache belongs to (the party
// leader's or lobby member's SteamID -- SO caches in TF2 are per-player,
// not per-object).
type SOCacheSubscribed struct {
	Owner       *uint64                  // fixed64 = 1
	Objects     []*SOCacheSubscribedType // repeated SubscribedType = 2
	Version     *uint64                  // fixed64 = 3
	OwnerSOID   *SOIDOwner               // .CMsgSOIDOwner = 4
	ServiceID   *uint32                  // uint32 = 5
	ServiceList []uint32                 // repeated uint32 = 6
	SyncVersion *uint64                  // fixed64 = 7
}

func (m *SOCacheSubscribed) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.Owner)
	RepeatedMessage(w, 2, m.Objects)
	w.OptFixed64(3, m.Version)
	OptMessage(w, 4, m.OwnerSOID)
	w.OptUint32(5, m.ServiceID)
	RepeatedUint32(w, 6, m.ServiceList)
	w.OptFixed64(7, m.SyncVersion)
	return w.Bytes()
}

// SOCacheSubscribedUpToDate is CMsgSOCacheSubscribedUpToDate
// (k_ESOMsg_CacheSubscribedUpToDate=29). GC -> client, sent once after the
// initial CMsgSOCacheSubscribed batch to say "that was everything you own,
// stop showing a loading spinner".
type SOCacheSubscribedUpToDate struct {
	Version     *uint64
	OwnerSOID   *SOIDOwner
	ServiceID   *uint32
	ServiceList []uint32
	SyncVersion *uint64
}

func (m *SOCacheSubscribedUpToDate) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.Version)
	OptMessage(w, 2, m.OwnerSOID)
	w.OptUint32(3, m.ServiceID)
	RepeatedUint32(w, 4, m.ServiceList)
	w.OptFixed64(5, m.SyncVersion)
	return w.Bytes()
}

// SOMultipleObjectsSingle is CMsgSOMultipleObjects.SingleObject.
type SOMultipleObjectsSingle struct {
	TypeID     *int32
	ObjectData []byte
}

func (s *SOMultipleObjectsSingle) Marshal() []byte {
	w := NewWriter()
	w.OptInt32(1, s.TypeID)
	w.OptBytes(2, s.ObjectData)
	return w.Bytes()
}

// SOMultipleObjects is CMsgSOMultipleObjects (k_ESOMsg_UpdateMultiple=26).
// GC -> client, used for incremental multi-object updates (e.g. a lobby's
// player list changing on top of an existing subscription, rather than a
// full re-subscribe).
type SOMultipleObjects struct {
	Owner     *uint64
	Objects   []*SOMultipleObjectsSingle
	Version   *uint64
	OwnerSOID *SOIDOwner
	ServiceID *uint32
}

func (m *SOMultipleObjects) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.Owner)
	RepeatedMessage(w, 2, m.Objects)
	w.OptFixed64(3, m.Version)
	OptMessage(w, 6, m.OwnerSOID)
	w.OptUint32(7, m.ServiceID)
	return w.Bytes()
}

// SOSingleObject is CMsgSOSingleObject, used for k_ESOMsg_Create/Update/Destroy
// (single-object create/update/destroy notifications, EMsg 21/22/23).
type SOSingleObject struct {
	Owner      *uint64
	TypeID     *int32
	ObjectData []byte
	Version    *uint64
	OwnerSOID  *SOIDOwner
	ServiceID  *uint32
}

func (m *SOSingleObject) Marshal() []byte {
	w := NewWriter()
	w.OptFixed64(1, m.Owner)
	w.OptInt32(2, m.TypeID)
	w.OptBytes(3, m.ObjectData)
	w.OptFixed64(4, m.Version)
	OptMessage(w, 5, m.OwnerSOID)
	w.OptUint32(6, m.ServiceID)
	return w.Bytes()
}
