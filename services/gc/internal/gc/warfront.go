package gc

import (
	"encoding/json"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// A player picks the operation they want to fight in on the war screen. The
// game sends the choice (msgWarFront, JSON {"front": "front-3"} in a
// CMsgPartyChatMsg, like msgSetLook); an empty front means "wherever I am
// needed". When the war forms its next battle it goes where most of the
// people being matched asked to go.
const msgWarFront uint32 = 51003

func init() {
	clientHandlers[msgWarFront] = func(g *GC, s *session.Session, m *wire.Msg) {
		var body gcpb.CMsgPartyChatMsg
		if !g.parse(m, &body) {
			return
		}
		var pick struct {
			Front string `json:"front"`
			Side  string `json:"side"`
		}
		if len(body.GetMsg()) > 256 || json.Unmarshal([]byte(body.GetMsg()), &pick) != nil {
			return
		}
		if pick.Front != "" && !g.frontOpen(pick.Front) {
			pick.Front = ""
		}
		if pick.Side != "" {
			g.chooseFaction(s.SteamID, pick.Side)
			return
		}
		g.player(s.SteamID).WarFront = pick.Front
		g.publishWar()
	}
}

func (g *GC) frontOpen(id string) bool {
	if g.war == nil {
		return false
	}
	for _, f := range g.war.Fronts() {
		if f.ID == id {
			return true
		}
	}
	return false
}

// frontVotes counts, per front, the queued players who asked for it.
func (g *GC) frontVotes(queue []mm.Entry) map[string]int {
	votes := map[string]int{}
	for _, e := range queue {
		for _, p := range e.Players {
			if pl := g.online[p.SteamID]; pl != nil && pl.WarFront != "" {
				votes[pl.WarFront]++
			}
		}
	}
	return votes
}
