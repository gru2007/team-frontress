package gc

import (
	"encoding/json"
	"strconv"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
	"google.golang.org/protobuf/proto"
)

// Looks: how each player appears in the party lobby, so everyone sees their
// party members as the class and in the items they chose. The game sends its
// own (msgSetLook) whenever it changes; the GC hands every member the whole
// party's (msgPartyLooks). Both carry JSON in a CMsgPartyChatMsg, like
// msgNotice. Looks live in memory only: a reconnecting game sends its again.
const (
	msgSetLook    uint32 = 51001
	msgPartyLooks uint32 = 51002
)

// Look is what the game says it looks like. The GC only checks its shape;
// item definitions are the game's business.
type Look struct {
	Class string     `json:"class"`
	Team  string     `json:"team,omitempty"`
	Items []LookItem `json:"items,omitempty"`
	// Filled in by the GC when it hands looks out: the clan tag and whether
	// they support the developers, for the nameplates.
	Tag  string `json:"tag,omitempty"`
	Gold bool   `json:"gold,omitempty"`
	Sup  bool   `json:"sup,omitempty"`
}

// LookItem is one item worn, with what makes it look like itself: a war
// paint and its wear, a paint can, an unusual effect, a killstreak sheen,
// australium, a style, festive lights. Zero is "none" for each.
type LookItem struct {
	Def        uint16  `json:"d"`
	Quality    uint8   `json:"q,omitempty"`
	PaintKit   uint32  `json:"pk,omitempty"`
	Wear       float32 `json:"w,omitempty"`
	Seed       uint64  `json:"sd,omitempty,string"`
	Paint      uint32  `json:"p,omitempty"`
	Paint2     uint32  `json:"p2,omitempty"`
	Unusual    uint32  `json:"u,omitempty"`
	Sheen      uint32  `json:"ks,omitempty"`
	Australium bool    `json:"au,omitempty"`
	Festive    bool    `json:"f,omitempty"`
	Style      uint8   `json:"st,omitempty"`
}

// clean bounds a look item to values the game can use.
func (it *LookItem) clean() {
	if it.Wear < 0 || it.Wear > 1 {
		it.Wear = 0
	}
	it.Paint &= 0xffffff
	it.Paint2 &= 0xffffff
	if it.Unusual > 100000 {
		it.Unusual = 0
	}
	if it.Sheen > 16 {
		it.Sheen = 0
	}
	if it.PaintKit > 1000000 {
		it.PaintKit = 0
	}
}

var lookClasses = map[string]bool{
	"scout": true, "soldier": true, "pyro": true, "demoman": true, "heavy": true,
	"engineer": true, "medic": true, "sniper": true, "spy": true,
}

func init() {
	clientHandlers[msgSetLook] = func(g *GC, s *session.Session, m *wire.Msg) {
		var body gcpb.CMsgPartyChatMsg
		if !g.parse(m, &body) {
			return
		}
		var look Look
		if len(body.GetMsg()) > 2048 || json.Unmarshal([]byte(body.GetMsg()), &look) != nil || !lookClasses[look.Class] {
			g.log.Debug("looks: bad look", "steamid", s.SteamID)
			return
		}
		if len(look.Items) > 16 {
			look.Items = look.Items[:16]
		}
		for i := range look.Items {
			look.Items[i].clean()
		}
		look.Tag, look.Gold, look.Sup = "", false, false
		if look.Team != "blu" {
			look.Team = "red"
		}
		p := g.player(s.SteamID)
		p.Look = &look
		if party := g.parties[p.PartyID]; party != nil && party.has(s.SteamID) {
			g.publishLooks(party)
		} else {
			g.sendLooks(s.SteamID, []uint64{s.SteamID})
		}
	}
}

// publishLooks sends every member of a party everyone's look.
func (g *GC) publishLooks(p *Party) {
	for _, m := range p.Members {
		g.sendLooks(m, p.Members)
	}
}

func (g *GC) sendLooks(to uint64, members []uint64) {
	if !g.isOnline(to) {
		return
	}
	looks := map[string]*Look{}
	for _, id := range members {
		if pl := g.online[id]; pl != nil && pl.Look != nil {
			l := *pl.Look
			if g.clans != nil {
				l.Tag, _, l.Gold = g.clans.TagOf(id)
			}
			if g.book != nil {
				l.Sup, _ = g.book.IsSupporter(id)
			}
			looks[strconv.FormatUint(id, 10)] = &l
		}
	}
	raw, err := json.Marshal(looks)
	if err != nil {
		return
	}
	g.send(to, msgPartyLooks, &gcpb.CMsgPartyChatMsg{Msg: proto.String(string(raw))})
}
