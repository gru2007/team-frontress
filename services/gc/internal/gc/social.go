package gc

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/clans"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/session"
	"github.com/gru2007/team-frontress/services/gc/internal/war"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

// Social: what makes a small population feel like a crowd. Global, clan and
// party chat; who is online and what they are doing; who is looking for a
// squad; a feed of what just happened in the war. Plus the clans (clan.go)
// and the support tab (support.go), which ride the same two messages.
//
// The game sends msgSocial, {"op": ...}; the GC answers and pushes
// msgSocialEvent, {"t": ...}. Both are JSON in a CMsgPartyChatMsg, like
// msgNotice. See src/game/client/tf/frontress/fui_social.cpp.
const (
	msgSocial      uint32 = 51010
	msgSocialEvent uint32 = 51011
)

// Chat limits.
const (
	chatMaxRunes   = 200
	chatBurst      = 5               // lines in a row
	chatRefill     = time.Second * 2 // one more line every this long
	presenceEvery  = 4 * time.Second
	presenceListed = 48
)

// ChatLine is one line in a channel. A system line has Key (a string the
// game translates) and Args instead of a sender and text.
type ChatLine struct {
	ID   uint64   `json:"id"`
	At   int64    `json:"at"`
	Ch   string   `json:"ch"` // global, clan, party
	From string   `json:"from,omitempty"`
	Name string   `json:"name,omitempty"`
	Tag  string   `json:"tag,omitempty"`
	Gold bool     `json:"gold,omitempty"`
	Sup  bool     `json:"sup,omitempty"`
	Text string   `json:"text,omitempty"`
	Key  string   `json:"key,omitempty"`
	Args []string `json:"args,omitempty"`
	Kind string   `json:"kind,omitempty"` // a system line's kind: war, clan, support
}

// socialOp is everything a client may send; each op reads its own fields.
type socialOp struct {
	Op     string `json:"op"`
	Name   string `json:"name"`
	Lang   string `json:"lang"`
	Ch     string `json:"ch"`
	Text   string `json:"text"`
	On     bool   `json:"on"`
	Note   string `json:"note"`
	ID     string `json:"id"`
	Who    string `json:"who"`
	Role   string `json:"role"`
	Tag    string `json:"tag"`
	Motto  string `json:"motto"`
	Emblem string `json:"emblem"`
	Color  string `json:"color"`
	Open   bool   `json:"open"`
	Front  string `json:"front"`
	Kind   string `json:"kind"`
	Tier   int    `json:"tier"`
	Anon   bool   `json:"anon"`
	Order  string `json:"order"`
	OK     bool   `json:"ok"`
}

func init() {
	clientHandlers[msgSocial] = func(g *GC, s *session.Session, m *wire.Msg) {
		var body gcpb.CMsgPartyChatMsg
		if !g.parse(m, &body) {
			return
		}
		var op socialOp
		if len(body.GetMsg()) > 4096 || json.Unmarshal([]byte(body.GetMsg()), &op) != nil {
			g.log.Debug("social: bad op", "steamid", s.SteamID)
			return
		}
		g.handleSocial(s.SteamID, op)
	}
}

func (g *GC) handleSocial(id uint64, op socialOp) {
	switch op.Op {
	case "hello":
		g.hello(id, op)
	case "say":
		g.say(id, op.Ch, op.Text)
	case "lfg":
		p := g.player(id)
		p.LFG = op.On
		p.LFGNote = cleanLine(op.Note, 40)
		g.presenceAt = time.Time{} // push now
	case "presence":
		g.sendPresence(id)
	default:
		if strings.HasPrefix(op.Op, "clan_") {
			g.handleClan(id, op)
		} else if strings.HasPrefix(op.Op, "support_") {
			g.handleSupport(id, op)
		}
	}
}

// emit sends a client one event.
func (g *GC) emit(to uint64, t string, fields map[string]any) {
	if !g.isOnline(to) {
		return
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["t"] = t
	raw, err := json.Marshal(fields)
	if err != nil {
		g.log.Warn("social: encode", "t", t, "err", err)
		return
	}
	g.send(to, msgSocialEvent, &gcpb.CMsgPartyChatMsg{Msg: proto.String(string(raw))})
}

// fail tells a player an op did not work, by a string the game translates.
func (g *GC) fail(to uint64, key string) {
	g.emit(to, "error", map[string]any{"key": "fui_err_" + key})
}

// hello is a game that just connected (or reloaded its menu): it tells us
// its name, and gets the chat so far, its clan and who is online.
func (g *GC) hello(id uint64, op socialOp) {
	p := g.player(id)
	if name := cleanLine(op.Name, 32); name != "" {
		p.Name = name
		if g.clans != nil {
			g.clans.Rename(id, name)
		}
	}
	if op.Lang != "" && len(op.Lang) <= 12 {
		p.Lang = op.Lang
	}
	var lines []ChatLine
	lines = append(lines, g.chat["global"]...)
	if c := g.clanKey(id); c != "" {
		lines = append(lines, g.chat[c]...)
	}
	if pk := g.partyChatKey(id); pk != "" {
		lines = append(lines, g.chat[pk]...)
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].ID < lines[j].ID })
	g.emit(id, "chat", map[string]any{"reset": true, "lines": append([]ChatLine{}, lines...)})
	g.sendPresence(id)
	g.sendClan(id)
	g.sendSupportSummary(id)
}

// --- chat --------------------------------------------------------------------

func (g *GC) clanKey(id uint64) string {
	if g.clans == nil {
		return ""
	}
	if _, clan, _ := g.clans.TagOf(id); clan != "" {
		return "clan:" + clan
	}
	return ""
}

func (g *GC) partyChatKey(id uint64) string {
	p := g.online[id]
	if p == nil {
		return ""
	}
	party := g.parties[p.PartyID]
	if party == nil || len(party.Members) < 2 {
		return ""
	}
	return "party:" + strconv.FormatUint(party.ID, 10)
}

// cleanLine is one line of text: no control characters, runs of spaces
// collapsed, at most max runes.
func cleanLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError || (r >= 0x200b && r <= 0x200f) || r == 0xfeff {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

// censor stars out the configured words.
func (g *GC) censor(s string) string {
	if len(g.cfg.Chat.BannedWords) == 0 {
		return s
	}
	lower := []rune(strings.ToLower(s))
	out := []rune(s)
	if len(lower) != len(out) {
		return s
	}
	for _, w := range g.cfg.Chat.BannedWords {
		wr := []rune(strings.ToLower(strings.TrimSpace(w)))
		if len(wr) == 0 {
			continue
		}
		for i := 0; i+len(wr) <= len(lower); i++ {
			if string(lower[i:i+len(wr)]) == string(wr) {
				for k := i; k < i+len(wr); k++ {
					out[k] = '*'
				}
			}
		}
	}
	return string(out)
}

func (g *GC) muted(id uint64) bool {
	ids := strconv.FormatUint(id, 10)
	for _, m := range g.cfg.Chat.MutedSteamIDs {
		if m == ids {
			return true
		}
	}
	return false
}

// say posts a player's line to a channel.
func (g *GC) say(id uint64, ch, text string) {
	if g.cfg.Chat.Disabled {
		return
	}
	text = g.censor(cleanLine(text, chatMaxRunes))
	if text == "" {
		return
	}
	if g.muted(id) {
		g.fail(id, "muted")
		return
	}
	p := g.player(id)
	// A token bucket: a burst, then one line per chatRefill.
	now := g.now()
	if !p.chatAt.IsZero() {
		p.chatTokens += float64(now.Sub(p.chatAt)) / float64(chatRefill)
	} else {
		p.chatTokens = chatBurst
	}
	if p.chatTokens > chatBurst {
		p.chatTokens = chatBurst
	}
	p.chatAt = now
	if p.chatTokens < 1 {
		g.fail(id, "chat_slow")
		return
	}
	p.chatTokens--

	key := "global"
	switch ch {
	case "clan":
		key = g.clanKey(id)
	case "party":
		key = g.partyChatKey(id)
	default:
		ch = "global"
	}
	if key == "" {
		g.fail(id, "no_channel")
		return
	}
	line := ChatLine{Ch: ch, From: strconv.FormatUint(id, 10), Name: p.Name, Text: text}
	if line.Name == "" {
		line.Name = g.players.Get(id).Name
	}
	if g.clans != nil {
		line.Tag, _, line.Gold = g.clans.TagOf(id)
	}
	if g.book != nil {
		line.Sup, _ = g.book.IsSupporter(id)
	}
	g.post(key, line)
}

// post adds a line to a channel's history and sends it to everyone in it.
func (g *GC) post(key string, line ChatLine) {
	g.chatSeq++
	line.ID = g.chatSeq
	line.At = g.now().Unix()
	hist := append(g.chat[key], line)
	if n := g.cfg.Chat.History; n > 0 && len(hist) > n {
		hist = hist[len(hist)-n:]
	}
	g.chat[key] = hist
	for _, to := range g.channelMembers(key) {
		g.emit(to, "chat", map[string]any{"lines": []ChatLine{line}})
	}
}

// feed posts a system line to the global channel: the war, clans, support.
func (g *GC) feed(kind, key string, args ...string) {
	g.post("global", ChatLine{Ch: "global", Key: key, Args: args, Kind: kind})
}

// clanFeed posts a system line to a clan's channel.
func (g *GC) clanFeed(clan, key string, args ...string) {
	g.post("clan:"+clan, ChatLine{Ch: "clan", Key: key, Args: args, Kind: "clan"})
}

func (g *GC) channelMembers(key string) []uint64 {
	var out []uint64
	switch {
	case key == "global":
		for id, p := range g.online {
			if p.Online {
				out = append(out, id)
			}
		}
	case strings.HasPrefix(key, "clan:"):
		if g.clans == nil {
			return nil
		}
		if c := g.clans.Get(strings.TrimPrefix(key, "clan:")); c != nil {
			for id := range c.Members {
				if g.isOnline(id) {
					out = append(out, id)
				}
			}
		}
	case strings.HasPrefix(key, "party:"):
		pid, _ := strconv.ParseUint(strings.TrimPrefix(key, "party:"), 10, 64)
		if party := g.parties[pid]; party != nil {
			for _, id := range party.Members {
				if g.isOnline(id) {
					out = append(out, id)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// --- presence ----------------------------------------------------------------

// PresencePlayer is one person in the online list.
type PresencePlayer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Tag    string `json:"tag,omitempty"`
	Gold   bool   `json:"gold,omitempty"`
	Sup    bool   `json:"sup,omitempty"`
	Level  int    `json:"level"`
	Status string `json:"status"` // menu, queue, match
	Group  int32  `json:"group"`  // the match group they queue for or play
	Map    string `json:"map,omitempty"`
	LFG    bool   `json:"lfg,omitempty"`
	Note   string `json:"note,omitempty"`
	Party  int    `json:"party"` // their party's size
	Since  int64  `json:"since,omitempty"`
}

// Presence is the online picture every menu shows.
type Presence struct {
	Total   int              `json:"total"`
	Menu    int              `json:"menu"`
	Queue   int              `json:"queue"`
	Match   int              `json:"match"`
	LFG     int              `json:"lfg"`
	Clans   int              `json:"clans"`
	Players []PresencePlayer `json:"players"`
}

func (g *GC) presence() Presence {
	pr := Presence{Players: []PresencePlayer{}}
	if g.clans != nil {
		pr.Clans = g.clans.Count()
	}
	var all []PresencePlayer
	for id, p := range g.online {
		if !p.Online {
			continue
		}
		pp := PresencePlayer{ID: strconv.FormatUint(id, 10), Name: p.Name, Status: "menu", Party: 1}
		if party := g.parties[p.PartyID]; party != nil {
			pp.Party = len(party.Members)
			for grp := range party.Queues {
				pp.Status, pp.Group = "queue", grp
			}
		}
		if l := g.lobbyOf(id); l != nil {
			pp.Status, pp.Group, pp.Map = "match", l.Group, l.Map
			p.LFG = false
		}
		pp.LFG, pp.Note = p.LFG, p.LFGNote
		switch pp.Status {
		case "menu":
			pr.Menu++
		case "queue":
			pr.Queue++
		case "match":
			pr.Match++
		}
		if pp.LFG {
			pr.LFG++
		}
		pr.Total++
		// Listed by name only: a game says hello with its name as it
		// connects, so a nameless one is counted but not shown.
		if pp.Name == "" {
			pp.Name = g.players.Get(id).Name
		}
		if pp.Name != "" {
			all = append(all, pp)
		}
	}
	// LFG first, then free in the menu, then by name; the list is capped,
	// so the people you can play with right now are the ones on it.
	rank := func(p PresencePlayer) int {
		switch {
		case p.LFG:
			return 0
		case p.Status == "menu":
			return 1
		case p.Status == "queue":
			return 2
		}
		return 3
	}
	sort.Slice(all, func(i, j int) bool {
		if rank(all[i]) != rank(all[j]) {
			return rank(all[i]) < rank(all[j])
		}
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].ID < all[j].ID
	})
	if len(all) > presenceListed {
		all = all[:presenceListed]
	}
	for i := range all {
		id, _ := strconv.ParseUint(all[i].ID, 10, 64)
		rec := g.players.Get(id)
		all[i].Level = rec.Level()
		if all[i].Name == "" {
			all[i].Name = rec.Name
		}
		if g.clans != nil {
			all[i].Tag, _, all[i].Gold = g.clans.TagOf(id)
		}
		if g.book != nil {
			all[i].Sup, _ = g.book.IsSupporter(id)
		}
	}
	pr.Players = append(pr.Players, all...)
	return pr
}

func (g *GC) sendPresence(to uint64) {
	pr := g.presence()
	g.emit(to, "online", map[string]any{"online": pr})
}

// tickPresence pushes the online picture to everyone when it changed.
func (g *GC) tickPresence() {
	now := g.now()
	if now.Sub(g.presenceAt) < presenceEvery {
		return
	}
	g.presenceAt = now
	pr := g.presence()
	g.presenceCounts = pr
	g.presenceCounts.Players = nil
	raw, _ := json.Marshal(pr)
	if string(raw) == g.presenceLast {
		return
	}
	g.presenceLast = string(raw)
	for id, p := range g.online {
		if p.Online {
			g.emit(id, "online", map[string]any{"online": pr})
		}
	}
}

// warFeed tells everyone how a war battle went, and when a sector fell.
func (g *GC) warFeed(l *Lobby, attackerWon bool, ownerBefore war.Side) {
	attacker := war.Side(l.War.Attacker)
	winner := attacker
	if !attackerWon {
		winner = attacker.Other()
	}
	name := l.War.NodeName
	if name == "" {
		name = g.nodeName(l.War.NodeID)
	}
	after := g.war.Owner(l.War.NodeID)
	if after == ownerBefore {
		g.feed("war", "fui_feed_battle", name, "#fui_side_"+side(winner), strconv.Itoa(l.War.StageIndex+1), strconv.Itoa(l.War.StageCount))
		return
	}
	// The sector fell. A legendary clan that fought hardest for it is named.
	if g.clans != nil {
		if c := g.clans.Banners()[l.War.NodeID]; c != nil && clans.Has(c.Level(), "legend") {
			g.feed("war", "fui_feed_captured_clan", name, "#fui_side_"+side(after), c.Tag)
			return
		}
	}
	g.feed("war", "fui_feed_captured", name, "#fui_side_"+side(after))
}
