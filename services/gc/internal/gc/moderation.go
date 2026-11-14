package gc

import (
	"encoding/json"
	"strconv"

	"github.com/gru2007/team-frontress/services/gc/internal/clans"
)

// ModerateClan runs on the GC loop so deletion, tags, invitations and banners
// change together. The store commits the audit and restrictions before removal.
func (g *GC) ModerateClan(a clans.ModerationAction) error {
	before := g.clans.Get(a.Clan)
	if err := g.clans.Moderate(a); err != nil {
		return err
	}
	if a.Action == "ban" || a.Action == "delete" || a.Action == "set_tag" {
		if a.Action != "set_tag" {
			delete(g.chat, "clan:"+a.Clan)
		}
		newTag := ""
		if after := g.clans.Get(a.Clan); after != nil {
			newTag = after.Tag
		}
		if before != nil {
			for channel, lines := range g.chat {
				kept := make([]ChatLine, 0, len(lines))
				for _, line := range lines {
					remove := false
					arg := clanFeedTagArgument(line)
					if arg >= 0 && arg < len(line.Args) && line.Args[arg] == before.Tag {
						line.Args = append([]string(nil), line.Args...)
						if a.Action == "set_tag" {
							line.Args[arg] = newTag
						} else {
							remove = true
						}
					}
					if remove {
						continue
					}
					if line.Tag == before.Tag {
						line.Tag = newTag
						if newTag == "" {
							line.Gold = false
						}
					}
					kept = append(kept, line)
				}
				g.chat[channel] = kept
			}
		}
		// Invitees and players browsing clans also cache the clan's identity.
		for id := range g.online {
			g.sendClan(id)
			g.sendChatHistory(id)
		}
		g.presenceLast = ""
		g.publishWar()
	}
	g.log.Info("clan moderation", "steamid", a.SteamID, "clan", a.Clan, "action", a.Action,
		"by", strconv.FormatUint(a.Actor, 10), "reason", a.Reason, "restrict_members", a.RestrictMembers)
	return nil
}

// Other arguments can be player names or another clan's name. Only the
// documented tag position identifies a clan in a system announcement.
func clanFeedTagArgument(line ChatLine) int {
	switch line.Key {
	case "fui_feed_clan_new", "fui_feed_clan_level", "fui_feed_contract":
		return 0
	case "fui_feed_captured_clan":
		return 2
	default:
		return -1
	}
}

// ClearPlayerChat removes retained menu and match messages and resets clients'
// chat caches. The private journal remains available as moderation evidence.
func (g *GC) ClearPlayerChat(id, actor uint64, reason string) {
	who := strconv.FormatUint(id, 10)
	for channel, lines := range g.chat {
		kept := make([]ChatLine, 0, len(lines))
		for _, line := range lines {
			if line.From != who {
				kept = append(kept, line)
			}
		}
		g.chat[channel] = kept
	}
	for player := range g.online {
		g.sendChatHistory(player)
	}
	g.log.Info("player chat cleared", "steamid", id, "by", strconv.FormatUint(actor, 10), "reason", reason)
}

// ModerateChat uses the existing persistent, revisioned chat settings. Executed
// on the loop, it cannot overwrite a concurrent administrator's chat settings.
func (g *GC) ModerateChat(id, actor uint64, muted bool, reason string) error {
	chat := g.cfg.Chat
	ids := make([]string, 0, len(chat.MutedSteamIDs)+1)
	who := strconv.FormatUint(id, 10)
	for _, value := range chat.MutedSteamIDs {
		if value != who {
			ids = append(ids, value)
		}
	}
	if muted {
		ids = append(ids, who)
	}
	chat.MutedSteamIDs = ids
	raw, err := json.Marshal(chat)
	if err != nil {
		return err
	}
	action := "unmute"
	if muted {
		action = "mute"
	}
	_, err = g.SaveSettings(g.settings.Revision, map[string]json.RawMessage{"chat": raw},
		"admin:"+strconv.FormatUint(actor, 10), action+" text chat "+who+": "+reason)
	if err == nil {
		g.log.Info("player chat moderation", "steamid", id, "by", strconv.FormatUint(actor, 10), "action", action, "reason", reason)
	}
	return err
}

func (g *GC) ClanModeration() map[string]any {
	out := []map[string]any{}
	for _, c := range g.clans.Ranked() {
		card := g.clanCard(c)
		card["members"] = c.Roster()
		out = append(out, card)
	}
	return map[string]any{"clans": out, "moderation": g.clans.Moderation(), "muted_steam_ids": g.cfg.Chat.MutedSteamIDs}
}
