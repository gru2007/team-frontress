package wire

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
)

func TestEncodeDecodeCarriesTheJob(t *testing.T) {
	raw, err := Encode(6557, &gcpb.CMsgPartyQueueForMatchResponse{}, 99)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != 6557 || m.Header.GetJobIdTarget() != 99 || m.Header.GetEresult() != 1 {
		t.Fatalf("got type %d header %v", m.Type, m.Header)
	}
}

func TestBatchRoundTrip(t *testing.T) {
	body, _ := proto.Marshal(&gcpb.CMsgPartySendChat{Msg: proto.String("hi")})
	var b []byte
	b = AppendFrame(b, Frame{Seq: 1, Type: 1 | ProtoFlag, Data: body})
	b = AppendFrame(b, Frame{Seq: 2, Type: 2 | ProtoFlag})
	fs, err := ParseBatch(b)
	if err != nil || len(fs) != 2 || fs[0].Seq != 1 || string(fs[0].Data) != string(body) || len(fs[1].Data) != 0 {
		t.Fatalf("got %+v %v", fs, err)
	}
	if _, err := ParseBatch(b[:len(b)-1]); err == nil {
		t.Fatal("a truncated batch parsed")
	}
}
