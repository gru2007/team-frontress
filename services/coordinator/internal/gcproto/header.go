package gcproto

// ProtoBufHeader is CMsgProtoBufHeader, confirmed field-for-field from
// src/gcsdk/steammessages.proto. It rides inside the ProtoBufMsgHeader_t
// framing (see envelope.go) on every protobuf GC message in both
// directions; the real client fills in client_steam_id/client_session_id on
// every outbound message and expects job_id_source/job_id_target back
// unmolested when it sent one (that is how it pairs a request with a reply).
type ProtoBufHeader struct {
	ClientSteamID   *uint64 // fixed64 = 1
	ClientSessionID *int32  // int32   = 2
	SourceAppID     *uint32 // uint32  = 3

	// JobIDSource/JobIDTarget default to 0xFFFFFFFFFFFFFFFF ("no job") in the
	// real proto. Leave nil to mean "not set / use the wire default"; the
	// dispatcher fills these in explicitly when it needs job correlation.
	JobIDSource *uint64 // fixed64 = 10
	JobIDTarget *uint64 // fixed64 = 11

	TargetJobName *string // string = 12
	EResult       *int32  // int32  = 13, default 2 (k_EResultOK... actually "OK"=1; Valve's default here is 2, keep as documented)
	ErrorMessage  *string // string = 14

	GCMsgSrc         *int32  // enum GCProtoBufMsgSrc = 200
	GCDirIndexSource *uint32 // uint32 = 201
}

// Marshal serializes the header.
func (h *ProtoBufHeader) Marshal() []byte {
	w := NewWriter()
	if h == nil {
		return w.Bytes()
	}
	w.OptFixed64(1, h.ClientSteamID)
	w.OptInt32(2, h.ClientSessionID)
	w.OptUint32(3, h.SourceAppID)
	w.OptFixed64(10, h.JobIDSource)
	w.OptFixed64(11, h.JobIDTarget)
	w.OptString(12, h.TargetJobName)
	w.OptInt32(13, h.EResult)
	w.OptString(14, h.ErrorMessage)
	w.OptEnum(200, h.GCMsgSrc)
	w.OptUint32(201, h.GCDirIndexSource)
	return w.Bytes()
}

// UnmarshalProtoBufHeader parses a serialized CMsgProtoBufHeader.
func UnmarshalProtoBufHeader(data []byte) (*ProtoBufHeader, error) {
	h := &ProtoBufHeader{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			h.ClientSteamID = &v
		case 2:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			h.ClientSessionID = &v
		case 3:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			h.SourceAppID = &v
		case 10:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			h.JobIDSource = &v
		case 11:
			v, err := r.Fixed64()
			if err != nil {
				return nil, err
			}
			h.JobIDTarget = &v
		case 12:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			h.TargetJobName = &v
		case 13:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			h.EResult = &v
		case 14:
			v, err := r.String()
			if err != nil {
				return nil, err
			}
			h.ErrorMessage = &v
		case 200:
			v, err := r.Int32()
			if err != nil {
				return nil, err
			}
			h.GCMsgSrc = &v
		case 201:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			h.GCDirIndexSource = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return h, nil
}

// NoJobID is the "no job" sentinel both job_id fields default to on the wire.
const NoJobID uint64 = 0xFFFFFFFFFFFFFFFF

// U64 is a small convenience for building *uint64 literals inline.
func U64(v uint64) *uint64 { return &v }

// I32 is a small convenience for building *int32 literals inline.
func I32(v int32) *int32 { return &v }

// U32 is a small convenience for building *uint32 literals inline.
func U32(v uint32) *uint32 { return &v }

// Str is a small convenience for building *string literals inline.
func Str(v string) *string { return &v }

// Bl is a small convenience for building *bool literals inline.
func Bl(v bool) *bool { return &v }

// F64 is a small convenience for building *float64 literals inline.
func F64(v float64) *float64 { return &v }
