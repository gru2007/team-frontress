package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
)

const (
	msgSocial      = 51010
	msgSocialEvent = 51011
)

func (p *peer) op(v map[string]any) {
	p.t.Helper()
	raw, _ := json.Marshal(v)
	p.send(msgSocial, &gcpb.CMsgPartyChatMsg{Msg: proto.String(string(raw))})
}

// events is every social event of a kind the peer got, decoded, oldest first.
func (p *peer) events(kind string) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []map[string]any
	for _, m := range p.inbox {
		if m.Type != msgSocialEvent {
			continue
		}
		var body gcpb.CMsgPartyChatMsg
		if proto.Unmarshal(m.Body, &body) != nil {
			continue
		}
		ev := map[string]any{}
		if json.Unmarshal([]byte(body.GetMsg()), &ev) == nil && ev["t"] == kind {
			out = append(out, ev)
		}
	}
	return out
}

func (p *peer) last(kind string) map[string]any {
	evs := p.events(kind)
	if len(evs) == 0 {
		return nil
	}
	return evs[len(evs)-1]
}

// chatText is everything said or announced in a channel, in order.
func (p *peer) chatText(ch string) []string {
	var out []string
	for _, ev := range p.events("chat") {
		lines, _ := ev["lines"].([]any)
		for _, l := range lines {
			line := l.(map[string]any)
			if line["ch"] != ch {
				continue
			}
			if txt, ok := line["text"].(string); ok && txt != "" {
				out = append(out, txt)
			} else if key, ok := line["key"].(string); ok {
				out = append(out, key)
			}
		}
	}
	return out
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Global chat reaches everyone online, keeps a backlog for whoever arrives
// later, is rate limited, and the online list shows who is looking for a squad.
func TestGlobalChatAndPresence(t *testing.T) {
	_, base, stop := startGC(t, nil)
	defer stop()

	a, b := newPeer(t, base, alice), newPeer(t, base, bob)
	a.client()
	b.client()
	a.op(map[string]any{"op": "hello", "name": "Alice"})
	b.op(map[string]any{"op": "hello", "name": "Bob"})
	a.until("the online list", func() bool { return a.last("online") != nil })

	a.op(map[string]any{"op": "say", "ch": "global", "text": "  кто   на 2fort? \x07"})
	b.until("alice's line", func() bool { return has(b.chatText("global"), "кто на 2fort?") })

	// Carol arrives later and sees it in the backlog.
	c := newPeer(t, base, carol)
	c.client()
	c.op(map[string]any{"op": "hello", "name": "Carol"})
	c.until("the backlog", func() bool { return has(c.chatText("global"), "кто на 2fort?") })

	// Bob looks for a squad; the online list puts him first.
	b.op(map[string]any{"op": "lfg", "on": true, "note": "медик ищет отряд"})
	c.until("bob looking for a squad", func() bool {
		ev := c.last("online")
		if ev == nil {
			return false
		}
		on := ev["online"].(map[string]any)
		players := on["players"].([]any)
		first := players[0].(map[string]any)
		return on["lfg"] == float64(1) && first["name"] == "Bob" && first["lfg"] == true && on["total"] == float64(3)
	})

	// A burst past the limit is refused.
	for i := 0; i < 8; i++ {
		a.op(map[string]any{"op": "say", "ch": "global", "text": "spam"})
	}
	a.until("being slowed down", func() bool {
		for _, ev := range a.events("error") {
			if ev["key"] == "fui_err_chat_slow" {
				return true
			}
		}
		return false
	})
}

// A clan: founded, joined, its own channel, its tag on lines and in looks.
func TestClans(t *testing.T) {
	_, base, stop := startGC(t, nil)
	defer stop()

	a, b := newPeer(t, base, alice), newPeer(t, base, bob)
	a.client()
	b.client()
	a.op(map[string]any{"op": "hello", "name": "Alice"})
	b.op(map[string]any{"op": "hello", "name": "Bob"})

	a.op(map[string]any{"op": "clan_create", "name": "Ржавые Ключи", "tag": "rk", "emblem": "engineer", "color": "gold", "open": true})
	a.until("the clan", func() bool {
		ev := a.last("clan")
		return ev != nil && ev["clan"] != nil
	})
	clan := a.last("clan")["clan"].(map[string]any)
	if clan["tag"] != "RK" || clan["role"] != "leader" || clan["level"] != float64(1) {
		t.Fatalf("clan %v", clan)
	}
	b.until("the founding in the feed", func() bool { return has(b.chatText("global"), "fui_feed_clan_new") })

	// Bob finds it in the list and joins.
	b.op(map[string]any{"op": "clan_browse"})
	b.until("the clan list", func() bool {
		ev := b.last("clan")
		top, _ := ev["top"].([]any)
		return ev != nil && len(top) == 1
	})
	b.op(map[string]any{"op": "clan_join", "id": clan["id"]})
	a.until("two members", func() bool {
		ev := a.last("clan")
		c, _ := ev["clan"].(map[string]any)
		return c != nil && len(c["members"].([]any)) == 2
	})

	// The clan channel reaches the clan, with the tag on the line.
	b.op(map[string]any{"op": "say", "ch": "clan", "text": "за мной"})
	a.until("bob in the clan channel", func() bool { return has(a.chatText("clan"), "за мной") })
	for _, ev := range a.events("chat") {
		for _, l := range ev["lines"].([]any) {
			line := l.(map[string]any)
			if line["text"] == "за мной" && line["tag"] != "RK" {
				t.Fatalf("line without the tag: %v", line)
			}
		}
	}

	// A taken tag is refused with a reason the game can show.
	c := newPeer(t, base, carol)
	c.client()
	c.op(map[string]any{"op": "clan_create", "name": "Other", "tag": "RK"})
	c.until("the refusal", func() bool {
		ev := c.last("error")
		return ev != nil && strings.HasSuffix(ev["key"].(string), "name_taken")
	})
}

// The support tab with fake Steam: prices in the player's currency, a
// donation goes through, the supporter badge and the feed follow.
func TestSupportFake(t *testing.T) {
	cfg := testConfig()
	cfg.Support.Enabled, cfg.Support.Fake = true, true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	_, base, stop := startGCWith(t, cfg, nil)
	defer stop()

	a, b := newPeer(t, base, alice), newPeer(t, base, bob)
	a.client()
	b.client()
	a.op(map[string]any{"op": "hello", "name": "Alice"})
	b.op(map[string]any{"op": "hello", "name": "Bob"})

	a.op(map[string]any{"op": "support_info"})
	a.until("the support tab", func() bool {
		ev := a.last("support")
		return ev != nil && ev["available"] == true
	})
	doc := a.last("support")
	once := doc["once"].([]any)
	if doc["currency"] != "RUB" || len(once) != 4 || once[0].(map[string]any)["price"] != "149 ₽" {
		t.Fatalf("support doc %v", doc)
	}

	a.op(map[string]any{"op": "support_buy", "kind": "monthly", "tier": 0})
	a.until("the donation", func() bool {
		ev := a.last("support_result")
		return ev != nil && ev["ok"] == true
	})
	a.until("the supporter badge", func() bool {
		ev := a.last("supporter")
		return ev != nil && ev["supporter"] == true && ev["monthly"] == true
	})
	b.until("the thanks in the feed", func() bool { return has(b.chatText("global"), "fui_feed_support_monthly") })

	a.op(map[string]any{"op": "support_cancel"})
	a.until("the subscription stopped", func() bool {
		ev := a.last("supporter")
		return ev != nil && ev["monthly"] == false
	})
}
