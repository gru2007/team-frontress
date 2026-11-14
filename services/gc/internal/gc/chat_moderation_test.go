package gc

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/clans"
	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func TestMatchChatPrivacyDeletionAndPolicySynchronization(t *testing.T) {
	cfg := config.Config{MatchGroups: []config.MatchGroupConfig{{MatchGroup: 7, Enabled: true, MinPlayers: 2, MaxPlayers: 4, Maps: []string{"koth_viaduct"}}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	g, err := New(Deps{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	id := steamauth.MinIndividual + 1
	client := g.sessions.Open(session.Client, id, nil)
	g.ClientConnected(client, true)
	server := g.sessions.Open(session.Server, 90000000000000001, nil)
	g.ServerConnected(server)
	send := func(s *session.Session, typ uint32, body proto.Message) {
		t.Helper()
		raw, err := wire.Encode(typ, body, wire.NoJob)
		if err != nil {
			t.Fatal(err)
		}
		g.Handle(s, raw)
	}
	report := &gcpb.CMsgPartyChatMsg{ActorId: proto.Uint64(id), Msg: proto.String("team\nPlayer\nprivate team message")}
	send(client, msgServerChat, report)
	if len(g.ChatModeration(ChatFilter{Source: "match"})) != 0 {
		t.Fatal("client forged a server message")
	}
	send(server, msgServerChat, report)
	lines := g.ChatModeration(ChatFilter{Source: "match"})
	if len(lines) != 1 || lines[0].Ch != "match_team" || lines[0].Server != "90000000000000001" {
		t.Fatalf("missing private match message: %+v", lines)
	}
	if len(g.chat["global"]) != 0 || len(g.ChatModeration(ChatFilter{Source: "menu"})) != 0 {
		t.Fatal("team message leaked into menu")
	}
	if err := g.DeleteChatMessage(lines[0].ID, id, "previous process", "reason"); err != ErrChatRestarted {
		t.Fatal("stale page deleted a new message", err)
	}
	if err := g.DeleteChatMessage(lines[0].ID, id, g.ChatEpoch(), "reason"); err != nil {
		t.Fatal(err)
	}
	if len(g.ChatModeration(ChatFilter{Source: "match"})) != 0 {
		t.Fatal("message survived deletion")
	}
	if err := g.DeleteChatMessage(lines[0].ID, id, g.ChatEpoch(), "reason"); err != ErrNoChatMessage {
		t.Fatal("missing message deleted", err)
	}
	if err := g.ModerateChat(id, id, true, "abuse"); err != nil {
		t.Fatal(err)
	}
	frames, _ := server.Poll(0, time.Millisecond, time.Now)
	var policy gcpb.CMsgPartyChatMsg
	for _, frame := range frames {
		if frame.Type&^wire.ProtoFlag == msgChatPolicy {
			m, _ := wire.Decode(frame.Data)
			if err := proto.Unmarshal(m.Body, &policy); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !strings.Contains(policy.GetMsg(), strconv.FormatUint(id, 10)) {
		t.Fatal("mute policy did not reach game server", policy.GetMsg())
	}
	if g.ChatPolicyServers()[0]["synced"] != false {
		t.Fatal("unacknowledged policy reported as synced")
	}
	revision := strconv.Itoa(g.settings.Revision)
	send(server, msgServerChat, &gcpb.CMsgPartyChatMsg{Msg: proto.String("policy_ack\n" + revision)})
	if g.ChatPolicyServers()[0]["synced"] != true {
		t.Fatal("policy acknowledgement lost")
	}
	party := g.parties[g.player(id).PartyID]
	friend := g.sessions.Open(session.Client, id+1, nil)
	g.ClientConnected(friend, true)
	if err := g.joinParty(party, friend.SteamID); err != nil {
		t.Fatal(err)
	}
	send(client, uint32(gcpb.ETFGCMsg_k_EMsgGCParty_SendChat), &gcpb.CMsgPartySendChat{PartyId: proto.Uint64(party.ID), Msg: proto.String("muted native party chat")})
	if len(g.ChatModeration(ChatFilter{Source: "menu"})) != 0 {
		t.Fatal("native party chat bypassed mute")
	}
	if err := g.ModerateChat(id, id, false, "appeal"); err != nil {
		t.Fatal(err)
	}
	send(client, uint32(gcpb.ETFGCMsg_k_EMsgGCParty_SendChat), &gcpb.CMsgPartySendChat{PartyId: proto.Uint64(party.ID), Msg: proto.String("allowed native party chat")})
	if len(g.ChatModeration(ChatFilter{Source: "menu"})) != 1 {
		t.Fatal("unmuted native chat missing")
	}
	if g.ChatPolicyServers()[0]["synced"] != false {
		t.Fatal("old acknowledgement accepted for changed policy")
	}
	server2 := g.sessions.Open(session.Server, server.SteamID, nil)
	g.ServerConnected(server2)
	send(server, msgServerChat, report)
	if len(g.ChatModeration(ChatFilter{Source: "match"})) != 0 {
		t.Fatal("superseded server session accepted")
	}
	g.servers[server.SteamID].LobbyID = 9
	g.lobbies[9] = &Lobby{ID: 9, ServerID: server.SteamID, MatchID: 10, Members: []*Member{{SteamID: id + 1}}}
	send(server2, msgServerChat, report)
	if len(g.ChatModeration(ChatFilter{Source: "match"})) != 0 {
		t.Fatal("server forged a player outside its match roster")
	}
}

func TestTagChangeRefreshesOnlyClanTags(t *testing.T) {
	g, err := New(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := g.clans.Create(1, "Leader", clans.Settings{Name: "Clan remains", Tag: "OLD"})
	g.feed("clan", "fui_feed_clan_new", "OLD", c.Name)
	g.feed("clan", "fui_feed_clan_new", "OTHER", "OLD")
	g.clanFeed(c.ID, "fui_feed_clan_joined", "OLD")
	g.post("global", ChatLine{Tag: "OLD", Gold: true, Text: "ordinary message"})
	if err := g.ModerateClan(clans.ModerationAction{Action: "set_tag", Clan: c.ID, Actor: 99, Reason: "fix", NewTag: "NEW"}); err != nil {
		t.Fatal(err)
	}
	global := g.chat["global"]
	if global[0].Args[0] != "NEW" || global[1].Args[1] != "OLD" || global[2].Tag != "NEW" || !global[2].Gold {
		t.Fatal("incorrect tag refresh", global)
	}
	if g.chat["clan:"+c.ID][0].Args[0] != "OLD" {
		t.Fatal("player name changed with clan tag")
	}
}
