package gc

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// Private server protocol, CMsgPartyChatMsg in both directions:
// report: actor_id=authenticated player; msg="all|team\nname\ntext".
// policy: actor_id=1 when chat is disabled; msg=revision then muted IDs, one per line.
// acknowledgement: actor_id=0; msg="policy_ack\nrevision".
const msgServerChat uint32 = 51030
const msgChatPolicy uint32 = 51031

func init() { serverHandlers[msgServerChat] = (*GC).handleServerChat }

func (g *GC) sendChatPolicy(srv *Server) {
	if srv == nil || srv.Session == nil {
		return
	}
	disabled := uint64(0)
	if g.cfg.Chat.Disabled {
		disabled = 1
	}
	g.send(srv.SteamID, msgChatPolicy, &gcpb.CMsgPartyChatMsg{ActorId: proto.Uint64(disabled), Msg: proto.String(strconv.Itoa(g.settings.Revision) + "\n" + strings.Join(g.cfg.Chat.MutedSteamIDs, "\n"))})
}

func (g *GC) handleServerChat(s *session.Session, m *wire.Msg) {
	var msg gcpb.CMsgPartyChatMsg
	if !g.parse(m, &msg) {
		return
	}
	srv, lobby := g.serverLobby(s, 0)
	if srv == nil || srv.Session != s {
		return
	}
	if msg.GetActorId() == 0 {
		if raw, ok := strings.CutPrefix(msg.GetMsg(), "policy_ack\n"); ok {
			if rev, err := strconv.Atoi(raw); err == nil && rev == g.settings.Revision {
				srv.ChatPolicyRevision = rev
			}
		}
		return
	}
	if !steamauth.ValidIndividual(msg.GetActorId()) {
		return
	}
	if lobby != nil {
		if lobby.member(msg.GetActorId()) == nil {
			return
		}
	}
	parts := strings.SplitN(msg.GetMsg(), "\n", 3)
	if len(parts) != 3 || (parts[0] != "all" && parts[0] != "team") {
		return
	}
	text := cleanLine(parts[2], chatMaxRunes)
	if text == "" {
		return
	}
	line := ChatLine{Ch: "match_" + parts[0], From: strconv.FormatUint(msg.GetActorId(), 10), Name: cleanLine(parts[1], 32), Text: text, Server: strconv.FormatUint(srv.SteamID, 10)}
	if lobby != nil {
		line.Match = strconv.FormatUint(lobby.MatchID, 10)
	}
	// This channel has no player subscribers. Match/team messages are visible
	// only to authenticated moderators, never to the menu's public chat.
	g.post("server:"+line.Server, line)
	g.log.Debug("match chat", "steamid", msg.GetActorId(), "server", srv.SteamID, "match", line.Match, "channel", line.Ch, "text", text)
}

func (g *GC) ChatPolicyServers() []map[string]any {
	out := []map[string]any{}
	for _, srv := range g.servers {
		if srv.Session == nil {
			continue
		}
		out = append(out, map[string]any{"steam_id": strconv.FormatUint(srv.SteamID, 10), "name": srv.Name, "synced": srv.ChatPolicyRevision == g.settings.Revision})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["steam_id"].(string) < out[j]["steam_id"].(string) })
	return out
}

type ModeratedChatLine struct {
	ChatLine
	Channel string `json:"channel"`
	Source  string `json:"source"`
}

type ChatFilter struct {
	Source  string
	SteamID string
	Q       string
	Before  uint64
	Limit   int
}

func (g *GC) ChatModeration(f ChatFilter) []ModeratedChatLine {
	out := []ModeratedChatLine{}
	for channel, lines := range g.chat {
		source := "menu"
		if strings.HasPrefix(channel, "server:") {
			source = "match"
		}
		if f.Source != "" && f.Source != "all" && f.Source != source {
			continue
		}
		for _, line := range lines {
			if f.Before != 0 && line.ID >= f.Before || f.SteamID != "" && line.From != f.SteamID {
				continue
			}
			if f.Q != "" && !strings.Contains(strings.ToLower(line.Text+" "+line.Name+" "+line.Key+" "+strings.Join(line.Args, " ")), strings.ToLower(f.Q)) {
				continue
			}
			line.Args = append([]string(nil), line.Args...)
			out = append(out, ModeratedChatLine{ChatLine: line, Channel: channel, Source: source})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

var ErrNoChatMessage = errors.New("message is no longer retained")
var ErrChatRestarted = errors.New("chat restarted; reload the page before deleting messages")

func (g *GC) ChatEpoch() string { return g.chatEpoch }

func (g *GC) DeleteChatMessage(id, actor uint64, epoch, reason string) error {
	if epoch != g.chatEpoch {
		return ErrChatRestarted
	}
	for channel, lines := range g.chat {
		for i, line := range lines {
			if line.ID != id {
				continue
			}
			g.chat[channel] = append(lines[:i:i], lines[i+1:]...)
			if !strings.HasPrefix(channel, "server:") {
				for _, player := range g.channelMembers(channel) {
					g.sendChatHistory(player)
				}
			}
			g.log.Info("chat message deleted", "steamid", line.From, "message", id, "channel", channel, "server", line.Server,
				"by", strconv.FormatUint(actor, 10), "reason", reason, "text", line.Text)
			return nil
		}
	}
	return ErrNoChatMessage
}
