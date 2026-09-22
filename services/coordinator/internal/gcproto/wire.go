// Package gcproto is a hand-rolled, stdlib-only implementation of the wire
// formats the real Steam Game Coordinator protocol uses: raw protobuf
// encoding/decoding, the CMsgProtoBufHeader envelope, the ProtoBufMsgHeader_t
// framing Valve's GCSDK puts on every message, and the FGC1 batch format this
// coordinator's HTTP long-poll transport uses to carry those messages.
//
// There is no protoc-generated code here. Every field layout below was
// confirmed against the leaked Source SDK this project forks from:
//   - src/public/gcsdk/msgbase.h (k_EMsgProtoBufFlag)
//   - src/public/gcsdk/gcmsg.h (ProtoBufMsgHeader_t)
//   - src/gcsdk/steammessages.proto (CMsgProtoBufHeader)
//   - src/gcsdk/gcsdk_gcmessages.proto (CMsgSOCacheSubscribed and friends)
//   - src/gcsdk/gcsystemmsgs.proto (EGCBaseClientMsg, ESOMsg)
//   - src/game/shared/tf/tf_gcmessages.proto (party/lobby/matchmaking)
//   - src/game/shared/tf/tf_gcmessages.h (EGCTFProtoObjectTypes, the SO
//     cache type_id values for CSOTFParty/CSOTFPartyInvite/CSOTFGameServerLobby)
//
// A real compiled GCSDK client does not care how these bytes were produced,
// only that they are legal protobuf and match the field numbers/wire types
// its own generated code expects. That is what this package guarantees.
package gcproto

import (
	"encoding/binary"
	"errors"
	"math"
	"reflect"
)

// Protobuf wire types (the low 3 bits of every tag).
const (
	WireVarint  = 0
	WireFixed64 = 1
	WireBytes   = 2
	WireFixed32 = 5
)

var (
	ErrTruncated = errors.New("gcproto: truncated message")
	ErrOverflow  = errors.New("gcproto: varint overflow")
)

// ---------------------------------------------------------------------------
// Writer
// ---------------------------------------------------------------------------

// Writer builds a serialized protobuf message one field at a time, in field
// order (real protobuf does not require field order, but writing in
// ascending order is what protoc-gen-go does and keeps output diffable).
type Writer struct {
	buf []byte
}

// NewWriter returns an empty Writer.
func NewWriter() *Writer { return &Writer{} }

// Bytes returns the serialized message so far. Do not mutate it.
func (w *Writer) Bytes() []byte { return w.buf }

// Len is the number of bytes written so far.
func (w *Writer) Len() int { return len(w.buf) }

func (w *Writer) putVarint(v uint64) {
	var tmp [10]byte
	n := binary.PutUvarint(tmp[:], v)
	w.buf = append(w.buf, tmp[:n]...)
}

func (w *Writer) putTag(field int, wireType int) {
	w.putVarint(uint64(field)<<3 | uint64(wireType))
}

// VarintField writes a raw varint-typed field unconditionally. Prefer the
// Opt* helpers below for proto2 "optional" semantics.
func (w *Writer) VarintField(field int, v uint64) {
	w.putTag(field, WireVarint)
	w.putVarint(v)
}

// Fixed64Field writes a little-endian 8-byte field (fixed64/sfixed64/double).
func (w *Writer) Fixed64Field(field int, v uint64) {
	w.putTag(field, WireFixed64)
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], v)
	w.buf = append(w.buf, tmp[:]...)
}

// Fixed32Field writes a little-endian 4-byte field (fixed32/sfixed32/float).
func (w *Writer) Fixed32Field(field int, v uint32) {
	w.putTag(field, WireFixed32)
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	w.buf = append(w.buf, tmp[:]...)
}

// BytesField writes a length-delimited field: raw bytes, a string, or an
// embedded message that has already been marshaled.
func (w *Writer) BytesField(field int, v []byte) {
	w.putTag(field, WireBytes)
	w.putVarint(uint64(len(v)))
	w.buf = append(w.buf, v...)
}

// --- optional-scalar helpers (proto2 "optional": omit entirely when nil) ---

func (w *Writer) OptUint32(field int, v *uint32) {
	if v != nil {
		w.VarintField(field, uint64(*v))
	}
}
func (w *Writer) OptInt32(field int, v *int32) {
	if v != nil {
		w.VarintField(field, uint64(uint32(*v)))
	}
}
func (w *Writer) OptUint64(field int, v *uint64) {
	if v != nil {
		w.VarintField(field, *v)
	}
}
func (w *Writer) OptBool(field int, v *bool) {
	if v != nil {
		b := uint64(0)
		if *v {
			b = 1
		}
		w.VarintField(field, b)
	}
}
func (w *Writer) OptEnum(field int, v *int32) { w.OptInt32(field, v) }

func (w *Writer) OptFixed64(field int, v *uint64) {
	if v != nil {
		w.Fixed64Field(field, *v)
	}
}
func (w *Writer) OptFixed32(field int, v *uint32) {
	if v != nil {
		w.Fixed32Field(field, *v)
	}
}
func (w *Writer) OptDouble(field int, v *float64) {
	if v != nil {
		w.Fixed64Field(field, math.Float64bits(*v))
	}
}
func (w *Writer) OptFloat(field int, v *float32) {
	if v != nil {
		w.Fixed32Field(field, math.Float32bits(*v))
	}
}
func (w *Writer) OptString(field int, v *string) {
	if v != nil {
		w.BytesField(field, []byte(*v))
	}
}
func (w *Writer) OptBytes(field int, v []byte) {
	if v != nil {
		w.BytesField(field, v)
	}
}

// Message is anything that can serialize itself, so embedded/nested messages
// can be written generically.
type Message interface{ Marshal() []byte }

// isNilMessage reports whether v is a nil pointer wearing a Message
// interface. A plain `v != nil` on the interface is not enough: a struct
// field like `*PerPlayerMatchCriteria` that is nil still produces a
// *non-nil* Message interface value (concrete type set, pointer value nil),
// so `v != nil` is true and v.Marshal() panics dereferencing a nil
// receiver. Every message type here is a pointer, so a reflect-based check
// is the one place that needs to know that, instead of every call site
// having to guard it by hand.
func isNilMessage(v Message) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Ptr && rv.IsNil()
}

// OptMessage writes a nested message field, omitted when v is nil (including
// a typed-nil pointer -- see isNilMessage).
func OptMessage(w *Writer, field int, v Message) {
	if !isNilMessage(v) {
		w.BytesField(field, v.Marshal())
	}
}

// RepeatedMessage writes one length-delimited entry per element, skipping
// any nil entries rather than panicking on them.
func RepeatedMessage[T Message](w *Writer, field int, v []T) {
	for _, m := range v {
		if isNilMessage(m) {
			continue
		}
		w.BytesField(field, m.Marshal())
	}
}

// RepeatedUint64 writes a repeated non-packed varint field (matches how the
// real GC encodes repeated fixed64/uint64 ID lists in these messages).
func RepeatedUint64(w *Writer, field int, v []uint64) {
	for _, x := range v {
		w.VarintField(field, x)
	}
}

// RepeatedFixed64 writes a repeated fixed64 field.
func RepeatedFixed64(w *Writer, field int, v []uint64) {
	for _, x := range v {
		w.Fixed64Field(field, x)
	}
}

// RepeatedString writes a repeated string field.
func RepeatedString(w *Writer, field int, v []string) {
	for _, s := range v {
		w.BytesField(field, []byte(s))
	}
}

// RepeatedUint32 writes a repeated non-packed varint field of uint32s.
func RepeatedUint32(w *Writer, field int, v []uint32) {
	for _, x := range v {
		w.VarintField(field, uint64(x))
	}
}

// RepeatedFixed32 writes a repeated fixed32 field.
func RepeatedFixed32(w *Writer, field int, v []uint32) {
	for _, x := range v {
		w.Fixed32Field(field, x)
	}
}

// RepeatedBytes writes a repeated bytes field, one length-delimited entry per
// element (used by CMsgSOCacheSubscribed.SubscribedType.object_data).
func RepeatedBytes(w *Writer, field int, v [][]byte) {
	for _, b := range v {
		w.BytesField(field, b)
	}
}

// ---------------------------------------------------------------------------
// Reader
// ---------------------------------------------------------------------------

// Reader walks a serialized protobuf message field by field.
type Reader struct {
	b []byte
	i int
}

// NewReader wraps b for reading. b is not copied; do not mutate it while the
// Reader is in use.
func NewReader(b []byte) *Reader { return &Reader{b: b} }

// Len is how many bytes remain unread.
func (r *Reader) Len() int { return len(r.b) - r.i }

func (r *Reader) readVarint() (uint64, error) {
	var v uint64
	var shift uint
	for {
		if r.i >= len(r.b) {
			return 0, ErrTruncated
		}
		b := r.b[r.i]
		r.i++
		if shift >= 64 {
			return 0, ErrOverflow
		}
		v |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, nil
		}
		shift += 7
	}
}

// Tag reads the next field tag: field number and wire type.
func (r *Reader) Tag() (field int, wireType int, err error) {
	v, err := r.readVarint()
	if err != nil {
		return 0, 0, err
	}
	return int(v >> 3), int(v & 0x7), nil
}

// Varint reads a raw varint-encoded value.
func (r *Reader) Varint() (uint64, error) { return r.readVarint() }

// Fixed64 reads 8 little-endian bytes.
func (r *Reader) Fixed64() (uint64, error) {
	if r.i+8 > len(r.b) {
		return 0, ErrTruncated
	}
	v := binary.LittleEndian.Uint64(r.b[r.i : r.i+8])
	r.i += 8
	return v, nil
}

// Fixed32 reads 4 little-endian bytes.
func (r *Reader) Fixed32() (uint32, error) {
	if r.i+4 > len(r.b) {
		return 0, ErrTruncated
	}
	v := binary.LittleEndian.Uint32(r.b[r.i : r.i+4])
	r.i += 4
	return v, nil
}

// Bytes reads a length-delimited field's payload (raw bytes, string, or an
// embedded message still in serialized form).
func (r *Reader) Bytes() ([]byte, error) {
	n, err := r.readVarint()
	if err != nil {
		return nil, err
	}
	if uint64(r.i)+n > uint64(len(r.b)) {
		return nil, ErrTruncated
	}
	v := r.b[r.i : r.i+int(n)]
	r.i += int(n)
	return v, nil
}

// String reads a length-delimited field as a string.
func (r *Reader) String() (string, error) {
	b, err := r.Bytes()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Bool reads a varint field as a bool (nonzero == true).
func (r *Reader) Bool() (bool, error) {
	v, err := r.readVarint()
	return v != 0, err
}

// Uint32 reads a varint field truncated to 32 bits.
func (r *Reader) Uint32() (uint32, error) {
	v, err := r.readVarint()
	return uint32(v), err
}

// Int32 reads a varint field as a signed 32-bit value (protobuf int32 wire
// encoding sign-extends through the varint, so this is a plain truncation).
func (r *Reader) Int32() (int32, error) {
	v, err := r.readVarint()
	return int32(uint32(v)), err
}

// Double reads a fixed64 field as an IEEE-754 double.
func (r *Reader) Double() (float64, error) {
	v, err := r.Fixed64()
	return math.Float64frombits(v), err
}

// Float reads a fixed32 field as an IEEE-754 float.
func (r *Reader) Float() (float32, error) {
	v, err := r.Fixed32()
	return math.Float32frombits(v), err
}

// Skip discards the value belonging to a tag whose wire type is wireType,
// for fields the caller does not recognize. Every message decoder must call
// this on unknown fields instead of erroring, exactly like a real protobuf
// parser -- newer clients may send fields this coordinator does not yet
// implement, and it must not choke on them.
func (r *Reader) Skip(wireType int) error {
	switch wireType {
	case WireVarint:
		_, err := r.readVarint()
		return err
	case WireFixed64:
		_, err := r.Fixed64()
		return err
	case WireFixed32:
		_, err := r.Fixed32()
		return err
	case WireBytes:
		_, err := r.Bytes()
		return err
	default:
		return errors.New("gcproto: unknown wire type")
	}
}
