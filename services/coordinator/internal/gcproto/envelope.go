package gcproto

import (
	"encoding/binary"
	"fmt"
)

// ProtoBufMsgHeader_t, confirmed byte-for-byte from src/public/gcsdk/gcmsg.h:
//
//	#pragma pack( push, 1 )
//	struct ProtoBufMsgHeader_t
//	{
//		int32  m_EMsgFlagged;         // eMsg | k_EMsgProtoBufFlag
//		uint32 m_cubProtoBufExtHdr;   // length of the serialized CMsgProtoBufHeader that follows
//	};
//	#pragma pack(pop)
//
// It is packed (no padding) and little-endian on the wire (x86/ARM native
// order, which is what the game ships on). Every message this coordinator's
// HTTP transport carries starts with these 8 bytes, then exactly
// m_cubProtoBufExtHdr bytes of serialized CMsgProtoBufHeader, then the
// serialized message body for the rest of the slot.
const envelopeHeaderSize = 8

// BuildMessage frames one GC message exactly as ProtoBufMsgHeader_t expects:
// flagged EMsg + header length + header bytes + body bytes.
func BuildMessage(eMsg EMsg, header *ProtoBufHeader, body []byte) []byte {
	hdrBytes := header.Marshal()

	out := make([]byte, envelopeHeaderSize+len(hdrBytes)+len(body))
	binary.LittleEndian.PutUint32(out[0:4], FlagProto(eMsg))
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(hdrBytes)))
	copy(out[8:8+len(hdrBytes)], hdrBytes)
	copy(out[8+len(hdrBytes):], body)
	return out
}

// ParsedMessage is one GC message read back out of ProtoBufMsgHeader_t
// framing.
type ParsedMessage struct {
	EMsg   EMsg
	Header *ProtoBufHeader
	Body   []byte
}

// ParseMessage decodes one ProtoBufMsgHeader_t-framed message. It requires
// the ProtoBufFlag bit to be set (k_EMsgFormatTypeProtocolBuffer) because
// every message this transport carries is protobuf; a struct-framed message
// here would mean a client this coordinator does not support.
func ParseMessage(data []byte) (*ParsedMessage, error) {
	if len(data) < envelopeHeaderSize {
		return nil, fmt.Errorf("gcproto: message shorter than ProtoBufMsgHeader_t (%d bytes)", len(data))
	}
	flagged := binary.LittleEndian.Uint32(data[0:4])
	cubHdr := binary.LittleEndian.Uint32(data[4:8])
	if flagged&ProtoBufFlag == 0 {
		return nil, fmt.Errorf("gcproto: EMsg 0x%x is not protobuf-flagged; struct messages are not supported", flagged)
	}
	rest := data[envelopeHeaderSize:]
	if uint64(cubHdr) > uint64(len(rest)) {
		return nil, fmt.Errorf("gcproto: header length %d exceeds remaining %d bytes", cubHdr, len(rest))
	}
	hdrBytes := rest[:cubHdr]
	body := rest[cubHdr:]

	hdr, err := UnmarshalProtoBufHeader(hdrBytes)
	if err != nil {
		return nil, fmt.Errorf("gcproto: bad CMsgProtoBufHeader: %w", err)
	}
	return &ParsedMessage{
		EMsg:   UnflagProto(flagged),
		Header: hdr,
		Body:   body,
	}, nil
}
