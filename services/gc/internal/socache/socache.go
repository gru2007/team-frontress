// Package socache is the GC's side of the shared object caches.
//
// Every object the game reads -- a party, a lobby, an invite -- lives in the
// cache of the SteamID it is shown to, and the game's gcsdk keeps its copy in
// step from k_ESOMsg_Create / Update / Destroy. This package holds the GC's
// copy and emits those messages; nothing else in the GC sends them.
//
// The GC never sends k_ESOMsg_CacheSubscribed to a player. The player's cache
// is also where the Valve web-API inventory lands, and a subscribe message
// replaces the whole cache: it would delete the inventory, or the inventory
// arriving would delete our objects. Instead the game asks for a refresh
// (k_ESOMsg_CacheSubscriptionRefresh) whenever its cache was replaced, and
// Refresh resends every object as a create.
package socache

import (
	"sort"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
)

// Type IDs from tf_gcmessages.h (EGCTFProtoObjectTypes).
const (
	TypeParty       int32 = 2003
	TypeLobby       int32 = 2004
	TypePartyInvite int32 = 2006
	TypeRatingData  int32 = 2007
	TypeLobbyInvite int32 = 2008
)

// Message types (ESOMsg).
const (
	msgCreate  = uint32(gcpb.ESOMsg_k_ESOMsg_Create)
	msgUpdate  = uint32(gcpb.ESOMsg_k_ESOMsg_Update)
	msgDestroy = uint32(gcpb.ESOMsg_k_ESOMsg_Destroy)
)

// Sender delivers one message to the session of a SteamID, if it has one. It
// is fine for it to drop the message when nobody is connected: a session that
// starts later gets a Refresh.
type Sender func(owner uint64, msgType uint32, body proto.Message)

type objKey struct {
	typ int32
	key string
}

type cache struct {
	version uint64
	objects map[objKey][]byte
}

// Store is every cache the GC writes to. It is not safe for concurrent use;
// the GC owns it from one goroutine.
type Store struct {
	caches map[uint64]*cache
	send   Sender
}

// New makes an empty store.
func New(send Sender) *Store {
	return &Store{caches: map[uint64]*cache{}, send: send}
}

func (s *Store) cacheOf(owner uint64) *cache {
	c := s.caches[owner]
	if c == nil {
		c = &cache{objects: map[objKey][]byte{}}
		s.caches[owner] = c
	}
	return c
}

// Put creates or updates one object. key identifies the object within its
// type and must be built from the same fields the .proto marks key_field.
// Unchanged objects are not resent.
func (s *Store) Put(owner uint64, typ int32, key string, obj proto.Message) {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(obj)
	if err != nil {
		return
	}
	c := s.cacheOf(owner)
	k := objKey{typ, key}
	old, existed := c.objects[k]
	if existed && string(old) == string(data) {
		return
	}
	c.objects[k] = data
	c.version++
	msgType := msgCreate
	if existed {
		msgType = msgUpdate
	}
	s.send(owner, msgType, &gcpb.CMsgSOSingleObject{
		Owner:      proto.Uint64(owner),
		TypeId:     proto.Int32(typ),
		ObjectData: data,
		Version:    proto.Uint64(c.version),
	})
}

// Delete destroys one object. Deleting something that is not there does
// nothing.
func (s *Store) Delete(owner uint64, typ int32, key string) {
	c := s.caches[owner]
	if c == nil {
		return
	}
	k := objKey{typ, key}
	data, ok := c.objects[k]
	if !ok {
		return
	}
	delete(c.objects, k)
	c.version++
	s.send(owner, msgDestroy, &gcpb.CMsgSOSingleObject{
		Owner:      proto.Uint64(owner),
		TypeId:     proto.Int32(typ),
		ObjectData: data,
		Version:    proto.Uint64(c.version),
	})
	if len(c.objects) == 0 {
		// Keep the version: a later create must not reuse a version the
		// client has already seen.
		c.objects = map[objKey][]byte{}
	}
}

// Has reports whether an object exists.
func (s *Store) Has(owner uint64, typ int32, key string) bool {
	c := s.caches[owner]
	if c == nil {
		return false
	}
	_, ok := c.objects[objKey{typ, key}]
	return ok
}

// Keys lists the keys of one type in one cache.
func (s *Store) Keys(owner uint64, typ int32) []string {
	c := s.caches[owner]
	if c == nil {
		return nil
	}
	var out []string
	for k := range c.objects {
		if k.typ == typ {
			out = append(out, k.key)
		}
	}
	sort.Strings(out)
	return out
}

// Refresh resends every object in a cache as a create. A create of an object
// the game already has updates it in place, so this is safe to do at any time.
func (s *Store) Refresh(owner uint64) {
	c := s.caches[owner]
	if c == nil {
		return
	}
	keys := make([]objKey, 0, len(c.objects))
	for k := range c.objects {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].typ != keys[j].typ {
			return keys[i].typ < keys[j].typ
		}
		return keys[i].key < keys[j].key
	})
	for _, k := range keys {
		s.send(owner, msgCreate, &gcpb.CMsgSOSingleObject{
			Owner:      proto.Uint64(owner),
			TypeId:     proto.Int32(k.typ),
			ObjectData: c.objects[k],
			Version:    proto.Uint64(c.version),
		})
	}
}
