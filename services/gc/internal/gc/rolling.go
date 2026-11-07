package gc

import (
	"slices"
	"sort"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/sink"
	"github.com/gru2007/team-frontress/services/gc/internal/socache"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func (g *GC) nextMapOptions(l *Lobby, group config.MatchGroupConfig) []uint32 {
	if l.Group != casualGroup || l.War != nil || g.cfg.MapIndex[l.Map] == 0 {
		return nil
	}
	maps := slices.Clone(group.EffectiveMaps())
	sort.Slice(maps, func(i, j int) bool {
		a, b := g.recentMaps[maps[i]], g.recentMaps[maps[j]]
		if a.Equal(b) {
			return maps[i] < maps[j]
		}
		return a.Before(b)
	})
	var options []uint32
	for _, name := range maps {
		id := g.cfg.MapIndex[name]
		if id != 0 && !sameMap(l.Map, name) && !slices.Contains(options, id) {
			options = append(options, id)
			if len(options) == 3 {
				return options
			}
		}
	}
	return nil
}

func (g *GC) handleNewMatch(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgGCNewMatchForLobbyRequest
	reply := &gcpb.CMsgGCNewMatchForLobbyResponse{Success: proto.Bool(false)}
	defer g.reply(s, m, uint32(gcpb.ETFGCMsg_k_EMsgGC_NewMatchForLobbyResponse), reply)
	if !g.parse(m, &msg) || msg.GetLobbyId() == 0 || msg.GetCurrentMatchId() == 0 || msg.GetNextMapId() == 0 {
		return
	}
	srv, l := g.serverLobby(s, msg.GetLobbyId())
	if l == nil {
		return
	}
	if l.PreviousMatchID == msg.GetCurrentMatchId() {
		*reply.Success = g.cfg.MapIndex[l.Map] == msg.GetNextMapId()
		return
	}
	grp, enabled := g.cfg.Group(l.Group)
	archived, _ := g.history.Get(lobbyKey(l.MatchID))
	if !enabled || l.MatchID != msg.GetCurrentMatchId() || l.State != lobbyRun || len(l.NextMaps) != 3 ||
		archived.Result == nil || !archived.Result.Completed() {
		return
	}
	index := msg.GetNextMapId()
	if index != g.cfg.MapIndex[l.Map] && !slices.Contains(l.NextMaps, index) {
		return
	}
	nextMap := ""
	for _, name := range grp.EffectiveMaps() {
		if g.cfg.MapIndex[name] == index {
			nextMap = name
			break
		}
	}
	if nextMap == "" {
		return
	}
	next := &Lobby{
		ID: l.ID, MatchID: newID(), PreviousMatchID: l.MatchID, Group: l.Group, Map: nextMap,
		MaxPlayers: l.MaxPlayers, State: lobbySetup, ServerID: l.ServerID, Connect: l.Connect, STV: l.STV,
		FormedAt: g.now(), MMVersion: l.MMVersion + 1,
	}
	for _, member := range l.Members {
		if member.Connect != gcpb.CTFLobbyPlayerProto_RESERVATION_PENDING {
			carry := *member
			carry.JoinedAt = next.FormedAt
			next.Members = append(next.Members, &carry)
		}
	}
	if len(next.Members) == 0 {
		return
	}
	next.NextMaps = g.nextMapOptions(next, grp)
	g.lobbies[l.ID] = next
	g.dirty = true
	// Persist the new match ID before acknowledging it to the game server.
	g.save()
	if g.dirty {
		g.lobbies[l.ID] = l
		return
	}
	srv.LateJoinSlots = 0
	g.recentMaps[next.Map] = next.FormedAt
	g.archiveMatch(l, nil, "continued to the next map")
	for _, member := range l.Members {
		if next.member(member.SteamID) == nil {
			g.so.Delete(member.SteamID, socache.TypeLobby, lobbyKey(l.ID))
		}
	}
	g.publishLobby(next)
	g.archiveMatch(next, nil, "")
	g.hooks.Emit(sink.Event{Type: "match_ended", At: next.FormedAt,
		Data: g.matchSummary(l, map[string]any{"reason": "continued to the next map"})})
	g.log.Info("match rolled to next map", "lobby", l.ID, "previous_match", l.MatchID, "match", next.MatchID, "map", next.Map)
	*reply.Success = true
}
