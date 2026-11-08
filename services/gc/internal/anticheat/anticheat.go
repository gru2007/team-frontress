// Package anticheat keeps what Easy Anti-Cheat said about each player and the
// matchmaking sanctions that follow from it.
//
// Protected game servers report EAC's verdicts (CMsgGC_Frontress_AntiCheatEvent).
// EAC enforces its own bans on every protected server already; the GC mirrors
// them so a sanctioned player cannot even queue, and keeps the trail admins
// review appeals against. Admins can also ban and lift bans by hand.
//
// One JSON file per player, written whole on every change, like reports.
// The store belongs to the GC loop; nothing else may call it concurrently.
package anticheat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Event kinds, as servers report them.
const (
	EventVerified         = "verified"
	EventKicked           = "kicked"
	EventNotProtected     = "not_protected"
	EventIdentityMismatch = "identity_mismatch"
	EventViolation        = "violation"
	EventTempBanned       = "temp_banned"
	EventPermBanned       = "perm_banned"
	EventManualBan        = "manual_ban"
	EventManualClear      = "manual_clear"
	EventSteamBan         = "steam_game_ban"
	EventSteamBanFailed   = "steam_game_ban_failed"
	EventSteamUnban       = "steam_game_unban"
)

// keepEvents bounds a record: enough history to judge an appeal.
const keepEvents = 100

// Event is one verdict about a player.
type Event struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Reason   uint32    `json:"reason,omitempty"` // EOS_EAntiCheatCommonClientActionReason
	Details  string    `json:"details,omitempty"`
	Server   string    `json:"server,omitempty"` // which server key reported it
	MatchID  string    `json:"match_id,omitempty"`
	Enforced bool      `json:"enforced"`
}

// Sanction keeps a player out of matchmaking.
type Sanction struct {
	Kind   string    `json:"kind"` // temporary, permanent, manual
	Since  time.Time `json:"since"`
	Until  time.Time `json:"until,omitempty"` // zero: until lifted
	Reason string    `json:"reason"`
	By     string    `json:"by"`
}

// Active reports whether the sanction still applies at now.
func (s *Sanction) Active(now time.Time) bool {
	return s != nil && (s.Until.IsZero() || now.Before(s.Until))
}

// Describe is what a player is told.
func (s *Sanction) Describe() string {
	if s.Until.IsZero() {
		return "banned by Easy Anti-Cheat"
	}
	return "banned by Easy Anti-Cheat until " + s.Until.UTC().Format("2006-01-02 15:04 UTC")
}

// SteamBan is a Steam game ban the GC issued.
type SteamBan struct {
	At        time.Time `json:"at"`
	AppIDs    []uint32  `json:"app_ids"`
	ReportIDs []string  `json:"report_ids"`
	Reason    string    `json:"reason"`
	By        string    `json:"by"`
}

// Record is everything known about one player.
type Record struct {
	SteamID       string    `json:"steam_id"`
	ProductUserID string    `json:"product_user_id,omitempty"`
	VerifiedAt    time.Time `json:"verified_at,omitempty"`
	Sanction      *Sanction `json:"sanction,omitempty"`
	SteamBan      *SteamBan `json:"steam_ban,omitempty"`
	Events        []Event   `json:"events"`
}

// Store holds the records.
type Store struct {
	dir     string
	records map[string]*Record
}

// Open loads every record in dir. An empty dir keeps records in memory.
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, records: map[string]*Record{}}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var r Record
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("anti-cheat record %s: %w", entry.Name(), err)
		}
		s.records[r.SteamID] = &r
	}
	return s, nil
}

func key(steamID uint64) string { return strconv.FormatUint(steamID, 10) }

// Get returns a copy of a player's record.
func (s *Store) Get(steamID uint64) (Record, bool) {
	r, ok := s.records[key(steamID)]
	if !ok {
		return Record{SteamID: key(steamID)}, false
	}
	return clone(r), true
}

// Sanctioned returns the player's sanction if one applies at now.
func (s *Store) Sanctioned(steamID uint64, now time.Time) (Sanction, bool) {
	r, ok := s.records[key(steamID)]
	if !ok || !r.Sanction.Active(now) {
		return Sanction{}, false
	}
	return *r.Sanction, true
}

// Add records an event; productUserID is remembered when given. A
// non-nil sanction replaces the current one unless the current one is
// longer -- a temporary ban never shortens a permanent one.
func (s *Store) Add(steamID uint64, productUserID string, ev Event, sanction *Sanction) error {
	if steamID == 0 || ev.Kind == "" {
		return errors.New("anti-cheat event needs a player and a kind")
	}
	r := s.records[key(steamID)]
	if r == nil {
		r = &Record{SteamID: key(steamID)}
	}
	next := clone(r)
	if productUserID != "" {
		next.ProductUserID = productUserID
	}
	if ev.Kind == EventVerified {
		next.VerifiedAt = ev.At
	}
	next.Events = append(next.Events, ev)
	if len(next.Events) > keepEvents {
		next.Events = next.Events[len(next.Events)-keepEvents:]
	}
	if sanction != nil && outlasts(sanction, next.Sanction, ev.At) {
		sc := *sanction
		next.Sanction = &sc
	}
	return s.save(&next)
}

// SetSteamBan records a Steam game ban the GC issued, or (nil) lifted, with
// the event that says so.
func (s *Store) SetSteamBan(steamID uint64, ban *SteamBan, ev Event) error {
	r := s.records[key(steamID)]
	if r == nil {
		r = &Record{SteamID: key(steamID)}
	}
	next := clone(r)
	if ban != nil {
		b := *ban
		b.AppIDs = append([]uint32(nil), ban.AppIDs...)
		b.ReportIDs = append([]string(nil), ban.ReportIDs...)
		next.SteamBan = &b
	} else if ev.Kind == EventSteamUnban {
		next.SteamBan = nil
	}
	next.Events = append(next.Events, ev)
	if len(next.Events) > keepEvents {
		next.Events = next.Events[len(next.Events)-keepEvents:]
	}
	return s.save(&next)
}

// Clear lifts a player's sanction.
func (s *Store) Clear(steamID uint64, by, reason string, now time.Time) error {
	r := s.records[key(steamID)]
	if r == nil {
		r = &Record{SteamID: key(steamID)}
	}
	next := clone(r)
	next.Sanction = nil
	next.Events = append(next.Events, Event{At: now, Kind: EventManualClear, Details: reason, Server: by, Enforced: true})
	return s.save(&next)
}

// ActiveSanctions lists every player with a sanction active at now, newest first.
func (s *Store) ActiveSanctions(now time.Time) []Record {
	var out []Record
	for _, r := range s.records {
		if r.Sanction.Active(now) {
			out = append(out, clone(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sanction.Since.After(out[j].Sanction.Since) })
	return out
}

func outlasts(next, cur *Sanction, now time.Time) bool {
	if !cur.Active(now) {
		return true
	}
	if cur.Until.IsZero() {
		return next.Until.IsZero()
	}
	return next.Until.IsZero() || next.Until.After(cur.Until)
}

func clone(r *Record) Record {
	c := *r
	c.Events = append([]Event(nil), r.Events...)
	if r.Sanction != nil {
		sc := *r.Sanction
		c.Sanction = &sc
	}
	if r.SteamBan != nil {
		sb := *r.SteamBan
		sb.AppIDs = append([]uint32(nil), r.SteamBan.AppIDs...)
		sb.ReportIDs = append([]string(nil), r.SteamBan.ReportIDs...)
		c.SteamBan = &sb
	}
	return c
}

// save writes the record before it replaces the one in memory, so what the
// GC acts on is always what a restart would load.
func (s *Store) save(r *Record) error {
	if s.dir != "" {
		raw, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		path := filepath.Join(s.dir, r.SteamID+".json")
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	s.records[r.SteamID] = r
	return nil
}
