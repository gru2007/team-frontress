package socache

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
)

type sent struct {
	owner uint64
	typ   uint32
	ver   uint64
}

func TestPutUpdateDeleteRefresh(t *testing.T) {
	var log []sent
	s := New(func(owner uint64, typ uint32, body proto.Message) {
		log = append(log, sent{owner, typ, body.(*gcpb.CMsgSOSingleObject).GetVersion()})
	})
	party := &gcpb.CSOTFParty{PartyId: proto.Uint64(1), LeaderId: proto.Uint64(7)}
	s.Put(7, TypeParty, "1", party)
	s.Put(7, TypeParty, "1", party) // unchanged: nothing sent
	party.MemberIds = []uint64{7}
	s.Put(7, TypeParty, "1", party)
	if len(log) != 2 || log[0].typ != msgCreate || log[1].typ != msgUpdate || log[1].ver <= log[0].ver {
		t.Fatalf("got %+v", log)
	}
	s.Refresh(7)
	if len(log) != 3 || log[2].typ != msgCreate {
		t.Fatalf("refresh: %+v", log)
	}
	s.Delete(7, TypeParty, "1")
	s.Delete(7, TypeParty, "1") // gone already: nothing sent
	if len(log) != 4 || log[3].typ != msgDestroy || s.Has(7, TypeParty, "1") {
		t.Fatalf("delete: %+v", log)
	}
	// A later create must not reuse a version the client has seen.
	s.Put(7, TypeParty, "1", party)
	if log[4].ver <= log[3].ver {
		t.Fatalf("version went backwards: %+v", log)
	}
}
