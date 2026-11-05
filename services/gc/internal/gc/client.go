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
	SteamID      uint64
	Online       bool
	OfflineSince time.Time
	Verified     bool
	AuthAppID    uint32
	PartyID      uint64
	Look         *Look  // see looks.go
	WarFront     string // the operation they asked to fight in, see warfront.go

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
func (g *GC) ClientConnected(s *session.Session, verified bool) {
	p := g.player(s.SteamID)
	p.Online = true
	p.OfflineSince = time.Time{}
	p.Verified = verified
	p.AuthAppID = s.AppID

	party := g.parties[p.PartyID]
	if party == nil || !party.has(p.SteamID) {
		party = g.newParty(p.SteamID)
	}
	g.publishParty(party)
	g.publishRatings(p.SteamID)
	g.sendWorldStatus(s)
	g.sendFaction(s.SteamID)

	// Everything else the GC holds for this player -- a lobby the game does
	// not know about yet because it restarted, an invite -- goes out with the
	// refresh.
	g.so.Refresh(p.SteamID)
	g.log.Info("client connected", "steamid", p.SteamID, "party", party.ID, "verified", verified)
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
	ratingLadder6v6     = 2  // k_nMMRating_6v6_GLICKO
	ratingLadder6v6Ackd = 11 // k_nMMRating_6v6_GLICKO_PlayerAcknowledged
	ratingCasualXP      = 5  // k_nMMRating_Casual_XP
	ratingCasualXPAckd  = 6  // k_nMMRating_Casual_XP_PlayerAcknowledged
)

// publishRatings puts the numbers the main menu badge reads into the
// player's cache: XP for casual, the rating for the ladder.
func (g *GC) publishRatings(id uint64) {
	rec := g.players.Get(id)
	put := func(typ int32, value int) {
		if value < 0 {
			value = 0
		}
		g.so.Put(id, socache.TypeRatingData, strconv.Itoa(int(typ)), &gcpb.CSOTFRatingData{
			AccountId:     proto.Uint32(uint32(id)),
			RatingType:    proto.Int32(typ),
			RatingPrimary: proto.Uint32(uint32(value)),
		})
	}
	put(ratingCasualXP, rec.XP())
	put(ratingCasualXPAckd, rec.XP())
	put(ratingLadder6v6, rec.Rating)
	put(ratingLadder6v6Ackd, rec.Rating)
}

// PlayerRecord is a player's public record, for the HTTP API.
func (g *GC) PlayerRecord(id uint64) players.Record { return g.players.Get(id) }
