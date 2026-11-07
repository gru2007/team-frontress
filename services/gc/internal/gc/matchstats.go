package gc

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/socache"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

const (
	matchHistoryLimit = 30
	ladderSeason      = 1
)

func init() {
	onClient(gcpb.ETFGCMsg_k_EMsgGCMatchHistoryLoad, (*GC).handleMatchHistory)
	onServer(gcpb.ETFGCMsg_k_EMsgGC_DailyCompetitiveStatsRollup, (*GC).handleDailyStats)
}

func (g *GC) handleMatchHistory(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGCMatchHistoryLoad
	if g.parse(m, &msg) && msg.GetMatchGroup() >= gcpb.ETFMatchGroup_k_eTFMatchGroup_First &&
		msg.GetMatchGroup() <= gcpb.ETFMatchGroup_k_eTFMatchGroup_Event_Last {
		g.publishMatchStats(s.SteamID, int32(msg.GetMatchGroup()))
		g.so.RefreshType(s.SteamID, socache.TypeMatchResult)
	}
}

func (g *GC) publishMatchStats(id uint64, group int32) {
	prefix := strconv.FormatInt(int64(group), 10) + ":"
	steamID := lobbyKey(id)
	var totals [6]uint64
	var medals [5][3]uint32
	var games uint32
	keep := make(map[string]bool, matchHistoryLimit)
	keepXP := make(map[string]bool)
	ack := g.players.Get(id).Acknowledged[group]
	for i, m := range g.history.Results(group, steamID, time.Time{}) {
		for _, p := range m.Result.Players {
			if p.SteamID != steamID {
				continue
			}
			if !p.Excluded && m.Result.Completed() {
				games++
				for i, value := range [...]uint32{p.Score, p.Kills, p.Deaths, p.Damage, p.Healing, p.Support} {
					totals[i] += uint64(value)
				}
				for i, medal := range p.Medals {
					if medal >= 1 && medal <= 3 {
						medals[i][medal-1]++
					}
				}
			}
			if i >= matchHistoryLimit {
				break
			}
			matchID, _ := strconv.ParseUint(m.MatchID, 10, 64)
			partyID, _ := strconv.ParseUint(p.PartyID, 10, 64)
			mapIndex := m.Result.MapIndex
			if mapIndex == 0 {
				mapIndex = g.cfg.MapIndex[m.Map]
			}
			key := prefix + m.MatchID
			keep[key] = true
			if m.ResultTime().After(ack.At) {
				amount, source := p.XPChange, gcpb.CMsgTFXPSource_SOURCE_COMPLETED_MATCH
				if group == ladderGroup {
					amount = p.RatingChange
					source = gcpb.CMsgTFXPSource_SOURCE_COMPETITIVE_WIN
					if amount < 0 {
						source = gcpb.CMsgTFXPSource_SOURCE_COMPETITIVE_LOSS
					}
				}
				if (group == ladderGroup || group == casualGroup) && amount != 0 {
					keepXP[key] = true
					g.so.Put(id, socache.TypeXPSource, key, &gcpb.CMsgTFXPSource{
						Type: source.Enum(), Amount: proto.Int32(int32(amount)), MatchGroup: proto.Int32(group),
						AccountId: proto.Uint32(uint32(id)), MatchId: proto.Uint64(matchID),
					})
				}
			}
			g.so.Put(id, socache.TypeMatchResult, key, &gcpb.CSOTFMatchResultPlayerStats{
				MatchId: proto.Uint64(matchID), AccountId: proto.Uint32(uint32(id)), MatchGroup: proto.Int32(group),
				Endtime: proto.Uint32(uint32(m.ResultTime().Unix())), SeasonId: proto.Uint32(ladderSeason),
				Status: proto.Uint32(uint32(m.Result.Status)), Team: proto.Uint32(p.Team), Flags: proto.Uint32(p.Flags),
				OriginalPartyId: proto.Uint32(uint32(partyID)), Ping: proto.Uint32(p.Ping), Rank: proto.Uint32(p.Rank),
				Score: proto.Uint32(p.Score), Kills: proto.Uint32(p.Kills), Deaths: proto.Uint32(p.Deaths),
				Damage: proto.Uint32(p.Damage), Healing: proto.Uint32(p.Healing), Support: proto.Uint32(p.Support),
				ScoreMedal: proto.Uint32(p.Medals[0]), KillsMedal: proto.Uint32(p.Medals[1]), DamageMedal: proto.Uint32(p.Medals[2]),
				HealingMedal: proto.Uint32(p.Medals[3]), SupportMedal: proto.Uint32(p.Medals[4]),
				DisplayRating: proto.Uint32(uint32(max(0, p.Rating))), DisplayRatingChange: proto.Int32(int32(p.RatingChange)),
				ClassesPlayed: proto.Uint32(p.Classes), MapIndex: proto.Uint32(mapIndex), WinningTeam: proto.Uint32(m.Result.WinningTeam),
			})
			break
		}
	}
	for _, key := range g.so.Keys(id, socache.TypeMatchResult) {
		if strings.HasPrefix(key, prefix) && !keep[key] {
			g.so.Delete(id, socache.TypeMatchResult, key)
		}
	}
	for _, key := range g.so.Keys(id, socache.TypeXPSource) {
		if strings.HasPrefix(key, prefix) && !keepXP[key] {
			g.so.Delete(id, socache.TypeXPSource, key)
		}
	}
	count := func(i int) *uint32 { return proto.Uint32(uint32(min(totals[i], math.MaxUint32))) }
	g.so.Put(id, socache.TypeLadderData, prefix+strconv.Itoa(ladderSeason), &gcpb.CSOTFLadderPlayerStats{
		AccountId: proto.Uint32(uint32(id)), MatchGroup: proto.Int32(group), SeasonId: proto.Uint32(ladderSeason),
		Games: proto.Uint32(games), Score: count(0), Kills: count(1), Deaths: count(2),
		Damage: count(3), Healing: count(4), Support: count(5),
		ScoreBronze: &medals[0][0], ScoreSilver: &medals[0][1], ScoreGold: &medals[0][2],
		KillsBronze: &medals[1][0], KillsSilver: &medals[1][1], KillsGold: &medals[1][2],
		DamageBronze: &medals[2][0], DamageSilver: &medals[2][1], DamageGold: &medals[2][2],
		HealingBronze: &medals[3][0], HealingSilver: &medals[3][1], HealingGold: &medals[3][2],
		SupportBronze: &medals[4][0], SupportSilver: &medals[4][1], SupportGold: &medals[4][2],
	})
}

func (g *GC) handleDailyStats(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGC_DailyCompetitiveStatsRollup
	response := &gcpb.CMsgGC_DailyCompetitiveStatsRollup_Response{}
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGC_DailyCompetitiveStatsRollup_Response), response)
	_, l := g.serverLobby(s, 0)
	if !g.parse(m, &msg) || l == nil {
		return
	}
	type bucket struct {
		n                  uint32
		mean, squaredDelta [5]float64
	}
	ranks := map[uint32]*bucket{}
	for _, match := range g.history.Results(l.Group, "", g.now().Add(-24*time.Hour)) {
		if !match.Result.Completed() || match.Result.Duration == 0 {
			continue
		}
		// The stock server compares medal stats normalized to nine minutes.
		scale := min(3.0, max(0.33, 540/float64(match.Result.Duration)))
		for _, p := range match.Result.Players {
			if p.Excluded {
				continue
			}
			b := ranks[p.Rank]
			if b == nil {
				b = &bucket{}
				ranks[p.Rank] = b
			}
			b.n++
			for i, value := range [...]uint32{p.Score, p.Kills, p.Damage, p.Healing, p.Support} {
				x := math.Trunc(float64(value) * scale)
				delta := x - b.mean[i]
				b.mean[i] += delta / float64(b.n)
				b.squaredDelta[i] += delta * (x - b.mean[i])
			}
		}
	}
	for rank, b := range ranks {
		avg := func(i int) *uint32 { return proto.Uint32(roundedStat(b.mean[i])) }
		dev := func(i int) *uint32 { return proto.Uint32(roundedStat(math.Sqrt(b.squaredDelta[i] / float64(b.n)))) }
		response.Rankdata = append(response.Rankdata, &gcpb.CMsgGC_DailyCompetitiveStatsRollup_Response_RankBucketEntry{
			Rank: proto.Uint32(rank), Records: proto.Uint32(b.n), AvgScore: avg(0), StdevScore: dev(0),
			AvgKills: avg(1), StdevKills: dev(1), AvgDamage: avg(2), StdevDamage: dev(2),
			AvgHealing: avg(3), StdevHealing: dev(3), AvgSupport: avg(4), StdevSupport: dev(4),
		})
	}
	sort.Slice(response.Rankdata, func(i, j int) bool { return response.Rankdata[i].GetRank() < response.Rankdata[j].GetRank() })
}

func roundedStat(x float64) uint32 {
	return uint32(min(math.MaxUint32, max(0, math.Round(x))))
}
