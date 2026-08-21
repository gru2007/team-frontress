// Package bans is the coordinator's record of who is not allowed to play.
//
// A ban is keyed by SteamID and nothing else. That is only worth anything when
// the coordinator actually proves SteamIDs — auth.mode=webapi — because under
// auth.mode=dev a client states its own ID and a banned player can simply
// state a different one. The ban list still works there, it just stops honest
// mistakes rather than determined people; see docs/STEAM-SETUP.md.
//
// The file is the authority, in the same shape as the war log: an append-only
// JSON-lines record of every ban and lift, replayed on open. A ban that
// evaporated when the coordinator restarted would not be a ban, and an
// operator needs to be able to read back why somebody was thrown out months
// later.
package bans

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Source says what put a ban on the list. It is the difference between "the
// coordinator watched them walk out of a battle" and "an operator decided",
// which is the first thing anyone asks when a ban is disputed.
type Source string

const (
	// SourceManual is an operator acting through the admin endpoint.
	SourceManual Source = "manual"
	// SourceAbandon is a player who left a live battle.
	SourceAbandon Source = "abandon"
	// SourceViolation is a proven policy violation. Nothing issues these
	// automatically yet — see the note in mm.Leave.
	SourceViolation Source = "violation"
)

// Ban is one account kept out of the war.
type Ban struct {
	SteamID uint64 `json:"steam_id,string"`
	Reason  string `json:"reason"`
	Source  Source `json:"source"`
	// IssuedBy names the operator for a manual ban, and is empty for one the
	// coordinator issued itself.
	IssuedBy string    `json:"issued_by,omitempty"`
	IssuedAt time.Time `json:"issued_at"`
	// ExpiresAt zero means permanent.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Permanent reports whether this ban has no end.
func (b Ban) Permanent() bool { return b.ExpiresAt.IsZero() }

// Active reports whether the ban is still in force at now.
func (b Ban) Active(now time.Time) bool {
	return b.Permanent() || now.Before(b.ExpiresAt)
}

// Remaining is how much longer the ban runs, or zero for a permanent one.
func (b Ban) Remaining(now time.Time) time.Duration {
	if b.Permanent() {
		return 0
	}
	if d := b.ExpiresAt.Sub(now); d > 0 {
		return d
	}
	return 0
}

// Describe is the sentence a banned player is shown.
func (b Ban) Describe(now time.Time) string {
	reason := b.Reason
	if reason == "" {
		reason = "no reason recorded"
	}
	if b.Permanent() {
		return "you are banned from the war: " + reason
	}
	return fmt.Sprintf("you are banned from the war for another %s: %s",
		roundDuration(b.Remaining(now)), reason)
}

// roundDuration trims a remaining time to something worth reading. Seconds
// matter on a ten minute ban and are noise on a three day one.
func roundDuration(d time.Duration) time.Duration {
	switch {
	case d >= 24*time.Hour:
		return d.Round(time.Hour)
	case d >= time.Hour:
		return d.Round(time.Minute)
	default:
		return d.Round(time.Second)
	}
}

// record is one line of the ban log.
type record struct {
	Seq uint64    `json:"seq"`
	Op  string    `json:"op"` // "ban" | "lift"
	At  time.Time `json:"at"`

	Ban *Ban `json:"ban,omitempty"`

	// Lift fields.
	SteamID uint64 `json:"steam_id,string,omitempty"`
	By      string `json:"by,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// List is the ban list: every ban in force, backed by its log.
type List struct {
	mu     sync.Mutex
	path   string
	f      *os.File
	w      *bufio.Writer
	seq    uint64
	active map[uint64]Ban
	now    func() time.Time
}

// Open opens (or creates) a ban list at path and replays what is already
// there. An empty path is a list that lives only for this process — right for
// tests, and for an operator who deliberately wants bans to end at a restart.
func Open(path string) (*List, error) {
	l := &List{path: path, active: make(map[uint64]Ban), now: time.Now}
	if path == "" {
		return l, nil
	}
	if err := l.read(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("bans: open %s: %w", path, err)
	}
	l.f = f
	l.w = bufio.NewWriter(f)
	return l, nil
}

func (l *List) read() error {
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("bans: read %s: %w", l.path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(raw, &rec); err != nil {
			// Same rule as the war log: a torn line means the file was edited
			// or a write was cut short, and guessing past it would quietly
			// unban somebody.
			return fmt.Errorf("bans: %s line %d is malformed: %w", l.path, line, err)
		}
		l.apply(rec)
		if rec.Seq > l.seq {
			l.seq = rec.Seq
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("bans: read %s: %w", l.path, err)
	}
	return nil
}

// apply folds one record into the in-memory state. Expired bans are kept as
// they replay and dropped by the lazy sweep in Check, so the log stays the
// only place history lives.
func (l *List) apply(rec record) {
	switch rec.Op {
	case "ban":
		if rec.Ban != nil && rec.Ban.SteamID != 0 {
			l.active[rec.Ban.SteamID] = *rec.Ban
		}
	case "lift":
		delete(l.active, rec.SteamID)
	}
}

// Ban puts an account on the list.
//
// An existing ban is never shortened. An abandon ban landing on somebody an
// operator has already banned for good must not quietly turn that into ten
// minutes, and the same is true of two automatic bans in a row — so the longer
// of the two wins, and a permanent ban beats anything.
func (l *List) Ban(b Ban) (Ban, error) {
	if b.SteamID == 0 {
		return Ban{}, fmt.Errorf("bans: a ban needs a SteamID")
	}
	if b.Source == "" {
		b.Source = SourceManual
	}
	b.Reason = strings.TrimSpace(b.Reason)
	if len(b.Reason) > 512 {
		b.Reason = b.Reason[:512]
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if b.IssuedAt.IsZero() {
		b.IssuedAt = now
	}
	if old, ok := l.active[b.SteamID]; ok && old.Active(now) && longer(old, b) {
		return old, nil
	}
	return b, l.appendLocked(record{Op: "ban", At: now, Ban: &b})
}

// Issue is the matchmaker's way in: a ban the coordinator decided on itself,
// expressed as a duration rather than a deadline. A non-positive duration is
// not a permanent ban here — it means "this rule is switched off" — so it does
// nothing, which is what an unset abandon_ban_duration should do.
func (l *List) Issue(steamID uint64, source, reason string, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	_, err := l.Ban(Ban{
		SteamID:   steamID,
		Reason:    reason,
		Source:    Source(source),
		ExpiresAt: l.now().Add(d),
	})
	return err
}

// longer reports whether a outlasts b.
func longer(a, b Ban) bool {
	if a.Permanent() {
		return true
	}
	if b.Permanent() {
		return false
	}
	return a.ExpiresAt.After(b.ExpiresAt)
}

// Lift takes an account off the list. It reports whether there was anything to
// lift, so an operator hears "that account was not banned" rather than a
// silent success.
func (l *List) Lift(steamID uint64, by, reason string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	old, ok := l.active[steamID]
	if !ok || !old.Active(now) {
		delete(l.active, steamID)
		return false, nil
	}
	return true, l.appendLocked(record{
		Op: "lift", At: now, SteamID: steamID, By: by, Reason: strings.TrimSpace(reason),
	})
}

// Check reports the ban in force on an account, if any.
func (l *List) Check(steamID uint64) (Ban, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.active[steamID]
	if !ok {
		return Ban{}, false
	}
	if !b.Active(l.now()) {
		// Expired bans are swept as they are noticed rather than on a timer:
		// the list is small, and a ban nobody looked at costs nothing.
		delete(l.active, steamID)
		return Ban{}, false
	}
	return b, true
}

// Banned is Check without the record, for the callers that only need a yes.
func (l *List) Banned(steamID uint64) bool {
	_, ok := l.Check(steamID)
	return ok
}

// Active lists every ban in force, longest-standing first.
func (l *List) Active() []Ban {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	out := make([]Ban, 0, len(l.active))
	for id, b := range l.active {
		if !b.Active(now) {
			delete(l.active, id)
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IssuedAt.Equal(out[j].IssuedAt) {
			return out[i].SteamID < out[j].SteamID
		}
		return out[i].IssuedAt.Before(out[j].IssuedAt)
	})
	return out
}

// appendLocked writes one record and folds it in. The write is flushed and
// synced before it returns: a ban that is only in a buffer is a ban that a
// crash undoes.
func (l *List) appendLocked(rec record) error {
	l.seq++
	rec.Seq = l.seq
	if l.w != nil {
		line, err := json.Marshal(rec)
		if err != nil {
			l.seq--
			return fmt.Errorf("bans: encode record: %w", err)
		}
		if _, err := l.w.Write(append(line, '\n')); err != nil {
			l.seq--
			return fmt.Errorf("bans: write %s: %w", l.path, err)
		}
		if err := l.w.Flush(); err != nil {
			l.seq--
			return fmt.Errorf("bans: flush %s: %w", l.path, err)
		}
		if err := l.f.Sync(); err != nil {
			l.seq--
			return fmt.Errorf("bans: sync %s: %w", l.path, err)
		}
	}
	l.apply(rec)
	return nil
}

// Close flushes and closes the log.
func (l *List) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w != nil {
		if err := l.w.Flush(); err != nil {
			return err
		}
		l.w = nil
	}
	if l.f != nil {
		err := l.f.Close()
		l.f = nil
		return err
	}
	return nil
}
