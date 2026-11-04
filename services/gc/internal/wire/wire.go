// Package wire is the byte layout of a game coordinator message and of the
// batches the HTTP transport carries them in.
//
// A GC message is exactly what Valve's gcsdk puts on the wire, because the game
// hands our transport the bytes gcsdk built and expects to be handed bytes
// gcsdk can parse:
//
//	protobuf message:  [u32 emsg|0x80000000][u32 hdrlen][CMsgProtoBufHeader][body]
//
// Job routing lives in the header: a request carries job_id_source, and the
// reply to it must carry the same value as job_id_target, or the waiting job
// never wakes up.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
)

// ProtoFlag marks a message type as protobuf-framed.
const ProtoFlag uint32 = 0x80000000

// NoJob is gcsdk's k_GIDNil: "no job".
const NoJob uint64 = 0xFFFFFFFFFFFFFFFF

// MaxMessage bounds one message. The largest thing we send is a lobby with
// two dozen members; anything near this is a bug or an attack.
const MaxMessage = 1 << 20

// Msg is one decoded GC message.
type Msg struct {
	Type   uint32 // without ProtoFlag
	Header *gcpb.CMsgProtoBufHeader
	Body   []byte
}

// SourceJob is the job that sent this message, or NoJob.
func (m *Msg) SourceJob() uint64 {
	if m.Header == nil {
		return NoJob
	}
	return m.Header.GetJobIdSource()
}

// Decode parses one protobuf-framed GC message.
func Decode(b []byte) (*Msg, error) {
	if len(b) < 8 {
		return nil, fmt.Errorf("gc message too short: %d bytes", len(b))
	}
	flagged := binary.LittleEndian.Uint32(b[0:4])
	if flagged&ProtoFlag == 0 {
		return nil, fmt.Errorf("gc message %d is not protobuf-framed", flagged)
	}
	hdrLen := binary.LittleEndian.Uint32(b[4:8])
	if uint64(hdrLen) > uint64(len(b)-8) {
		return nil, fmt.Errorf("gc message header length %d overruns %d bytes", hdrLen, len(b))
	}
	hdr := &gcpb.CMsgProtoBufHeader{}
	if err := proto.Unmarshal(b[8:8+hdrLen], hdr); err != nil {
		return nil, fmt.Errorf("gc message header: %w", err)
	}
	return &Msg{Type: flagged &^ ProtoFlag, Header: hdr, Body: b[8+hdrLen:]}, nil
}

// Encode builds the bytes of one protobuf-framed GC message. replyTo is the
// job_id_source of the request this answers, or NoJob for a message nobody is
// waiting on.
func Encode(msgType uint32, body proto.Message, replyTo uint64) ([]byte, error) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = proto.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	// eresult defaults to 2 (k_EResultFail) in the .proto, so an unset field
	// reads as a failure to anything that checks it.
	hdr := &gcpb.CMsgProtoBufHeader{Eresult: proto.Int32(1)}
	if replyTo != NoJob {
		hdr.JobIdTarget = proto.Uint64(replyTo)
	}
	hdrBytes, err := proto.Marshal(hdr)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 8, 8+len(hdrBytes)+len(bodyBytes))
	binary.LittleEndian.PutUint32(out[0:4], msgType|ProtoFlag)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(hdrBytes)))
	out = append(out, hdrBytes...)
	return append(out, bodyBytes...), nil
}

// Frame is one message in a transport batch.
//
// Seq numbers each direction of a session independently, starting at 1. The
// receiver drops anything at or below the last Seq it processed, so a batch
// that is sent twice -- a retried request, a response the client never saw --
// is processed once.
type Frame struct {
	Seq  uint64
	Type uint32 // as ISteamGameCoordinator saw it, ProtoFlag included
	Data []byte
}

// ErrBadBatch is a batch that does not parse.
var ErrBadBatch = errors.New("malformed gc batch")

// AppendFrame appends one frame: [u64 seq][u32 type][u32 len][data].
func AppendFrame(dst []byte, f Frame) []byte {
	var h [16]byte
	binary.LittleEndian.PutUint64(h[0:8], f.Seq)
	binary.LittleEndian.PutUint32(h[8:12], f.Type)
	binary.LittleEndian.PutUint32(h[12:16], uint32(len(f.Data)))
	dst = append(dst, h[:]...)
	return append(dst, f.Data...)
}

// ParseBatch splits a batch into frames. The frames alias b.
func ParseBatch(b []byte) ([]Frame, error) {
	var out []Frame
	for len(b) > 0 {
		if len(b) < 16 {
			return nil, ErrBadBatch
		}
		seq := binary.LittleEndian.Uint64(b[0:8])
		typ := binary.LittleEndian.Uint32(b[8:12])
		n := binary.LittleEndian.Uint32(b[12:16])
		if n > MaxMessage || uint64(n) > uint64(len(b)-16) {
			return nil, ErrBadBatch
		}
		out = append(out, Frame{Seq: seq, Type: typ, Data: b[16 : 16+n]})
		b = b[16+n:]
	}
	return out, nil
}
