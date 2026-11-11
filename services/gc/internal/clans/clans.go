// Package clans keeps the clans: who is in which, what they have earned
// together, and what that unlocks.
//
// A clan is more than a tag. Members earn clan XP by playing (more when they
// play together), the clan levels up, and each level unlocks a perk that
// changes something real: an XP bonus for fighting beside clanmates, the
// clan's banner on the war map over the sector it fought hardest for, a
// weekly clan contract, the clan's own order, a bigger roster. See Perks.
//
// Like package players, state is a fold over an append-only JSONL log, so a
// restart replays to the same clans. An empty path keeps everything in memory.
package clans

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Role is a member's rank in the clan.
type Role string

const (
	Leader  Role = "leader"
	Officer Role = "officer"
	Soldier Role = "member"
)

// Perk is something a clan level unlocks.
type Perk struct {
	ID    string `json:"id"`
	Level int    `json:"level"`
}

// Perks, by the level that unlocks them. The game names and explains them
// (fui_clan_perk_<id>); the GC is what makes each one true.
var Perks = []Perk{
	{"tag", 1},      // the tag on nameplates and in chat; the clan channel
	{"brothers", 2}, // +BrothersBonus% match XP when a clanmate fought in the same match
	{"banner", 3},   // the clan's banner on the war map, over its sector
	{"contract", 4}, // a weekly clan contract: a shared goal that pays everyone
	{"order", 5},    // officers pick an operation; battles in it pay OrderBonusXP
	{"roster", 6},   // room for BigRoster members instead of SmallRoster
	{"veterans", 7}, // the brothers bonus becomes VeteransBonus%
	{"gold", 8},     // a gold tag
	{"legend", 10},  // the war feed names the clan when its sector falls
}

const (
	SmallRoster   = 20
	BigRoster     = 40
	BrothersBonus = 10 // percent of the match's XP
	VeteransBonus = 20
	OrderBonusXP  = 50
	MaxLevel      = 10

	// Clan XP a member earns the clan per match.
	XPPerMatch    = 20
	XPWinBonus    = 10
	XPTogetherPct = 50 // more when two or more clanmates played the match

	// The weekly contract.
	ContractClanXP   = 1500
	ContractMemberXP = 300
)

// levelXP is the clan XP each level starts at.
var levelXP = [MaxLevel]int{0, 600, 1800, 3600, 6000, 9500, 14000, 20000, 28000, 38000}

// LevelFor is the level a clan XP total earns.
func LevelFor(xp int) int {
	lvl := 1
	for i, need := range levelXP {
		if xp >= need {
			lvl = i + 1
		}
	}
	return lvl
}

// LevelSpan is where a level starts and where the next one does. At the top
// level both are the same.
func LevelSpan(level int) (from, to int) {
	if level < 1 {
		level = 1
	}
	if level >= MaxLevel {
		return levelXP[MaxLevel-1], levelXP[MaxLevel-1]
	}
	return levelXP[level-1], levelXP[level]
}

// LevelStart is the clan XP a level starts at.
func LevelStart(level int) int {
	if level < 1 {
		level = 1
	}
	if level > MaxLevel {
		level = MaxLevel
	}
	return levelXP[level-1]
}

// NextPerk is the first perk above a level, or nil at the top.
func NextPerk(level int) *Perk {
	for _, p := range Perks {
		if p.Level > level {
			p := p
			return &p
		}
	}
	return nil
}

// Has reports whether a level has a perk.
func Has(level int, perk string) bool {
	for _, p := range Perks {
		if p.ID == perk {
			return level >= p.Level
		}
	}
	return false
}

// Emblems and colors a clan can pick. Emblems are the nine classes' icons.
var (
	Emblems = []string{"scout", "soldier", "pyro", "demoman", "heavy", "engineer", "medic", "sniper", "spy"}
	Colors  = []string{"red", "blu", "gold", "green", "purple", "teal"}
)

// Contract is a week's shared goal.
type Contract struct {
	Kind   string `json:"kind"` // wins, war, together
	Target int    `json:"target"`
}

var contracts = []Contract{{"wins", 25}, {"war", 15}, {"together", 10}}

// Week is a week number: weeks since the Unix epoch, starting on Monday.
func Week(t time.Time) int { return int((t.Unix() + 3*86400) / (7 * 86400)) }

// WeekEnds is when a week ends.
func WeekEnds(week int) time.Time { return time.Unix(int64(week+1)*7*86400-3*86400, 0) }

// ContractFor is the contract for a week. Every clan gets the same one.
func ContractFor(week int) Contract { return contracts[week%len(contracts)] }

// Member is one person in a clan.
type Member struct {
	SteamID uint64    `json:"steam_id,string"`
	Name    string    `json:"name"`
	Role    Role      `json:"role"`
	Joined  time.Time `json:"joined"`
	XP      int       `json:"xp"` // clan XP they earned, all time
	WeekXP  int       `json:"week_xp"`
	week    int
}

// Clan is one clan.
type Clan struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Tag     string             `json:"tag"`
	Motto   string             `json:"motto"`
	Emblem  string             `json:"emblem"`
	Color   string             `json:"color"`
	Open    bool               `json:"open"`
	Created time.Time          `json:"created"`
	XP      int                `json:"xp"`
	Members map[uint64]*Member `json:"-"`
	// Order is the war operation (a front id) the officers sent the clan to.
	Order string `json:"order"`
	// War: battles won by members, by sector, and in all.
	Sectors map[string]int `json:"-"`
	Battles int            `json:"battles"`
	Wins    int            `json:"wins"`
	// This week's contract.
	Week     int  `json:"week"`
	Progress int  `json:"progress"`
	Paid     bool `json:"paid"`
	// Invites by SteamID, to when they were sent. Older than InviteTTL they
	// no longer count.
	Invites map[uint64]time.Time `json:"-"`
}

// InviteTTL is how long an invite stands.
const InviteTTL = 7 * 24 * time.Hour

// CanInvite reports whether a member of this rank may invite: officers
// always, anyone in an open clan (a stranger could walk in anyway).
func (c *Clan) CanInvite(r Role) bool { return r != Soldier || c.Open }

// WeekXP is the clan XP its members earned this week. Right on copies the
// Store hands out, which drop last week's numbers.
func (c *Clan) WeekXP() int {
	n := 0
	for _, m := range c.Members {
		n += m.WeekXP
	}
	return n
}

// Level is the clan's level.
func (c *Clan) Level() int { return LevelFor(c.XP) }

// Cap is how many members the clan has room for.
func (c *Clan) Cap() int {
	if Has(c.Level(), "roster") {
		return BigRoster
	}
	return SmallRoster
}

// Banner is the sector the clan won most battles in, if it has one.
func (c *Clan) Banner() (string, int) {
	best, n := "", 0
	for id, wins := range c.Sectors {
		if wins > n || (wins == n && id < best) {
			best, n = id, wins
		}
	}
	return best, n
}

// Roster is the members, leader first, then by rank and clan XP.
func (c *Clan) Roster() []Member {
	out := make([]Member, 0, len(c.Members))
	for _, m := range c.Members {
		out = append(out, *m)
	}
	rank := map[Role]int{Leader: 0, Officer: 1, Soldier: 2}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Role] != rank[out[j].Role] {
			return rank[out[i].Role] < rank[out[j].Role]
		}
		if out[i].XP != out[j].XP {
			return out[i].XP > out[j].XP
		}
		return out[i].SteamID < out[j].SteamID
	})
	return out
}

// Errors a player can see. The game shows them translated by their text.
var (
	ErrInClan    = errors.New("already_in_clan")
	ErrNotInClan = errors.New("not_in_clan")
	ErrNoClan    = errors.New("no_such_clan")
	ErrName      = errors.New("bad_name")
	ErrTag       = errors.New("bad_tag")
	ErrTaken     = errors.New("name_taken")
	ErrClosed    = errors.New("clan_closed")
	ErrFull      = errors.New("clan_full")
	ErrRank      = errors.New("not_allowed")
	ErrPerk      = errors.New("perk_locked")
	ErrSelf      = errors.New("not_yourself")
	ErrNotMember = errors.New("not_a_member")
)

type event struct {
	Type   string    `json:"t"`
	At     time.Time `json:"at"`
	Clan   string    `json:"clan"`
	By     uint64    `json:"by,string,omitempty"`
	Who    uint64    `json:"who,string,omitempty"`
	Name   string    `json:"name,omitempty"`
	Tag    string    `json:"tag,omitempty"`
	Motto  string    `json:"motto,omitempty"`
	Emblem string    `json:"emblem,omitempty"`
	Color  string    `json:"color,omitempty"`
	Open   *bool     `json:"open,omitempty"`
	Role   Role      `json:"role,omitempty"`
	Order  *string   `json:"order,omitempty"`
	XP     int       `json:"xp,omitempty"`
	Sector string    `json:"sector,omitempty"`
	Won    bool      `json:"won,omitempty"`
	War    bool      `json:"war,omitempty"`
	With   bool      `json:"with,omitempty"` // played beside a clanmate
	// Founder is the founder's name, on a create.
	Founder string `json:"founder,omitempty"`
}

const (
	evCreate   = "create"
	evJoin     = "join"
	evLeave    = "leave"
	evRole     = "role"
	evSettings = "settings"
	evOrder    = "order"
	evPlayed   = "played"
	evInvite   = "invite"
	evUninvite = "uninvite"
	evPaid     = "paid"
	evDisband  = "disband"
)

// Store is every clan.
type Store struct {
	mu    sync.Mutex
	clans map[string]*Clan
	of    map[uint64]string // member -> clan
	f     *os.File
	now   func() time.Time
}

// Open opens a store. An empty path keeps everything in memory.
func Open(path string) (*Store, error) {
	s := &Store{clans: map[string]*Clan{}, of: map[uint64]string{}, now: time.Now}
	if path == "" {
		return s, nil
	}
	if err := s.replay(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("clans: %w", err)
	}
	s.f = f
	return s, nil
}

// UseClock replaces the clock. Tests only.
func (s *Store) UseClock(f func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = f
}

// Close flushes the log.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

func (s *Store) replay(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("clans: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var ev event
		if err := json.Unmarshal(raw, &ev); err != nil {
			// A torn last line is a crash mid-write; skip it. Anything
			// else would be read again on the next line.
			continue
		}
		s.apply(ev)
	}
	return sc.Err()
}

func (s *Store) write(ev event) {
	ev.At = s.now()
	s.apply(ev)
	if s.f == nil {
		return
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = s.f.Write(append(raw, '\n'))
}

// apply folds one event into the state. It trusts the event: the methods
// below check everything before they write one.
func (s *Store) apply(ev event) {
	c := s.clans[ev.Clan]
	if c == nil && ev.Type != evCreate {
		return
	}
	switch ev.Type {
	case evCreate:
		c = &Clan{ID: ev.Clan, Name: ev.Name, Tag: ev.Tag, Motto: ev.Motto, Emblem: ev.Emblem, Color: ev.Color,
			Open: ev.Open != nil && *ev.Open, Created: ev.At, Members: map[uint64]*Member{},
			Sectors: map[string]int{}, Invites: map[uint64]time.Time{}}
		s.clans[c.ID] = c
		c.Members[ev.By] = &Member{SteamID: ev.By, Name: ev.Founder, Role: Leader, Joined: ev.At}
		s.of[ev.By] = c.ID
	case evJoin:
		c.Members[ev.Who] = &Member{SteamID: ev.Who, Name: ev.Name, Role: Soldier, Joined: ev.At}
		s.of[ev.Who] = c.ID
		delete(c.Invites, ev.Who)
	case evLeave:
		delete(c.Members, ev.Who)
		delete(s.of, ev.Who)
		if len(c.Members) == 0 {
			s.disband(c)
		}
	case evRole:
		if m := c.Members[ev.Who]; m != nil {
			m.Role = ev.Role
		}
	case evSettings:
		c.Motto = ev.Motto
		if ev.Emblem != "" {
			c.Emblem = ev.Emblem
		}
		if ev.Color != "" {
			c.Color = ev.Color
		}
		if ev.Open != nil {
			c.Open = *ev.Open
		}
	case evOrder:
		if ev.Order != nil {
			c.Order = *ev.Order
		}
	case evPlayed:
		week := Week(ev.At)
		if c.Week != week {
			c.Week, c.Progress, c.Paid = week, 0, false
		}
		c.XP += ev.XP
		if m := c.Members[ev.Who]; m != nil {
			if m.week != week {
				m.week, m.WeekXP = week, 0
			}
			m.XP += ev.XP
			m.WeekXP += ev.XP
		}
		if ev.War {
			c.Battles++
			if ev.Won {
				c.Wins++
				if ev.Sector != "" {
					c.Sectors[ev.Sector]++
				}
			}
		}
		switch ContractFor(week).Kind {
		case "wins":
			if ev.Won {
				c.Progress++
			}
		case "war":
			if ev.War && ev.Won {
				c.Progress++
			}
		case "together":
			if ev.With {
				c.Progress++
			}
		}
	case evInvite:
		c.Invites[ev.Who] = ev.At
	case evUninvite:
		delete(c.Invites, ev.Who)
	case evPaid:
		c.Paid = true
		c.XP += ev.XP
	case evDisband:
		s.disband(c)
	}
}

func (s *Store) disband(c *Clan) {
	for id := range c.Members {
		delete(s.of, id)
	}
	delete(s.clans, c.ID)
}

// --- reading ----------------------------------------------------------------

// Of is the clan a player is in, a copy, or nil.
func (s *Store) Of(id uint64) *Clan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyLocked(s.clans[s.of[id]])
}

// Get is a clan by id, a copy, or nil.
func (s *Store) Get(id string) *Clan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyLocked(s.clans[id])
}

// TagOf is a player's clan tag, or "".
func (s *Store) TagOf(id uint64) (tag, clan string, gold bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clans[s.of[id]]
	if c == nil {
		return "", "", false
	}
	return c.Tag, c.ID, Has(c.Level(), "gold")
}

// InvitesFor is the clans that invited a player.
func (s *Store) InvitesFor(id uint64) []*Clan {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Clan
	for _, c := range s.clans {
		if s.invitedLocked(c, id) {
			out = append(out, s.copyLocked(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].XP > out[j].XP })
	return out
}

// Top is the clans, best first: by clan XP, then war wins, then age.
func (s *Store) Top(n int) []*Clan {
	out := s.Ranked()
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// Ranked is every clan, best first, as Top orders them.
func (s *Store) Ranked() []*Clan {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Clan, 0, len(s.clans))
	for _, c := range s.clans {
		out = append(out, s.copyLocked(c))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].XP != out[j].XP {
			return out[i].XP > out[j].XP
		}
		if out[i].Wins != out[j].Wins {
			return out[i].Wins > out[j].Wins
		}
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Banners is, for each sector, the clan that won the most battles there
// among clans with the banner perk.
func (s *Store) Banners() map[string]*Clan {
	s.mu.Lock()
	defer s.mu.Unlock()
	best := map[string]*Clan{}
	wins := map[string]int{}
	for _, c := range s.clans {
		if !Has(c.Level(), "banner") {
			continue
		}
		sector, n := c.Banner()
		if sector == "" {
			continue
		}
		if n > wins[sector] || (n == wins[sector] && best[sector] != nil && c.ID < best[sector].ID) {
			wins[sector] = n
			best[sector] = s.copyLocked(c)
		}
	}
	return best
}

// Count is how many clans there are.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clans)
}

func (s *Store) invitedLocked(c *Clan, id uint64) bool {
	at, ok := c.Invites[id]
	return ok && s.now().Sub(at) < InviteTTL
}

func (s *Store) copyLocked(c *Clan) *Clan {
	if c == nil {
		return nil
	}
	out := *c
	out.Members = make(map[uint64]*Member, len(c.Members))
	for id, m := range c.Members {
		mm := *m
		if m.week != Week(s.now()) {
			mm.WeekXP = 0
		}
		out.Members[id] = &mm
	}
	out.Sectors = make(map[string]int, len(c.Sectors))
	for k, v := range c.Sectors {
		out.Sectors[k] = v
	}
	out.Invites = make(map[uint64]time.Time, len(c.Invites))
	for k, v := range c.Invites {
		if s.now().Sub(v) < InviteTTL {
			out.Invites[k] = v
		}
	}
	if c.Week != Week(s.now()) {
		out.Week, out.Progress, out.Paid = Week(s.now()), 0, false
	}
	return &out
}

// --- changing ---------------------------------------------------------------

// Settings are what a clan's founder picks and its officers can change.
type Settings struct {
	Name   string
	Tag    string
	Motto  string
	Emblem string
	Color  string
	Open   bool
}

// CleanName checks a clan name: 3 to 24 letters, digits, spaces and a few
// marks.
func CleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	n := utf8.RuneCountInString(name)
	if n < 3 || n > 24 {
		return "", ErrName
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" -_.'!", r) {
			return "", ErrName
		}
	}
	return name, nil
}

// CleanTag checks a tag: 2 to 5 letters or digits, kept upper case.
func CleanTag(tag string) (string, error) {
	tag = strings.ToUpper(strings.TrimSpace(tag))
	n := utf8.RuneCountInString(tag)
	if n < 2 || n > 5 {
		return "", ErrTag
	}
	for _, r := range tag {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "", ErrTag
		}
	}
	return tag, nil
}

func cleanMotto(m string) string {
	m = strings.Join(strings.Fields(m), " ")
	if utf8.RuneCountInString(m) > 80 {
		m = string([]rune(m)[:80])
	}
	return m
}

func pick(v string, from []string, def string) string {
	for _, f := range from {
		if f == v {
			return v
		}
	}
	return def
}

func newID() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	var b [8]byte
	_, _ = rand.Read(b[:])
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out)
}

// Create founds a clan with the player as its leader.
func (s *Store) Create(founder uint64, founderName string, set Settings) (*Clan, error) {
	name, err := CleanName(set.Name)
	if err != nil {
		return nil, err
	}
	tag, err := CleanTag(set.Tag)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.of[founder] != "" {
		return nil, ErrInClan
	}
	for _, c := range s.clans {
		if strings.EqualFold(c.Name, name) || strings.EqualFold(c.Tag, tag) {
			return nil, ErrTaken
		}
	}
	id := newID()
	for s.clans[id] != nil {
		id = newID()
	}
	open := set.Open
	s.write(event{Type: evCreate, Clan: id, By: founder, Name: name, Tag: tag, Motto: cleanMotto(set.Motto),
		Emblem: pick(set.Emblem, Emblems, "soldier"), Color: pick(set.Color, Colors, "red"), Open: &open,
		Founder: founderName})
	return s.copyLocked(s.clans[id]), nil
}

// Join puts a player in a clan: an open one, or one that invited them.
func (s *Store) Join(id uint64, name, clan string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.of[id] != "" {
		return ErrInClan
	}
	c := s.clans[clan]
	if c == nil {
		return ErrNoClan
	}
	if !c.Open && !s.invitedLocked(c, id) {
		return ErrClosed
	}
	if len(c.Members) >= c.Cap() {
		return ErrFull
	}
	s.write(event{Type: evJoin, Clan: clan, Who: id, Name: name})
	return nil
}

// Leave takes a player out of their clan. A leaving leader hands the clan to
// the highest-ranked, longest-serving member; the last one out disbands it.
func (s *Store) Leave(id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clans[s.of[id]]
	if c == nil {
		return ErrNotInClan
	}
	if c.Members[id].Role == Leader && len(c.Members) > 1 {
		var heir *Member
		for _, m := range c.Members {
			if m.SteamID == id {
				continue
			}
			if heir == nil || (m.Role == Officer && heir.Role != Officer) ||
				(m.Role == heir.Role && m.Joined.Before(heir.Joined)) {
				heir = m
			}
		}
		s.write(event{Type: evRole, Clan: c.ID, Who: heir.SteamID, Role: Leader})
	}
	s.write(event{Type: evLeave, Clan: c.ID, Who: id})
	return nil
}

// memberLocked is a player's clan and their place in it.
func (s *Store) memberLocked(id uint64) (*Clan, *Member) {
	c := s.clans[s.of[id]]
	if c == nil {
		return nil, nil
	}
	return c, c.Members[id]
}

// Kick removes a member. Officers can kick members; the leader anyone.
func (s *Store) Kick(by, who uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, me := s.memberLocked(by)
	if c == nil {
		return ErrNotInClan
	}
	if by == who {
		return ErrSelf
	}
	them := c.Members[who]
	if them == nil {
		return ErrNotMember
	}
	if !(me.Role == Leader || (me.Role == Officer && them.Role == Soldier)) {
		return ErrRank
	}
	s.write(event{Type: evLeave, Clan: c.ID, By: by, Who: who})
	return nil
}

// SetRole promotes or demotes a member; only the leader can. Making someone
// leader hands the clan over.
func (s *Store) SetRole(by, who uint64, role Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, me := s.memberLocked(by)
	if c == nil {
		return ErrNotInClan
	}
	if by == who {
		return ErrSelf
	}
	if c.Members[who] == nil {
		return ErrNotMember
	}
	if me.Role != Leader || (role != Leader && role != Officer && role != Soldier) {
		return ErrRank
	}
	if role == Leader {
		s.write(event{Type: evRole, Clan: c.ID, Who: by, Role: Officer})
	}
	s.write(event{Type: evRole, Clan: c.ID, Who: who, Role: role})
	return nil
}

// Invite lets a player into the clan, closed or not. Officers can, and in an
// open clan anyone (see CanInvite).
func (s *Store) Invite(by, who uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, me := s.memberLocked(by)
	if c == nil {
		return ErrNotInClan
	}
	if !c.CanInvite(me.Role) {
		return ErrRank
	}
	if by == who {
		return ErrSelf
	}
	if who == 0 {
		return ErrNotMember
	}
	if s.of[who] != "" {
		return ErrInClan
	}
	if len(c.Members) >= c.Cap() {
		return ErrFull
	}
	s.write(event{Type: evInvite, Clan: c.ID, By: by, Who: who})
	return nil
}

// Uninvite takes back an invite the clan sent. Whoever may invite may.
func (s *Store) Uninvite(by, who uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, me := s.memberLocked(by)
	if c == nil {
		return ErrNotInClan
	}
	if !c.CanInvite(me.Role) {
		return ErrRank
	}
	if _, ok := c.Invites[who]; ok {
		s.write(event{Type: evUninvite, Clan: c.ID, By: by, Who: who})
	}
	return nil
}

// Decline drops an invite.
func (s *Store) Decline(who uint64, clan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.clans[clan]; c != nil {
		if _, ok := c.Invites[who]; ok {
			s.write(event{Type: evUninvite, Clan: clan, Who: who})
		}
	}
}

// Configure changes the motto, emblem, color and whether anyone may join.
func (s *Store) Configure(by uint64, set Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, me := s.memberLocked(by)
	if c == nil {
		return ErrNotInClan
	}
	if me.Role == Soldier {
		return ErrRank
	}
	open := set.Open
	s.write(event{Type: evSettings, Clan: c.ID, By: by, Motto: cleanMotto(set.Motto),
		Emblem: pick(set.Emblem, Emblems, c.Emblem), Color: pick(set.Color, Colors, c.Color), Open: &open})
	return nil
}

// SetOrder sends the clan to a war operation ("" to stand down). It takes the
// order perk and an officer.
func (s *Store) SetOrder(by uint64, front string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, me := s.memberLocked(by)
	if c == nil {
		return ErrNotInClan
	}
	if me.Role == Soldier {
		return ErrRank
	}
	if !Has(c.Level(), "order") {
		return ErrPerk
	}
	s.write(event{Type: evOrder, Clan: c.ID, By: by, Order: &front})
	return nil
}

// Played is one member's part in a finished match.
type Played struct {
	SteamID uint64
	Won     bool
	War     bool   // a war battle
	Sector  string // the war sector it was fought for
	Front   string // the war operation it was part of
}

// Earned is what a match paid one member through their clan.
type Earned struct {
	Clan     string
	ClanXP   int
	BonusPct int  // brothers/veterans: percent of the match XP to add
	Order    bool // fought in the clan's order: OrderBonusXP
	Levelled int  // the clan's new level, if this match raised it
	Contract bool // this match finished the weekly contract
}

// RecordMatch credits clans for a finished match and says what each member
// earned. Members of one clan who played it together earn more.
func (s *Store) RecordMatch(ps []Played) map[uint64]Earned {
	s.mu.Lock()
	defer s.mu.Unlock()
	together := map[string]int{}
	for _, p := range ps {
		if c := s.of[p.SteamID]; c != "" {
			together[c]++
		}
	}
	out := map[uint64]Earned{}
	week := Week(s.now())
	for _, p := range ps {
		c := s.clans[s.of[p.SteamID]]
		if c == nil {
			continue
		}
		before := c.Level()
		with := together[c.ID] >= 2
		xp := XPPerMatch
		if p.Won {
			xp += XPWinBonus
		}
		if with {
			xp += xp * XPTogetherPct / 100
		}
		s.write(event{Type: evPlayed, Clan: c.ID, Who: p.SteamID, XP: xp, Won: p.Won, War: p.War, Sector: p.Sector, With: with})
		e := Earned{Clan: c.ID, ClanXP: xp}
		lvl := c.Level()
		if with && Has(lvl, "veterans") {
			e.BonusPct = VeteransBonus
		} else if with && Has(lvl, "brothers") {
			e.BonusPct = BrothersBonus
		}
		e.Order = p.War && c.Order != "" && c.Order == p.Front && Has(lvl, "order")
		if Has(lvl, "contract") && c.Week == week && !c.Paid && c.Progress >= ContractFor(week).Target {
			s.write(event{Type: evPaid, Clan: c.ID, XP: ContractClanXP})
			e.Contract = true
		}
		if l := c.Level(); l > before {
			e.Levelled = l
		}
		out[p.SteamID] = e
	}
	return out
}

// ContractPaidTo is who a finished weekly contract pays: everyone who earned
// the clan XP that week.
func (s *Store) ContractPaidTo(clan string) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clans[clan]
	if c == nil {
		return nil
	}
	var out []uint64
	week := Week(s.now())
	for id, m := range c.Members {
		if m.week == week && m.WeekXP > 0 {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Rename records a member's current name, so the roster shows it.
func (s *Store) Rename(id uint64, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, m := s.memberLocked(id); c != nil && name != "" && m.Name != name {
		m.Name = name // memory only: the next join or create writes it
	}
}
