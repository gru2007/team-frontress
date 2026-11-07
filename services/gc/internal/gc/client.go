package gc

import (
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/players"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/socache"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// Player is somebody the GC has seen this run.
type Player struct {
	SteamID        uint64
	Online         bool
	OfflineSince   time.Time
	Verified       bool
	AuthAppID      uint32
	PartyID        uint64
	Look           *Look  // see looks.go
	WarFront       string // the operation they asked to fight in, see warfront.go
	BestDataCenter string

	// Social (social.go): their name as the game told us, their language,
	// whether they are looking for a squad, their chat allowance.
	Name       string
	Lang       string
	LFG        bool
	LFGNote    string
	Currency   string
	chatTokens float64
	chatAt     time.Time
	// supportBusy: a donation is being opened, so a second click waits.
	supportBusy     time.Time
	supportReportAt time.Time
}

func (g *GC) player(id uint64) *Player {
	p := g.online[id]
	if p == nil {
		p = &Player{SteamID: id}
		g.online[id] = p
	}
	return p
}

// ClientConnected brings a freshly authenticated client up to date: its
// party, its match if it has one, its rating, and what the GC offers. It runs
// on the loop.
func (g *GC) ClientConnected(s *session.Session, verified bool) uint64 {
	p := g.player(s.SteamID)
	p.Online = true
	p.OfflineSince = time.Time{}
	p.Verified = verified
	p.AuthAppID = s.AppID
	p.BestDataCenter = ""

	party := g.parties[p.PartyID]
	if party == nil || !party.has(p.SteamID) {
		party = g.newParty(p.SteamID)
	}
	g.publishParty(party)
	g.publishRatings(p.SteamID)
	for _, grp := range g.cfg.MatchGroups {
		if grp.Enabled {
			g.publishMatchStats(p.SteamID, grp.MatchGroup)
		}
	}
	g.sendWorldStatus(s)
	g.sendFaction(s.SteamID)

	// Everything else the GC holds for this player -- a lobby the game does
	// not know about yet because it restarted, an invite -- goes out with the
	// refresh.
	g.so.Refresh(p.SteamID)
	g.log.Info("client connected", "steamid", p.SteamID, "party", party.ID, "verified", verified)
	if l := g.lobbyOf(p.SteamID); l != nil {
		return l.ID
	}
	return 0
}

func (g *GC) onSessionEnd(s *session.Session) {
	// A session replaced by a newer one for the same SteamID is not the
	// player going away.
	if cur := g.sessions.BySteamID(s.SteamID); cur != nil && cur != s {
		return
	}
	switch s.Kind {
	case session.Client:
		p := g.online[s.SteamID]
		if p == nil || !p.Online {
			return
		}
		p.Online = false
		p.OfflineSince = g.now()
		if party := g.parties[p.PartyID]; party != nil {
			g.publishParty(party)
			g.partyChat(party, gcpb.ETFPartyChatType_k_eTFPartyChatType_Synthetic_MemberOffline, p.SteamID, "")
		}
		g.log.Info("client went offline", "steamid", s.SteamID)
	case session.Server:
		g.serverGone(s)
	}
}

func init() {
	onClient(gcpb.ETFGCMsg_k_EMsgGC_AcknowledgeXP, (*GC).handleAcknowledgeXP)
	onClient(gcpb.ETFGCMsg_k_EMsgGC_NotificationAcknowledge, (*GC).handleAcknowledgeNotification)
	// The game's cache was replaced -- the web API inventory arrived, or the
	// game restarted its GC client -- and our objects went with it.
	onClient(gcpb.ESOMsg_k_ESOMsg_CacheSubscriptionRefresh, func(g *GC, s *session.Session, m *wire.Msg) {
		var msg gcpb.CMsgSOCacheSubscriptionRefresh
		if !g.parse(m, &msg) {
			return
		}
		if msg.GetOwner() != 0 && msg.GetOwner() != s.SteamID {
			return
		}
		g.so.Refresh(s.SteamID)
	})
	onServer(gcpb.ESOMsg_k_ESOMsg_CacheSubscriptionRefresh, func(g *GC, s *session.Session, m *wire.Msg) {
		g.so.Refresh(s.SteamID)
	})
}

// sendWorldStatus tells the client which match groups exist. A group the GC
// does not run is "disabled", which is what greys it out in the stock menu.
func (g *GC) sendWorldStatus(s *session.Session) {
	ws := &gcpb.CMsgTFWorldStatus{}
	for grp := int32(0); grp <= int32(gcpb.ETFMatchGroup_k_eTFMatchGroup_Event_Last); grp++ {
		if _, ok := g.cfg.Group(grp); !ok {
			ws.DisabledMatchGroups = append(ws.DisabledMatchGroups, gcpb.ETFMatchGroup(grp))
		}
	}
	g.sendTo(s, uint32(gcpb.ETFGCMsg_k_EMsgGC_WorldStatusBroadcast), ws, wire.NoJob)
}

// Rating types the stock menu reads (EMMRating).
const (
	ladderGroup          = int32(gcpb.ETFMatchGroup_k_eTFMatchGroup_Ladder_6v6)
	casualGroup          = int32(gcpb.ETFMatchGroup_k_eTFMatchGroup_Casual_12v12)
	ratingLadder6v6      = 2  // k_nMMRating_6v6_GLICKO
	ratingLadder6v6Ackd  = 11 // k_nMMRating_6v6_GLICKO_PlayerAcknowledged
	ratingLadderRank     = 7  // k_nMMRating_6v6_Rank
	ratingLadderRankAckd = 8  // k_nMMRating_6v6_Rank_PlayerAcknowledged
	ratingCasualXP       = 5  // k_nMMRating_Casual_XP
	ratingCasualXPAckd   = 6  // k_nMMRating_Casual_XP_PlayerAcknowledged
)

// publishRatings puts the numbers the main menu badge reads into the
// player's cache: XP for casual, the rating for the ladder.
func (g *GC) publishRatings(id uint64) {
	rec := g.players.Get(id)
	putRating := func(typ int32, value int, secondary int) {
		if value < 0 {
			value = 0
		}
		g.so.Put(id, socache.TypeRatingData, strconv.Itoa(int(typ)), &gcpb.CSOTFRatingData{
			AccountId:       proto.Uint32(uint32(id)),
			RatingType:      proto.Int32(typ),
			RatingPrimary:   proto.Uint32(uint32(value)),
			RatingSecondary: proto.Uint32(uint32(secondary)),
		})
	}
	putRating(ratingCasualXP, rec.XP(), 0)
	putRating(ratingCasualXPAckd, rec.Acknowledged[casualGroup].Value, 0)
	putRating(ratingLadder6v6, rec.Rating, 0)
	ackRating := players.DefaultRating
	if ack, ok := rec.Acknowledged[ladderGroup]; ok {
		ackRating = ack.Value
	}
	putRating(ratingLadder6v6Ackd, ackRating, 0)
	rank, wins := g.ladderRank(id, rec.Rating)
	putRating(ratingLadderRank, rank, wins)
	putRating(ratingLadderRankAckd, rec.Acknowledged[ladderGroup].Rank, 0)
}

func (g *GC) handleAcknowledgeXP(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgAcknowledgeXP
	if !g.parse(m, &msg) || msg.PredictedExperience == nil {
		return
	}
	group := msg.GetMatchGroup()
	if group != casualGroup && group != ladderGroup {
		return
	}
	rank := 0
	if group == ladderGroup {
		rank, _ = g.ladderRank(s.SteamID, g.players.Get(s.SteamID).Rating)
	}
	if g.players.AcknowledgeProgress(s.SteamID, group, msg.GetPredictedExperience(), group == ladderGroup, rank) {
		g.publishRatings(s.SteamID)
		g.publishMatchStats(s.SteamID, group)
	}
}

func (g *GC) ladderRank(id uint64, rating int) (rank, placementWins int) {
	const winsToPlace = 10 // CMatchGroupDescription_Competitive in the SDK
	steamID := lobbyKey(id)
	for _, match := range g.history.Results(ladderGroup, steamID, time.Time{}) {
		if !match.Result.Completed() {
			continue
		}
		for _, p := range match.Result.Players {
			if p.SteamID == steamID && !p.Excluded && p.Team == match.Result.WinningTeam {
				placementWins++
				if placementWins == winsToPlace {
					return players.LadderRank(rating), placementWins
				}
			}
		}
	}
	return 0, placementWins
}

func (g *GC) handleAcknowledgeNotification(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgNotificationAcknowledge
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGC_NotificationAcknowledgeReply), &gcpb.CMsgNotificationAcknowledgeReply{})
	if g.parse(m, &msg) && (msg.AccountId == nil || msg.GetAccountId() == uint32(s.SteamID)) {
		g.so.Delete(s.SteamID, socache.TypeNotification, lobbyKey(msg.GetNotificationId()))
	}
}

// PlayerRecord is a player's public record, for the HTTP API.
func (g *GC) PlayerRecord(id uint64) players.Record { return g.players.Get(id) }
