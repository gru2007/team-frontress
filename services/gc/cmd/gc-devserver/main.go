// gc-devserver joins a coordinator's server pool as a pretend dedicated
// server, for working on the game's side of matchmaking without one (the
// macOS development engine has no dedicated server).
//
// It speaks the same transport as src/game/shared/tf/frontress_gc.cpp: it
// reports itself empty, takes the match the GC hands it, says the map is up
// so the lobby goes live (the players get "match found"), and after -after
// seconds reports a result -- which moves the war, if the match was a war
// battle -- and closes the lobby. Then it waits for the next one.
//
//	go run ./cmd/gc-devserver -gc http://127.0.0.1:27100 -key <server key> -win red
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/httpapi"
	"github.com/gru2007/team-frontress/services/gc/internal/socache"
	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

type server struct {
	base    string
	session string
	outSeq  uint64
	ack     uint64
	lobby   *gcpb.CSOTFGameServerLobby
	replied map[uint32]bool
	// onSocial gets the GC's social events (JSON), for the chatting stand-ins.
	onSocial func(event map[string]any)
}

func main() {
	base := flag.String("gc", "http://127.0.0.1:27100", "the coordinator")
	key := flag.String("key", "", "a server key from the coordinator's server_keys")
	steamID := flag.Uint64("steamid", 90071992547409920, "this server's SteamID (any game server id)")
	connect := flag.String("connect", "127.0.0.1:27015", "the address players are told to connect to")
	region := flag.String("region", "", "the region this server reports")
	after := flag.Duration("after", 30*time.Second, "how long a match runs before the result")
	win := flag.String("win", "red", "who wins: red, blu or none")
	once := flag.Bool("once", false, "play one match, then quit")
	fill := flag.Int("fill", 0, "this many pretend players queue for -group too, so one real player gets a match")
	group := flag.Int("group", 7, "the match group -fill queues for")
	chatters := flag.Int("chatters", 0, "this many pretend players sit in the menu: they chat, look for a squad and found a clan")
	chatLang := flag.String("chat-lang", "ru", "what the chatters speak: ru or en")
	chatEvery := flag.Duration("chat-every", 15*time.Second, "how often each chatter says something (later ones a little less)")
	noClan := flag.Bool("chat-no-clan", false, "chatters do not found or join a clan")
	flag.Parse()
	// Every stand-in long-polls; keep their connections instead of opening
	// a new one per request, which runs a machine out of ports.
	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = 128
	if *key == "" {
		log.Fatal("-key is required")
	}

	s := &server{base: strings.TrimRight(*base, "/"), replied: map[uint32]bool{}}
	if err := s.connect(url.Values{
		"kind": {"server"}, "key": {*key}, "connect": {*connect}, "region": {*region},
		"name": {"gc-devserver"}, "steam_id": {strconv.FormatUint(*steamID, 10)},
	}); err != nil {
		log.Fatal(err)
	}
	log.Printf("in the pool of %s as %d", s.base, *steamID)
	for i := 0; i < *fill; i++ {
		go queueStandIn(s.base, 76561197960265729+(*steamID%1000)*16+uint64(i), int32(*group))
	}
	for i := 0; i < *chatters; i++ {
		go chatStandIn(s.base, 76561197960265729+(*steamID%1000)*16+8+uint64(i), i, *chatLang, *chatEvery, *noClan)
	}

	for {
		// Empty and waiting.
		for s.lobby == nil {
			s.status(gcpb.ServerMatchmakingState_ServerMatchmakingState_EMPTY, "", nil)
			s.pumpFor(5 * time.Second)
		}
		l := s.lobby
		log.Printf("got match %d on %s: %d players", l.GetMatchId(), l.GetMapName(), len(l.GetMembers()))

		// The map is up and the players are expected: the lobby goes live.
		var players []*gcpb.CMsgGameServerMatchmakingStatus_Player
		for _, m := range l.GetMembers() {
			players = append(players, &gcpb.CMsgGameServerMatchmakingStatus_Player{
				SteamId: proto.Uint64(m.GetId()), ConnectState: gcpb.CMsgGameServerMatchmakingStatus_RESERVED.Enum(),
			})
		}
		s.status(gcpb.ServerMatchmakingState_ServerMatchmakingState_ACTIVE_MATCH, l.GetMapName(), players)
		log.Printf("match live; result in %s", *after)
		deadline := time.Now().Add(*after)
		for time.Now().Before(deadline) {
			s.status(gcpb.ServerMatchmakingState_ServerMatchmakingState_ACTIVE_MATCH, l.GetMapName(), players)
			s.pumpFor(5 * time.Second)
		}

		// The result. Defenders wear RED (2), invaders BLU (3).
		winner := uint32(0)
		switch *win {
		case "red":
			winner = 2
		case "blu":
			winner = 3
		}
		res := &gcpb.CMsgGC_Match_Result{MatchId: proto.Uint64(l.GetMatchId()), WinningTeam: proto.Uint32(winner)}
		for _, m := range l.GetMembers() {
			team := uint32(2)
			if m.GetTeam() == gcpb.TF_GC_TEAM_TF_GC_TEAM_INVADERS {
				team = 3
			}
			res.Players = append(res.Players, &gcpb.CMsgGC_Match_Result_Player{
				SteamId: proto.Uint64(m.GetId()), Team: proto.Uint32(team),
				Kills: proto.Uint32(12), Damage: proto.Uint32(3400), Score: proto.Uint32(30),
			})
		}
		s.send(uint32(gcpb.ETFGCMsg_k_EMsgGC_Match_Result), res)
		for !s.replied[uint32(gcpb.ETFGCMsg_k_EMsgGC_Match_ResultResponse)] {
			s.pumpFor(2 * time.Second)
		}
		s.send(uint32(gcpb.ETFGCMsg_k_EMsgGCGameServerKickingLobby), &gcpb.CMsgGameServerKickingLobby{
			LobbyId: proto.Uint64(l.GetLobbyId()), MatchId: proto.Uint64(l.GetMatchId()),
		})
		for s.lobby != nil {
			s.pumpFor(2 * time.Second)
		}
		log.Printf("match %d over (winner: %s)", l.GetMatchId(), *win)
		delete(s.replied, uint32(gcpb.ETFGCMsg_k_EMsgGC_Match_ResultResponse))
		if *once {
			return
		}
	}
}

func (s *server) connect(form url.Values) error {
	form.Set("protocol", strconv.Itoa(httpapi.ProtocolVersion))
	resp, err := http.PostForm(s.base+"/gc/v1/session", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("session: HTTP %d: %s", resp.StatusCode, body)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(line, "session="); ok {
			s.session = v
		}
	}
	if s.session == "" {
		return fmt.Errorf("no session in %q", body)
	}
	return nil
}

func (s *server) status(state gcpb.ServerMatchmakingState, mapName string, players []*gcpb.CMsgGameServerMatchmakingStatus_Player) {
	m := &gcpb.CMsgGameServerMatchmakingStatus{MatchmakingState: state.Enum(), Players: players}
	if mapName != "" {
		m.Map = proto.String(mapName)
	}
	s.send(uint32(gcpb.ETFGCMsg_k_EMsgGCGameServerMatchmakingStatus), m)
}

func (s *server) send(msgType uint32, body proto.Message) {
	data, err := wire.Encode(msgType, body, wire.NoJob)
	if err != nil {
		log.Fatal(err)
	}
	// With a source job, as a reliable message has, so replies come back.
	m, _ := wire.Decode(data)
	m.Header.JobIdSource = proto.Uint64(4242)
	hdr, _ := proto.Marshal(m.Header)
	raw := append([]byte{}, data[:4]...)
	raw = append(raw, byte(len(hdr)), byte(len(hdr)>>8), byte(len(hdr)>>16), byte(len(hdr)>>24))
	raw = append(raw, hdr...)
	raw = append(raw, m.Body...)
	s.outSeq++
	batch := wire.AppendFrame(nil, wire.Frame{Seq: s.outSeq, Type: msgType | wire.ProtoFlag, Data: raw})
	var resp *http.Response
	for try := 0; ; try++ {
		req, _ := http.NewRequest(http.MethodPost, s.base+"/gc/v1/send", bytes.NewReader(batch))
		req.Header.Set("X-GC-Session", s.session)
		var err error
		if resp, err = http.DefaultClient.Do(req); err == nil {
			break
		}
		if try == 5 {
			log.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		log.Fatalf("send: HTTP %d", resp.StatusCode)
	}
}

// pumpFor receives for a while, keeping the lobby object up to date.
func (s *server) pumpFor(d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/gc/v1/recv?ack="+strconv.FormatUint(s.ack, 10), nil)
	req.Header.Set("X-GC-Session", s.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			time.Sleep(d)
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return
	}
	body, _ := io.ReadAll(resp.Body)
	frames, err := wire.ParseBatch(body)
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range frames {
		if f.Seq <= s.ack {
			continue
		}
		s.ack = f.Seq
		m, err := wire.Decode(f.Data)
		if err != nil {
			log.Fatal(err)
		}
		switch gcpb.ESOMsg(m.Type) {
		case gcpb.ESOMsg_k_ESOMsg_Create, gcpb.ESOMsg_k_ESOMsg_Update, gcpb.ESOMsg_k_ESOMsg_Destroy:
			var so gcpb.CMsgSOSingleObject
			if proto.Unmarshal(m.Body, &so) != nil || so.GetTypeId() != socache.TypeLobby {
				continue
			}
			if gcpb.ESOMsg(m.Type) == gcpb.ESOMsg_k_ESOMsg_Destroy {
				s.lobby = nil
				continue
			}
			var l gcpb.CSOTFGameServerLobby
			if proto.Unmarshal(so.GetObjectData(), &l) == nil {
				s.lobby = &l
			}
		default:
			if m.Type == 51011 && s.onSocial != nil {
				var body gcpb.CMsgPartyChatMsg
				ev := map[string]any{}
				if proto.Unmarshal(m.Body, &body) == nil && json.Unmarshal([]byte(body.GetMsg()), &ev) == nil {
					s.onSocial(ev)
				}
				continue
			}
			if m.Header.GetJobIdTarget() == 4242 {
				s.replied[m.Type] = true
			}
		}
	}
}

// queueStandIn is a pretend player (the GC must be in dev auth mode): it
// connects, gets its party, and queues; it requeues whenever it is free.
func queueStandIn(base string, id uint64, group int32) {
	c := &server{base: base, replied: map[uint32]bool{}}
	if err := c.connect(url.Values{"kind": {"client"}, "ticket": {"dev"}, "steam_id": {strconv.FormatUint(id, 10)}}); err != nil {
		log.Printf("stand-in %d: %v", id, err)
		return
	}
	standInNames := []string{"Saxton", "Redmond", "Blutarch", "Zepheniah", "Gray", "Olivia"}
	c.send(51010, &gcpb.CMsgPartyChatMsg{Msg: proto.String(fmt.Sprintf(`{"op":"hello","name":%q}`, standInNames[id%uint64(len(standInNames))]))})
	log.Printf("stand-in player %d queued for group %d", id, group)
	for {
		c.send(uint32(gcpb.ETFGCMsg_k_EMsgGCParty_QueueForMatch), &gcpb.CMsgPartyQueueForMatch{
			MatchGroup: gcpb.ETFMatchGroup(group).Enum(),
		})
		c.pumpFor(10 * time.Second)
	}
}

var chatNames = map[string][]string{
	"ru": {"Медведь", "Spycrab", "Dispenser", "Mumbles", "Doktor", "Saxton"},
	"en": {"Pootis", "Spycrab", "Dispenser", "Mumbles", "Doktor", "Saxton"},
}
var chatLines = map[string][][]string{
	"ru": {
		{"всем привет, кто на войну?", "Развязка почти наша, нужны медики", "го в отряд, есть место"},
		{"кто-нибудь на 2fort?", "ищу шпиона в пару", "gg, отличный бой был"},
		{"инж с раздатчиком ищет отряд", "кто строит телепорт на ГЭС?", "сервер огонь, пинг 20"},
		{"медик с убером, беру в отряд", "держим Лесопилку, BLU лезут", "кто в клан? набираем"},
	},
	"en": {
		{"hey all, who's up for the war?", "Junction is almost ours, need medics", "squad has room, join up"},
		{"anyone for 2fort?", "looking for a spy duo", "gg, great match"},
		{"engie with a dispenser, need a squad", "who's building a tele at the dam?", "server feels great, 20 ping"},
		{"medic with uber, taking a squad", "holding the Sawmill, BLU pushing", "our clan is recruiting"},
	},
}

// chatStandIn is a pretend player in the menu (the GC must be in dev auth
// mode): it says hello, the first one founds a clan the others join, some
// look for a squad, and they all chat now and then.
func chatStandIn(base string, id uint64, n int, lang string, every time.Duration, noClan bool) {
	if chatNames[lang] == nil {
		lang = "ru"
	}
	c := &server{base: base, replied: map[uint32]bool{}}
	if err := c.connect(url.Values{"kind": {"client"}, "ticket": {"dev"}, "steam_id": {strconv.FormatUint(id, 10)}}); err != nil {
		log.Printf("chatter %d: %v", id, err)
		return
	}
	op := func(v string) {
		c.send(51010, &gcpb.CMsgPartyChatMsg{Msg: proto.String(v)})
	}
	joined := n == 0 || noClan
	c.onSocial = func(ev map[string]any) {
		if ev["t"] != "clan" || joined || n >= 3 {
			return
		}
		if ev["clan"] != nil {
			joined = true
			return
		}
		if top, _ := ev["top"].([]any); len(top) > 0 {
			if first, _ := top[0].(map[string]any); first != nil {
				joined = true
				op(fmt.Sprintf(`{"op":"clan_join","id":%q}`, first["id"]))
			}
		}
	}
	name := chatNames[lang][n%len(chatNames[lang])]
	op(fmt.Sprintf(`{"op":"hello","name":%q,"lang":"russian"}`, name))
	c.pumpFor(time.Second)
	if n == 0 && !noClan {
		op(`{"op":"clan_create","name":"Ржавые Ключи","tag":"РК","motto":"Где ключ — там и раздатчик","emblem":"engineer","color":"gold","open":true}`)
	}
	c.pumpFor(2 * time.Second)
	if n > 0 && n < 3 {
		c.pumpFor(time.Duration(n) * time.Second)
		// Join whatever clan is first in the list: the one the first chatter founded.
		op(`{"op":"clan_browse"}`)
	}
	if n%2 == 1 {
		note := map[string]string{"ru": "ищу отряд на войну", "en": "looking for a war squad"}[lang]
		op(fmt.Sprintf(`{"op":"lfg","on":true,"note":%q}`, note))
	}
	log.Printf("chatter %s (%d) in the menu", name, id)
	lines := chatLines[lang][n%len(chatLines[lang])]
	for i := 0; ; i++ {
		// pumpFor returns early whenever something arrives; wait the whole time.
		next := time.Now().Add(every + time.Duration(n)*every/3)
		for time.Now().Before(next) {
			c.pumpFor(time.Until(next))
		}
		op(fmt.Sprintf(`{"op":"say","ch":"global","text":%q}`, lines[i%len(lines)]))
	}
}
