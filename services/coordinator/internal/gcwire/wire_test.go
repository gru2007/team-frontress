package gcwire

import (
	"errors"
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"google.golang.org/protobuf/proto"
)

func TestPacketRoundTrip(t *testing.T) {
	source := uint64(42)
	in := &gcproto.CMsgClientHello{Version: proto.Uint32(7)}
	w, err := Encode(4006, &gcproto.CMsgProtoBufHeader{JobIdSource: &source}, in)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Decode(w)
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != 4006 || p.Header.GetJobIdSource() != source {
		t.Fatalf("wrong packet: %+v", p)
	}
	out := new(gcproto.CMsgClientHello)
	if err := proto.Unmarshal(p.Body, out); err != nil {
		t.Fatal(err)
	}
	if out.GetVersion() != 7 {
		t.Fatalf("version = %d", out.GetVersion())
	}
}

func TestRejectsMalformedPacket(t *testing.T) {
	_, err := Decode(Message{Type: 1, Data: "AA=="})
	if !errors.Is(err, ErrMalformedPacket) {
		t.Fatalf("got %v", err)
	}
}

func TestReplyHeaderTargetsSourceJob(t *testing.T) {
	source := uint64(99)
	h := ReplyHeader(&gcproto.CMsgProtoBufHeader{JobIdSource: &source})
	if h.GetJobIdTarget() != source {
		t.Fatalf("target = %d", h.GetJobIdTarget())
	}
}
