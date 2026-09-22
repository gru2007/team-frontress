package gcproto

// ClientHello is CMsgClientHello (base_gcmessages.proto:119), sent
// client -> GC as EMsgGCClientHello (4006) to open a session.
type ClientHello struct {
	Version *uint32 // uint32 = 1
}

func (m *ClientHello) Marshal() []byte {
	w := NewWriter()
	w.OptUint32(1, m.Version)
	return w.Bytes()
}

func UnmarshalClientHello(data []byte) (*ClientHello, error) {
	m := &ClientHello{}
	r := NewReader(data)
	for r.Len() > 0 {
		field, wt, err := r.Tag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			v, err := r.Uint32()
			if err != nil {
				return nil, err
			}
			m.Version = &v
		default:
			if err := r.Skip(wt); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// ClientWelcome is CMsgClientWelcome (base_gcmessages.proto:135), sent
// GC -> client as EMsgGCClientWelcome (4004) in reply to ClientHello. It is
// the session's "you're in" -- everything after this (party/lobby SO cache
// subscriptions) assumes the client received one.
//
// game_data is an opaque per-title payload in the real protocol (TF2 does
// not appear to populate it meaningfully for this handshake); this
// coordinator leaves it empty rather than inventing a schema for it.
type ClientWelcome struct {
	Version        *uint32 // uint32 = 1
	GameData       []byte  // bytes  = 2
	TxnCountryCode *string // string = 3
}

func (m *ClientWelcome) Marshal() []byte {
	w := NewWriter()
	w.OptUint32(1, m.Version)
	w.OptBytes(2, m.GameData)
	w.OptString(3, m.TxnCountryCode)
	return w.Bytes()
}
