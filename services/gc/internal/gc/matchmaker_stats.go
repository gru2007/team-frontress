package gc

import (
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func init() {
	onClient(gcpb.ETFGCMsg_k_EMsgGC_TFClientInit, (*GC).handleClientInit)
	onClient(gcpb.ETFGCMsg_k_EMsgGCRequestMatchMakerStats, (*GC).handleMatchmakerStats)
	onClient(gcpb.ETFGCMsg_k_EMsgGCDataCenterPing_Update, (*GC).handleDataCenterPings)
}

func (g *GC) handleClientInit(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgTFClientInit
	if g.parse(m, &msg) {
		g.sendMatchmakerStats(s, m)
	}
}

func (g *GC) handleMatchmakerStats(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGCRequestMatchMakerStats
	if g.parse(m, &msg) {
		g.sendMatchmakerStats(s, m)
	}
}

func (g *GC) handleDataCenterPings(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGCDataCenterPing_Update
	if !g.parse(m, &msg) {
		return
	}
	bestPing := uint32(0)
	bestCenter := ""
	for _, entry := range msg.GetPingdata() {
		center := strings.ToLower(entry.GetName())
		if len(center) == 0 || len(center) > 8 || strings.IndexFunc(center, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) >= 0 || entry.Ping == nil || entry.GetPing() > 5000 ||
			entry.GetPingStatus() != gcpb.CMsgGCDataCenterPing_Update_Normal {
			continue
		}
		if bestCenter == "" || entry.GetPing() < bestPing || (entry.GetPing() == bestPing && center < bestCenter) {
			bestCenter, bestPing = center, entry.GetPing()
		}
	}
	g.player(s.SteamID).BestDataCenter = bestCenter
}

func (g *GC) sendMatchmakerStats(s *session.Session, req *wire.Msg) {
	now := g.now()
	if g.matchmakerStats != nil && now.Sub(g.matchmakerStatsAt) < time.Second {
		g.reply(s, req, uint32(gcpb.ETFGCMsg_k_EMsgGCMatchMakerStatsResponse), g.matchmakerStats)
		return
	}
	const groups = int(gcpb.ETFMatchGroup_k_eTFMatchGroup_Event_Last) + 1
	const maxMapIndex = 65535
	response := &gcpb.CMsgGCMatchMakerStatsResponse{}
	mapCount := 0
	for _, idx := range g.cfg.MapIndex {
		if idx <= maxMapIndex {
			mapCount = max(mapCount, int(idx)+1)
		}
	}
	response.MapCount = make([]uint32, mapCount)
	response.MatchgroupDataCenterPopulation = make([]*gcpb.CMsgGCMatchGroupDataCenterPopulation, groups)
	for i := range response.MatchgroupDataCenterPopulation {
		response.MatchgroupDataCenterPopulation[i] = &gcpb.CMsgGCMatchGroupDataCenterPopulation{}
	}
	// A player searching multiple casual groups counts once per map.
	mapPlayers := map[uint32]map[uint64]bool{}
	for _, grp := range g.cfg.MatchGroups {
		if !grp.Enabled || grp.MatchGroup < 0 || grp.MatchGroup >= int32(groups) {
			continue
		}
		counts := map[string]int{}
		population := 0
		casual := grp.MatchGroup >= int32(gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_First) &&
			grp.MatchGroup <= int32(gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_Last)
		maps := grp.EffectiveMaps()
		for _, party := range g.queueFor(grp) {
			for _, player := range party.Players {
				if center := g.online[player.SteamID].BestDataCenter; center != "" {
					counts[center]++
					population++
				}
				if !casual {
					continue
				}
				for _, name := range maps {
					index, ok := g.cfg.MapIndex[name]
					if !ok || index > maxMapIndex || (len(party.Maps) != 0 && !slices.Contains(party.Maps, name)) {
						continue
					}
					if mapPlayers[index] == nil {
						mapPlayers[index] = map[uint64]bool{}
					}
					mapPlayers[index][player.SteamID] = true
				}
			}
		}
		centers := response.MatchgroupDataCenterPopulation[grp.MatchGroup]
		for name, count := range counts {
			centers.DataCenterPopulation = append(centers.DataCenterPopulation, &gcpb.CMsgGCDataCenterPopulation{
				Name: proto.String(name), HealthRatio: proto.Float32(float32(count) / float32(population)),
			})
		}
		sort.Slice(centers.DataCenterPopulation, func(i, j int) bool {
			return centers.DataCenterPopulation[i].GetName() < centers.DataCenterPopulation[j].GetName()
		})
	}
	for index, players := range mapPlayers {
		response.MapCount[index] = uint32(len(players))
	}
	g.matchmakerStats, g.matchmakerStatsAt = response, now
	g.reply(s, req, uint32(gcpb.ETFGCMsg_k_EMsgGCMatchMakerStatsResponse), response)
}
