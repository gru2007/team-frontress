package gc

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/mm"
	"github.com/gru2007/team-frontress/services/gc/internal/players"
	"github.com/gru2007/team-frontress/services/gc/internal/sink"
	"github.com/gru2007/team-frontress/services/gc/internal/socache"
	"github.com/gru2007/team-frontress/services/gc/internal/state"
	"github.com/gru2007/team-frontress/services/gc/internal/war"
)

// A lobby is a match, from the moment it forms to the moment it ends. It has
// two states, the same two the game knows:
//
//	setup  formed, waiting for a server to take it and load the map
//	run    the server is on the map; players connect and play
//
// Every state has a deadline (see tickLobbies). A match either moves on or
// ends; nothing waits forever.
type lobbyState string

const (
	lobbySetup lobbyState = "setup"
	lobbyRun   lobbyState = "run"
)

// Member is one seat.
type Member struct {
	SteamID  uint64
	Team     mm.Team
	PartyID  uint64
	Connect  gcpb.CTFLobbyPlayerProto_ConnectState
	JoinedAt time.Time
}

// Lobby is one match.
type Lobby struct {
	ID              uint64
	MatchID         uint64
	Group           int32
	Map             string
	MaxPlayers      int
	State           lobbyState
	Members         []*Member
	ServerID        uint64
	Connect         string
	STV             string
	FormedAt        time.Time
	StartedAt       time.Time
	MMVersion       uint32
	PreviousMatchID uint64
	NextMaps        []uint32
	War             *state.War

	// Runtime only.
	serverLostAt  time.Time
	everConnected bool
	resultSeen    bool
	kicked        map[uint64]bool
}

func (l *Lobby) member(id uint64) *Member {
	for _, m := range l.Members {
		if m.SteamID == id {
			return m
		}
	}
	return nil
}

func (l *Lobby) teamCounts() (red, blu int) {
	for _, m := range l.Members {
		if m.Team == mm.Red {
			red++
		} else {
			blu++
		}
	}
	return
}

func (l *Lobby) teamCap(grp config.MatchGroupConfig) int {
	if l.MaxPlayers > 0 {
		return l.MaxPlayers / 2
	}
	return grp.TeamCap()
}

func (l *Lobby) addMember(id uint64, team mm.Team, party uint64, now time.Time) {
	if l.member(id) != nil {
		return
	}
	l.Members = append(l.Members, &Member{
		SteamID:  id,
		Team:     team,
		PartyID:  party,
		Connect:  gcpb.CTFLobbyPlayerProto_RESERVATION_PENDING,
		JoinedAt: now,
	})
}

func lobbyKey(id uint64) string { return strconv.FormatUint(id, 10) }

// lobbyOf is the match a player has a seat in.
func (g *GC) lobbyOf(id uint64) *Lobby {
	for _, l := range g.lobbies {
		if l.member(id) != nil {
			return l
		}
	}
	return nil
}

func (g *GC) proto(l *Lobby) *gcpb.CSOTFGameServerLobby {
	st := gcpb.CSOTFGameServerLobby_SERVERSETUP
	if l.State == lobbyRun {
		st = gcpb.CSOTFGameServerLobby_RUN
	}
	msg := &gcpb.CSOTFGameServerLobby{
		LobbyId:         proto.Uint64(l.ID),
		MatchId:         proto.Uint64(l.MatchID),
		State:           st.Enum(),
		MapName:         proto.String(l.Map),
		MatchGroup:      proto.Uint32(uint32(l.Group)),
		FormedTime:      proto.Uint32(uint32(l.FormedAt.Unix())),
		LobbyMmVersion:  proto.Uint32(l.MMVersion),
		FixedMatchSize:  proto.Uint32(uint32(l.MaxPlayers)),
		NextMapsForVote: l.NextMaps,
	}
	if l.ServerID != 0 {
		msg.ServerId = proto.Uint64(l.ServerID)
	}
	if l.State == lobbyRun && l.Connect != "" {
		msg.Connect = proto.String(l.Connect)
	}
	if grp, ok := g.cfg.Group(l.Group); ok && grp.Backfills() {
		msg.LateJoinEligible = proto.Bool(true)
	}
	for _, m := range l.Members {
		rec := g.players.Get(m.SteamID)
		p := &gcpb.CTFLobbyPlayerProto{
			Id:              proto.Uint64(m.SteamID),
			Team:            gcpb.TF_GC_TEAM(m.Team).Enum(),
			ConnectState:    m.Connect.Enum(),
			Type:            gcpb.CTFLobbyPlayerProto_MATCH_PLAYER.Enum(),
			OriginalPartyId: proto.Uint64(m.PartyID),
			BadgeLevel:      proto.Uint32(uint32(rec.Level())),
			LastConnectTime: proto.Uint32(uint32(m.JoinedAt.Unix())),
		}
		if rec.Name != "" {
			p.Name = proto.String(rec.Name)
		}
		if l.Group == ladderGroup {
			p.Rank = proto.Uint32(uint32(players.LadderRank(rec.Rating)))
		}
		msg.Members = append(msg.Members, p)
	}
	return msg
}

// publishLobby writes the lobby into every member's cache and the server's.
func (g *GC) publishLobby(l *Lobby) {
	msg := g.proto(l)
	for _, m := range l.Members {
		g.so.Put(m.SteamID, socache.TypeLobby, lobbyKey(l.ID), msg)
	}
	if l.ServerID != 0 {
		g.so.Put(l.ServerID, socache.TypeLobby, lobbyKey(l.ID), msg)
	}
}

// lobbyChanged is every change to a lobby the server must acknowledge: a new
// version, published everywhere, saved.
func (g *GC) lobbyChanged(l *Lobby) {
	l.MMVersion++
	g.publishLobby(l)
	g.dirty = true
	g.archiveMatch(l, nil, "")
}

// --- forming -------------------------------------------------------------------

type battle struct {
	Battle war.Battle
	Front  war.Front
}

// nextBattle is the battle the war wants played: on the front most of the
// queued players asked for, otherwise the one with the fewest matches already
// running.
func (g *GC) nextBattle(grp config.MatchGroupConfig, queue []mm.Entry) *battle {
	if g.war == nil || !g.cfg.FightsWar(grp) {
		return nil
	}
	fronts := g.war.Fronts()
	if len(fronts) == 0 {
		return nil
	}
	busy := map[string]int{}
	for _, l := range g.lobbies {
		if l.War != nil {
			busy[l.War.FrontID]++
		}
	}
	votes := g.frontVotes(queue)
	sort.Slice(fronts, func(i, j int) bool {
		if votes[fronts[i].ID] != votes[fronts[j].ID] {
			return votes[fronts[i].ID] > votes[fronts[j].ID]
		}
		if busy[fronts[i].ID] != busy[fronts[j].ID] {
			return busy[fronts[i].ID] < busy[fronts[j].ID]
		}
		return fronts[i].ID < fronts[j].ID
	})
	b, err := g.war.NextBattle(fronts[0].ID)
	if err != nil {
		g.log.Warn("war: no battle for front", "front", fronts[0].ID, "err", err)
		return nil
	}
	return &battle{Battle: b, Front: fronts[0]}
}

func (g *GC) createLobby(grp config.MatchGroupConfig, f mm.Formed, sizes mm.Sizes, b *battle) {
	now := g.now()
	l := &Lobby{
		ID:         newID(),
		MatchID:    newID(),
		Group:      grp.MatchGroup,
		MaxPlayers: sizes.Max,
		State:      lobbySetup,
		FormedAt:   now,
	}
	picked := append(append([]mm.Entry{}, f.Red...), f.Blu...)
	lastPlayed := func(m string) time.Time { return g.recentMaps[m] }

	attackerGC := mm.Blu
	if b != nil {
		group := mm.Sizes{Min: grp.MinPlayers, Ideal: grp.IdealPlayers, Max: grp.MaxPlayers}
		fits := mm.FieldsFitting(group, b.Battle.Fields, f.Seats())
		l.Map = mm.ChooseMap(mm.MapNames(fits), picked, lastPlayed)
		field := mm.FieldNamed(b.Battle.Fields, l.Map, fits[0])
		if l.Map == "" {
			l.Map = field.Map
		}
		l.MaxPlayers = mm.FieldSizes(group, field).Max
		attacker := b.Battle.AttackerTeam
		if field.AttackerTeam != 0 {
			attacker = field.AttackerTeam
		}
		if attacker == 0 && b.Battle.Attacker == war.SideRED {
			attacker = 2
		}
		if attacker == 2 { // TF_TEAM_RED
			attackerGC = mm.Red
		}
		l.War = &state.War{
			Campaign:     g.war.Campaign(),
			UniformSwap:  (b.Battle.Attacker == war.SideRED) != (attackerGC == mm.Red),
			FrontID:      b.Front.ID,
			NodeID:       b.Front.NodeID,
			NodeName:     b.Battle.NodeName,
			StageIndex:   b.Battle.StageIndex,
			StageCount:   b.Battle.StageCount,
			StageKind:    b.Battle.Stage.Kind,
			Attacker:     string(b.Battle.Attacker),
			AttackerTeam: int32(attackerGC),
			Conditions:   b.Battle.Conditions,
			Order:        b.Front.ID == g.orderFront(),
		}
	}
	if l.Map == "" {
		l.Map = mm.ChooseMap(grp.EffectiveMaps(), picked, lastPlayed)
	}
	g.recentMaps[l.Map] = now
	l.NextMaps = g.nextMapOptions(l, grp)

	seat := func(es []mm.Entry, team mm.Team) {
		for _, e := range es {
			for _, p := range e.Players {
				l.addMember(p.SteamID, team, e.PartyID, now)
			}
			if p := g.parties[e.PartyID]; p != nil {
				delete(p.Queues, grp.MatchGroup)
			}
		}
	}
	redTeam, bluTeam := mm.Red, mm.Blu
	if l.War != nil && l.War.UniformSwap {
		redTeam, bluTeam = mm.Blu, mm.Red
	}
	seat(f.Red, redTeam)
	seat(f.Blu, bluTeam)

	g.lobbies[l.ID] = l
	g.dirty = true
	g.publishLobby(l)
	for _, e := range picked {
		if p := g.parties[e.PartyID]; p != nil {
			g.publishParty(p)
		}
	}
	red, blu := l.teamCounts()
	g.log.Info("match formed", "lobby", l.ID, "match", l.MatchID, "group", grp.Name,
		"map", l.Map, "red", red, "blu", blu)
	g.assignServer(l)
	g.archiveMatch(l, nil, "")
}

// --- servers -------------------------------------------------------------------

// assignServer gives a lobby a server from the pool: the servers connected
// to the GC with a server key. A match is only formed when one is free (see
// freeServers), so this finds one unless it was taken in between; then the
// next tick tries again, until the boot deadline.
func (g *GC) assignServer(l *Lobby) {
	if l.ServerID != 0 {
		return
	}
	grp, _ := g.cfg.Group(l.Group)
	var best *Server
	for _, srv := range g.servers {
		if !srv.idle(g.now()) || (grp.Region != "" && srv.Region != grp.Region) {
			continue
		}
		if best == nil || srv.SteamID < best.SteamID {
			best = srv
		}
	}
	if best != nil {
		g.bindServer(l, best)
		return
	}
}

func (g *GC) bindServer(l *Lobby, srv *Server) {
	l.ServerID = srv.SteamID
	srv.LobbyID = l.ID
	srv.LateJoinSlots = 0
	if l.Connect == "" {
		l.Connect = srv.Connect
	}
	l.serverLostAt = time.Time{}
	g.sendConditions(srv, l)
	g.lobbyChanged(l)
	g.log.Info("server assigned", "lobby", l.ID, "server", srv.SteamID, "name", srv.Name, "connect", l.Connect)
}

// goLive is the server on the match's map: players may connect.
func (g *GC) goLive(l *Lobby) {
	if l.Connect == "" {
		g.log.Warn("server is on the map but has no address to give players yet", "lobby", l.ID)
		return
	}
	l.State = lobbyRun
	l.StartedAt = g.now()
	g.lobbyChanged(l)
	g.log.Info("match live", "lobby", l.ID, "connect", l.Connect)
	g.hooks.Emit(sink.Event{Type: "match_started", At: l.StartedAt, Data: g.matchSummary(l, nil)})
}

// --- ending --------------------------------------------------------------------

// leaveLobby takes a player out of a match. Walking out of one that is
// running is an abandon unless the server already said the player could go.
func (g *GC) leaveLobby(l *Lobby, id uint64, abandon bool) {
	if l.member(id) == nil {
		return
	}
	if abandon && l.State == lobbyRun {
		g.players.Abandoned(id, "", l.MatchID)
	}
	g.dropMember(l, id)
	if len(l.Members) == 0 {
		g.endLobby(l, "everybody left", false)
	}
}

func (g *GC) dropMember(l *Lobby, id uint64) {
	n := len(l.Members)
	out := l.Members[:0]
	for _, m := range l.Members {
		if m.SteamID != id {
			out = append(out, m)
		}
	}
	l.Members = out
	if len(l.Members) == n {
		return
	}
	g.so.Delete(id, socache.TypeLobby, lobbyKey(l.ID))
	g.lobbyChanged(l)
	if pl := g.online[id]; pl != nil {
		if p := g.parties[pl.PartyID]; p != nil {
			g.publishParty(p)
		}
	}
}

// endLobby finishes a match for whatever reason. If requeue is set the
// match never got going, and its parties go back to the front of the queue.
func (g *GC) endLobby(l *Lobby, why string, requeue bool) {
	if g.lobbies[l.ID] == nil {
		return
	}
	g.archiveMatch(l, nil, why)
	delete(g.lobbies, l.ID)
	g.dirty = true
	for _, m := range l.Members {
		g.so.Delete(m.SteamID, socache.TypeLobby, lobbyKey(l.ID))
	}
	if l.ServerID != 0 {
		g.so.Delete(l.ServerID, socache.TypeLobby, lobbyKey(l.ID))
		if srv := g.servers[l.ServerID]; srv != nil && srv.LobbyID == l.ID {
			srv.LobbyID = 0
			srv.Ready = false // it says when it is empty again
			srv.LateJoinSlots = 0
			srv.RestUntil = g.now().Add(serverRest)
			if srv.Session == nil {
				delete(g.servers, srv.SteamID)
			}
		}
	}
	parties := map[uint64]bool{}
	for _, m := range l.Members {
		parties[m.PartyID] = true
		if pl := g.online[m.SteamID]; pl != nil {
			parties[pl.PartyID] = true
		}
	}
	for pid := range parties {
		p := g.parties[pid]
		if p == nil {
			continue
		}
		if requeue {
			p.Queues[l.Group] = l.FormedAt.Add(-time.Minute)
		}
		g.publishParty(p)
	}
	for _, m := range l.Members {
		g.publishRatings(m.SteamID)
	}
	g.log.Info("match ended", "lobby", l.ID, "match", l.MatchID, "why", why, "requeued", requeue)
	if l.State == lobbyRun {
		g.hooks.Emit(sink.Event{Type: "match_ended", At: g.now(), Data: g.matchSummary(l, map[string]any{"reason": why})})
	}
}

// tickLobbies enforces every deadline a match has.
func (g *GC) tickLobbies() {
	if g.war != nil {
		busy := false
		for _, l := range g.lobbies {
			if l.War != nil {
				busy = true
				break
			}
		}
		if !busy {
			if advanced, err := g.war.AdvanceCampaign(g.now(), 24*time.Hour); err != nil {
				g.log.Error("campaign restart", "err", err)
			} else if advanced {
				for id := range g.online {
					g.sendFaction(id)
				}
			}
		}
		online := 0
		for _, p := range g.online {
			if p.Online {
				online++
			}
		}
		if _, err := g.war.Reconcile(online, g.poolSize()); err != nil {
			g.log.Warn("war: reconcile", "err", err)
		}
	}
	now := g.now()
	t := g.cfg.Timing
	for _, l := range g.lobbies {
		srv := g.servers[l.ServerID]
		connected := srv != nil && srv.Session != nil && !srv.Session.Closed()
		if l.ServerID != 0 && !connected {
			if l.serverLostAt.IsZero() {
				l.serverLostAt = now
			}
		} else {
			l.serverLostAt = time.Time{}
		}

		switch l.State {
		case lobbySetup:
			if now.Sub(l.FormedAt) > t.ServerBoot() {
				g.endLobby(l, "no server became ready in time", true)
				continue
			}
			if l.ServerID == 0 {
				g.assignServer(l)
			}
		case lobbyRun:
			switch {
			case !l.serverLostAt.IsZero() && now.Sub(l.serverLostAt) > t.ServerLost():
				g.endLobby(l, "lost the game server", false)
			case !l.everConnected && now.Sub(l.StartedAt) > t.NoShow():
				g.endLobby(l, "nobody connected", false)
			case now.Sub(l.StartedAt) > t.MaxMatch():
				g.endLobby(l, "ran past the maximum match length", false)
			}
		}
	}
}

// --- persistence ---------------------------------------------------------------

func (l *Lobby) record() state.Lobby {
	r := state.Lobby{
		ID: l.ID, MatchID: l.MatchID, Group: l.Group, Map: l.Map, MaxPlayers: l.MaxPlayers,
		State: string(l.State), ServerID: l.ServerID, Connect: l.Connect, STV: l.STV,
		FormedAt: l.FormedAt, StartedAt: l.StartedAt,
		MMVersion: l.MMVersion, War: l.War, PreviousMatchID: l.PreviousMatchID, NextMaps: l.NextMaps,
	}
	for _, m := range l.Members {
		r.Members = append(r.Members, state.Member{
			SteamID: m.SteamID, Team: int32(m.Team), PartyID: m.PartyID,
			ConnectState: int32(m.Connect), JoinedAt: m.JoinedAt,
		})
	}
	return r
}

// restoreLobby brings back a match from the snapshot. Its server and its
// players reconnect on their own; until they do, the usual deadlines apply.
func (g *GC) restoreLobby(r state.Lobby) {
	l := &Lobby{
		ID: r.ID, MatchID: r.MatchID, Group: r.Group, Map: r.Map, MaxPlayers: r.MaxPlayers,
		State: lobbyState(r.State), ServerID: r.ServerID, Connect: r.Connect, STV: r.STV,
		FormedAt: r.FormedAt, StartedAt: r.StartedAt,
		MMVersion: r.MMVersion, War: r.War, everConnected: r.State == string(lobbyRun),
		PreviousMatchID: r.PreviousMatchID, NextMaps: r.NextMaps,
	}
	if archived, ok := g.history.Get(lobbyKey(r.MatchID)); ok {
		l.resultSeen = archived.Result != nil
		for _, p := range archived.Members {
			if id, err := strconv.ParseUint(p.SteamID, 10, 64); p.VoteKicked && err == nil {
				if l.kicked == nil {
					l.kicked = make(map[uint64]bool)
				}
				l.kicked[id] = true
			}
		}
	}
	for _, m := range r.Members {
		l.Members = append(l.Members, &Member{
			SteamID: m.SteamID, Team: mm.Team(m.Team), PartyID: m.PartyID,
			Connect: gcpb.CTFLobbyPlayerProto_ConnectState(m.ConnectState), JoinedAt: m.JoinedAt,
		})
	}
	if l.ServerID != 0 {
		g.servers[l.ServerID] = &Server{SteamID: l.ServerID, LobbyID: l.ID}
		l.serverLostAt = g.now()
	}
	g.lobbies[l.ID] = l
	// The objects go back into the caches now, so whoever reconnects gets
	// them in their refresh.
	g.publishLobby(l)
	g.log.Info("match restored", "lobby", l.ID, "state", l.State, "members", len(l.Members))
	g.archiveMatch(l, nil, "")
}

// matchSummary is what other projects are told about a match.
func (g *GC) matchSummary(l *Lobby, extra map[string]any) map[string]any {
	grpName := ""
	if grp, ok := g.cfg.Group(l.Group); ok {
		grpName = grp.Name
	}
	var members []map[string]any
	for _, m := range l.Members {
		team := "red"
		if m.Team == mm.Blu {
			team = "blu"
		}
		members = append(members, map[string]any{
			"steam_id": strconv.FormatUint(m.SteamID, 10),
			"team":     team,
			"party_id": strconv.FormatUint(m.PartyID, 10),
		})
	}
	out := map[string]any{
		"match_id":    strconv.FormatUint(l.MatchID, 10),
		"match_group": l.Group,
		"group_name":  grpName,
		"map":         l.Map,
		"server":      l.Connect,
		"stv":         l.STV,
		"members":     members,
	}
	if l.War != nil {
		out["war"] = l.War
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// sameMap compares a lobby's map with what a server reports, which may carry
// a workshop path or different case.
func sameMap(want, got string) bool {
	got = strings.ToLower(strings.TrimSpace(got))
	want = strings.ToLower(strings.TrimSpace(want))
	if i := strings.LastIndexByte(got, '/'); i >= 0 {
		got = got[i+1:]
	}
	got = strings.TrimSuffix(got, ".bsp")
	return got != "" && got == want
}
