package gcproto

import (
	"encoding/binary"
	"fmt"
)

// BatchMagic is FRONTRESS_GC_BATCH_MAGIC from src/game/shared/frontress/frontress_gc.h
// ('F','G','C','1' as a little-endian uint32).
const BatchMagic uint32 = 0x31434746

// batchHeaderSize is magic(4) + count(4).
const batchHeaderSize = 8

// EncodeBatch packs a set of already-framed GC messages (each one the output
// of BuildMessage) into one FGC1 batch body, per frontress_gc.h:
//
//	uint32 magic 'FGC1'
//	uint32 count
//	count x { uint32 cubMsg; uint8 rgubMsg[cubMsg] }
func EncodeBatch(msgs [][]byte) []byte {
	size := batchHeaderSize
	for _, m := range msgs {
		size += 4 + len(m)
	}
	out := make([]byte, size)
	binary.LittleEndian.PutUint32(out[0:4], BatchMagic)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(msgs)))
	off := batchHeaderSize
	for _, m := range msgs {
		binary.LittleEndian.PutUint32(out[off:off+4], uint32(len(m)))
		off += 4
		copy(out[off:], m)
		off += len(m)
	}
	return out
}

// EmptyBatch is the well-formed "nothing waiting" reply: magic + count(0),
// exactly 8 bytes. The client treats any response under 8 bytes as nothing
// to parse, and this parses to zero messages either way, so it is always
// safe to send.
func EmptyBatch() []byte { return EncodeBatch(nil) }

// DecodeBatch unpacks an FGC1 batch body into its raw per-message slices.
// Per the client's own contract, a body shorter than 8 bytes means nothing
// was sent and is not an error.
func DecodeBatch(data []byte) ([][]byte, error) {
	if len(data) < batchHeaderSize {
		return nil, nil
	}
	magic := binary.LittleEndian.Uint32(data[0:4])
	if magic != BatchMagic {
		return nil, fmt.Errorf("gcproto: bad batch magic 0x%x", magic)
	}
	count := binary.LittleEndian.Uint32(data[4:8])
	out := make([][]byte, 0, count)
	off := batchHeaderSize
	for i := uint32(0); i < count; i++ {
		if off+4 > len(data) {
			return nil, fmt.Errorf("gcproto: truncated batch at message %d/%d", i, count)
		}
		cub := binary.LittleEndian.Uint32(data[off : off+4])
		off += 4
		if uint64(off)+uint64(cub) > uint64(len(data)) {
			return nil, fmt.Errorf("gcproto: message %d/%d claims %d bytes, only %d remain", i, count, cub, len(data)-off)
		}
		out = append(out, data[off:off+int(cub)])
		off += int(cub)
	}
	return out, nil
}
