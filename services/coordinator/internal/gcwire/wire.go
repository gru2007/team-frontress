// Package gcwire implements the small amount of Steam GC framing used by the
// Frontress coordinator. Message bodies remain Valve protobufs; this package
// only owns the eight-byte protobuf envelope and the HTTP transport envelope.
package gcwire

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"google.golang.org/protobuf/proto"
)

const (
	ProtoMask      uint32 = 0x80000000
	MaxMessages           = 256
	MaxPacketBytes        = 256 << 10
	MaxBatchBytes         = 768 << 10
)

var ErrMalformedPacket = errors.New("malformed GC packet")

// Message is one complete Steam GC packet encoded for JSON transport.
type Message struct {
	Type uint32 `json:"type"`
	Data string `json:"data"`
}

type ExchangeRequest struct {
	Protocol          int       `json:"protocol"`
	SessionID         string    `json:"session_id,omitempty"`
	InstanceID        string    `json:"instance_id,omitempty"`
	ClientSequence    uint64    `json:"client_sequence,omitempty"`
	AckServerSequence uint64    `json:"ack_server_sequence,omitempty"`
	Role              string    `json:"role"`
	SteamID           string    `json:"steam_id,omitempty"`
	Ticket            string    `json:"ticket,omitempty"`
	ServerToken       string    `json:"server_token,omitempty"`
	MatchID           string    `json:"match_id,omitempty"`
	Messages          []Message `json:"messages,omitempty"`
}

type ExchangeResponse struct {
	SessionID      string    `json:"session_id"`
	Connected      bool      `json:"connected"`
	PollAfter      int       `json:"poll_after_ms"`
	ClientSequence uint64    `json:"client_sequence,omitempty"`
	ServerSequence uint64    `json:"server_sequence,omitempty"`
	Messages       []Message `json:"messages,omitempty"`
}

type Packet struct {
	Type   uint32
	Header *gcproto.CMsgProtoBufHeader
	Body   []byte
}

func Decode(m Message) (Packet, error) {
	if len(m.Data) == 0 || base64.StdEncoding.DecodedLen(len(m.Data)) > MaxPacketBytes {
		return Packet{}, fmt.Errorf("%w: packet too large", ErrMalformedPacket)
	}
	raw, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil {
		return Packet{}, fmt.Errorf("%w: base64: %v", ErrMalformedPacket, err)
	}
	if len(raw) < 8 {
		return Packet{}, ErrMalformedPacket
	}
	typ := binary.LittleEndian.Uint32(raw[:4])
	if typ&ProtoMask == 0 || typ&^ProtoMask != m.Type&^ProtoMask {
		return Packet{}, fmt.Errorf("%w: message type mismatch", ErrMalformedPacket)
	}
	headerLen := int(binary.LittleEndian.Uint32(raw[4:8]))
	if headerLen < 0 || headerLen > len(raw)-8 {
		return Packet{}, ErrMalformedPacket
	}
	header := new(gcproto.CMsgProtoBufHeader)
	if err := proto.Unmarshal(raw[8:8+headerLen], header); err != nil {
		return Packet{}, fmt.Errorf("%w: header: %v", ErrMalformedPacket, err)
	}
	return Packet{Type: typ &^ ProtoMask, Header: header, Body: raw[8+headerLen:]}, nil
}

func Encode(typ uint32, header *gcproto.CMsgProtoBufHeader, body proto.Message) (Message, error) {
	if header == nil {
		header = new(gcproto.CMsgProtoBufHeader)
	}
	h, err := proto.Marshal(header)
	if err != nil {
		return Message{}, err
	}
	b, err := proto.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	raw := make([]byte, 8+len(h)+len(b))
	binary.LittleEndian.PutUint32(raw[:4], typ|ProtoMask)
	binary.LittleEndian.PutUint32(raw[4:8], uint32(len(h)))
	copy(raw[8:], h)
	copy(raw[8+len(h):], b)
	return Message{Type: typ, Data: base64.StdEncoding.EncodeToString(raw)}, nil
}

// ReplyHeader preserves Valve's reliable-message correlation contract.
func ReplyHeader(request *gcproto.CMsgProtoBufHeader) *gcproto.CMsgProtoBufHeader {
	h := new(gcproto.CMsgProtoBufHeader)
	if request != nil && request.JobIdSource != nil {
		v := request.GetJobIdSource()
		h.JobIdTarget = &v
	}
	return h
}
