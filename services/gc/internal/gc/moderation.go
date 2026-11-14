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
	if a.Action == "ban" || a.Action == "delete" {
		delete(g.chat, "clan:"+a.Clan)
		if before != nil {
			for channel, lines := range g.chat {
				kept := make([]ChatLine, 0, len(lines))
				for _, line := range lines {
					remove := false
					if line.Kind == "clan" || line.Kind == "war" {
						for _, arg := range line.Args {
							if arg == before.Name || arg == before.Tag {
								remove = true
							}
						}
					}
					if remove {
						continue
					}
					if line.Tag == before.Tag {
						line.Tag, line.Gold = "", false
					}
					kept = append(kept, line)
				}
				g.chat[channel] = kept
			}
		}
		// Refresh everybody: an invitee or a player browsing clans can also
		// have a cached reference to the removed clan.
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

// ClearPlayerChat removes retained menu messages and resets connected clients'
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
		"admin:"+strconv.FormatUint(actor, 10), action+" menu chat "+who+": "+reason)
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
