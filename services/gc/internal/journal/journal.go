// Package journal keeps what the GC logged about players, servers and
// matches, so moderators can read a player's or a server's history from the
// admin panel.
//
// It is fed by the GC's own structured log: a slog handler in front of the
// usual one copies every record that names a player (steamid, steamids,
// reporter, target), a server or a match. Nothing in the GC writes to the
// journal directly, so it holds exactly what the operator's log says, and
// also the debug-level records (chat lines, queue changes) that the log
// leaves out.
//
// Entries are kept in a day file each (<dir>/YYYY-MM-DD.jsonl) for Days days,
// and the newest ones in memory. Writing never blocks the GC: when the disk
// falls behind, entries are dropped and counted.
package journal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Entry is one record.
type Entry struct {
	Seq     uint64            `json:"seq"`
	At      time.Time         `json:"at"`
	Level   string            `json:"level"`
	Msg     string            `json:"msg"`
	Players []string          `json:"players,omitempty"`
	Server  string            `json:"server,omitempty"`
	Match   string            `json:"match,omitempty"`
	Lobby   string            `json:"lobby,omitempty"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// Query selects entries. Empty fields match everything.
type Query struct {
	Player string
	Server string
	Match  string // a match or a lobby ID
	Text   string // in the message or any attribute
	// MinLevel: "debug" (default), "info", "warn", "error".
	MinLevel string
	// Before is a Seq: only older entries (paging). 0: the newest.
	Before uint64
	Limit  int
}

// Page is a query's answer, newest first.
type Page struct {
	Entries []Entry `json:"entries"`
	// Next is the Before of the next page, 0 when there is none.
	Next    uint64 `json:"next"`
	Dropped uint64 `json:"dropped"`
}

const memory = 50000

// Store holds the journal.
type Store struct {
	dir  string
	days int

	mu      sync.Mutex
	ring    []Entry // oldest first
	seq     uint64
	dropped uint64
	queue   chan Entry
}

// Open opens a journal. An empty dir keeps entries in memory only.
func Open(dir string, days int) (*Store, error) {
	if days <= 0 {
		days = 30
	}
	s := &Store{dir: dir, days: days, queue: make(chan Entry, 4096)}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	files, err := s.files()
	if err != nil {
		return nil, err
	}
	// The newest files fill memory and tell where numbering continues.
	var recent []Entry
	for i := len(files) - 1; i >= 0 && len(recent) < memory; i-- {
		entries, err := readFile(files[i])
		if err != nil {
			return nil, err
		}
		recent = append(entries, recent...)
	}
	if len(recent) > memory {
		recent = recent[len(recent)-memory:]
	}
	s.ring = recent
	if n := len(recent); n > 0 {
		s.seq = recent[n-1].Seq
	}
	return s, nil
}

func (s *Store) files() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(s.dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func readFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Add records an entry without blocking.
func (s *Store) Add(e Entry) {
	s.mu.Lock()
	s.seq++
	e.Seq = s.seq
	s.ring = append(s.ring, e)
	if len(s.ring) > memory+memory/10 {
		s.ring = append([]Entry(nil), s.ring[len(s.ring)-memory:]...)
	}
	s.mu.Unlock()
	if s.dir == "" {
		return
	}
	select {
	case s.queue <- e:
	default:
		s.mu.Lock()
		s.dropped++
		s.mu.Unlock()
	}
}

// Run writes entries to disk until ctx ends, and removes day files older
// than Days.
func (s *Store) Run(ctx context.Context) {
	if s.dir == "" {
		<-ctx.Done()
		return
	}
	var (
		day string
		f   *os.File
		w   *bufio.Writer
	)
	closeFile := func() {
		if f != nil {
			_ = w.Flush()
			_ = f.Close()
			f = nil
		}
	}
	defer closeFile()
	flush := time.NewTicker(time.Second)
	defer flush.Stop()
	s.prune()
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case e := <-s.queue:
					day, f, w = s.write(e, day, f, w)
				default:
					return
				}
			}
		case e := <-s.queue:
			day, f, w = s.write(e, day, f, w)
		case <-flush.C:
			if w != nil {
				_ = w.Flush()
			}
		}
	}
}

func (s *Store) write(e Entry, day string, f *os.File, w *bufio.Writer) (string, *os.File, *bufio.Writer) {
	d := e.At.UTC().Format("2006-01-02")
	if d != day || f == nil {
		if f != nil {
			_ = w.Flush()
			_ = f.Close()
		}
		var err error
		f, err = os.OpenFile(filepath.Join(s.dir, d+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			s.mu.Lock()
			s.dropped++
			s.mu.Unlock()
			return "", nil, nil
		}
		w = bufio.NewWriter(f)
		day = d
		s.prune()
	}
	line, err := json.Marshal(e)
	if err == nil {
		_, _ = w.Write(append(line, '\n'))
	}
	return day, f, w
}

func (s *Store) prune() {
	files, err := s.files()
	if err != nil {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -s.days).Format("2006-01-02")
	for _, path := range files {
		if strings.TrimSuffix(filepath.Base(path), ".jsonl") < cutoff {
			_ = os.Remove(path)
		}
	}
}

var levels = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

func (q Query) match(e Entry) bool {
	if q.Before != 0 && e.Seq >= q.Before {
		return false
	}
	if levels[e.Level] < levels[q.MinLevel] {
		return false
	}
	if q.Player != "" {
		found := false
		for _, p := range e.Players {
			if p == q.Player {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if q.Server != "" && e.Server != q.Server {
		return false
	}
	if q.Match != "" && e.Match != q.Match && e.Lobby != q.Match {
		return false
	}
	if q.Text != "" {
		text := strings.ToLower(q.Text)
		hit := strings.Contains(strings.ToLower(e.Msg), text)
		for _, v := range e.Attrs {
			if hit {
				break
			}
			hit = strings.Contains(strings.ToLower(v), text)
		}
		if !hit {
			return false
		}
	}
	return true
}

// Query returns matching entries, newest first: from memory, then from the
// day files for what memory no longer holds.
func (s *Store) Query(q Query) (Page, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	s.mu.Lock()
	ring := s.ring
	page := Page{Entries: []Entry{}, Dropped: s.dropped}
	s.mu.Unlock()

	oldest := uint64(0)
	if len(ring) > 0 {
		oldest = ring[0].Seq
	}
	for i := len(ring) - 1; i >= 0 && len(page.Entries) < q.Limit; i-- {
		if q.match(ring[i]) {
			page.Entries = append(page.Entries, ring[i])
		}
	}
	if len(page.Entries) < q.Limit && s.dir != "" && oldest > 1 {
		files, err := s.files()
		if err != nil {
			return page, err
		}
		older := q
		if older.Before == 0 || older.Before > oldest {
			older.Before = oldest
		}
		for i := len(files) - 1; i >= 0 && len(page.Entries) < q.Limit; i-- {
			entries, err := readFile(files[i])
			if err != nil {
				return page, err
			}
			for j := len(entries) - 1; j >= 0 && len(page.Entries) < q.Limit; j-- {
				if older.match(entries[j]) {
					page.Entries = append(page.Entries, entries[j])
				}
			}
		}
	}
	if n := len(page.Entries); n == q.Limit && page.Entries[n-1].Seq > 1 {
		page.Next = page.Entries[n-1].Seq
	}
	return page, nil
}

// Handler is a slog handler that passes records on to next and copies the
// ones about a player, a server or a match into the journal.
func (s *Store) Handler(next slog.Handler) slog.Handler {
	return &handler{store: s, next: next}
}

type handler struct {
	store *Store
	next  slog.Handler
	attrs []slog.Attr
}

func (h *handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{store: h.store, next: h.next.WithAttrs(attrs), attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}

// WithGroup passes the group on; the journal flattens groups away.
func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{store: h.store, next: h.next.WithGroup(name), attrs: h.attrs}
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.next.Enabled(ctx, r.Level) {
		err = h.next.Handle(ctx, r)
	}
	e := Entry{At: r.Time, Level: levelName(r.Level), Msg: r.Message, Attrs: map[string]string{}}
	add := func(a slog.Attr) bool {
		h.collect(&e, a)
		return true
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(add)
	if len(e.Players) == 0 && e.Server == "" && e.Match == "" && e.Lobby == "" && r.Level < slog.LevelWarn {
		return err
	}
	if len(e.Attrs) == 0 {
		e.Attrs = nil
	}
	h.store.Add(e)
	return err
}

func (h *handler) collect(e *Entry, a slog.Attr) {
	v := a.Value.Resolve()
	switch a.Key {
	case "steamid", "reporter", "target":
		if id := idString(v); id != "" {
			e.Players = appendUnique(e.Players, id)
		}
	case "steamids":
		if ids, ok := v.Any().([]uint64); ok {
			for _, id := range ids {
				e.Players = appendUnique(e.Players, strconv.FormatUint(id, 10))
			}
		}
	case "server":
		e.Server = idString(v)
	case "match":
		e.Match = idString(v)
	case "lobby":
		e.Lobby = idString(v)
	}
	if a.Key != "steamids" {
		s := v.String()
		if len(s) > 500 {
			s = s[:500]
		}
		e.Attrs[a.Key] = s
	}
}

func idString(v slog.Value) string {
	switch v.Kind() {
	case slog.KindUint64:
		if v.Uint64() == 0 {
			return ""
		}
		return strconv.FormatUint(v.Uint64(), 10)
	case slog.KindInt64:
		if v.Int64() <= 0 {
			return ""
		}
		return strconv.FormatInt(v.Int64(), 10)
	case slog.KindString:
		return v.String()
	}
	if s := fmt.Sprint(v.Any()); s != "0" {
		return s
	}
	return ""
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	}
	return "debug"
}
