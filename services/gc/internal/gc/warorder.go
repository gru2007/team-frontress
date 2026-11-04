package gc

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"google.golang.org/protobuf/proto"
)

// The war's order: the operation headquarters wants finished, which is the one
// closest to taking its sector (furthest stage, then the most momentum spent,
// then the oldest). Every battle on it pays orderBonusXP to everyone in it.
const orderBonusXP = 100

func (g *GC) orderFront() string {
	if g.war == nil {
		return ""
	}
	fronts := g.war.Fronts()
	if len(fronts) == 0 {
		return ""
	}
	sort.SliceStable(fronts, func(i, j int) bool {
		a, b := fronts[i], fronts[j]
		if a.StageIndex != b.StageIndex {
			return a.StageIndex > b.StageIndex
		}
		if a.Momentum != b.Momentum {
			return a.Momentum < b.Momentum
		}
		return a.ID < b.ID
	})
	return fronts[0].ID
}

// A server is told what its battle is (msgMatchConditions, JSON in a
// CMsgPartyChatMsg, like the client's custom messages): the conditions it is
// fought under, which sector, which stage, who attacks. It execs
// cfg/frontress_cond_<id>.cfg for each condition once the match is up and
// tells its players the rest; see src/game/server/tf/frontress_conditions.cpp.
const msgMatchConditions uint32 = 51004

func (g *GC) sendConditions(srv *Server, l *Lobby) {
	if l.War == nil || srv == nil {
		return
	}
	w := l.War
	raw, err := json.Marshal(struct {
		MatchID     string   `json:"match_id"`
		Conditions  []string `json:"conditions"`
		Sector      string   `json:"sector"`
		Stage       int      `json:"stage"`
		Stages      int      `json:"stages"`
		Kind        string   `json:"kind"`
		Attacker    string   `json:"attacker"`      // the war side: red or blu
		AttackerTm  int32    `json:"attacker_team"` // the game team it wears: 2 or 3
		Order       bool     `json:"order"`
		UniformSwap bool     `json:"uniform_swap"`
	}{
		MatchID: strconv.FormatUint(l.MatchID, 10), Conditions: append([]string{}, w.Conditions...),
		Sector: w.NodeName, Stage: w.StageIndex + 1, Stages: w.StageCount, Kind: w.StageKind,
		Attacker: strings.ToLower(w.Attacker), AttackerTm: gameTeam(w.AttackerTeam), Order: w.Order,
		UniformSwap: w.UniformSwap,
	})
	if err != nil {
		return
	}
	g.send(srv.SteamID, msgMatchConditions, &gcpb.CMsgPartyChatMsg{Msg: proto.String(string(raw))})
}

// gameTeam is the TF2 team (2 RED, 3 BLU) for a lobby's GC team.
func gameTeam(gcTeam int32) int32 {
	if gcTeam == int32(mm.Red) {
		return 2
	}
	return 3
}
