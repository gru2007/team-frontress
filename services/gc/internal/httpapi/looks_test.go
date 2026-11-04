package httpapi_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"google.golang.org/protobuf/proto"
)

const (
	msgSetLook    = 51001
	msgPartyLooks = 51002
)

// lastLooks is the newest party looks message a peer has, decoded.
func (p *peer) lastLooks() map[string]map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.inbox) - 1; i >= 0; i-- {
		if p.inbox[i].Type != msgPartyLooks {
			continue
		}
		var body gcpb.CMsgPartyChatMsg
		if proto.Unmarshal(p.inbox[i].Body, &body) != nil {
			return nil
		}
		out := map[string]map[string]any{}
		if json.Unmarshal([]byte(body.GetMsg()), &out) != nil {
			return nil
		}
		return out
	}
	return nil
}

// A player's look comes back to them (and would go to their party), and a
// malformed one is dropped.
func TestPartyLooks(t *testing.T) {
	_, base, stop := startGC(t, nil)
	defer stop()

	a := newPeer(t, base, alice)
	a.client()
	a.until("party", func() bool { return a.party() != nil })

	a.send(msgSetLook, &gcpb.CMsgPartyChatMsg{Msg: proto.String(`{"class":"wizard"}`)})
	a.send(msgSetLook, &gcpb.CMsgPartyChatMsg{Msg: proto.String(`{"class":"soldier","team":"blu","items":[{"d":18},{"d":15006,"pk":350,"w":0.2,"u":701}]}`)})
	me := strconv.FormatUint(alice, 10)
	a.until("own look", func() bool {
		looks := a.lastLooks()
		return looks != nil && looks[me] != nil
	})
	look := a.lastLooks()[me]
	if look["class"] != "soldier" || look["team"] != "blu" {
		t.Fatalf("look %v", look)
	}
	if items, _ := look["items"].([]any); len(items) != 2 {
		t.Fatalf("items %v", look["items"])
	}
}
