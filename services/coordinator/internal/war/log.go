package war

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// EventLog is the war's append-only history, stored as JSON lines.
//
// One line per event, flushed and synced as it is appended. The file is the
// authority: the engine's in-memory state is just the result of replaying it,
// so a coordinator that dies mid-campaign comes back holding the same war.
type EventLog struct {
	mu     sync.Mutex
	path   string
	f      *os.File
	w      *bufio.Writer
	events []Event
	seq    uint64
}

// OpenLog opens (or creates) a log and reads everything already in it. A
// missing file is a fresh world, which is the normal first run.
func OpenLog(path string) (*EventLog, error) {
	l := &EventLog{path: path}
	if err := l.read(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("war: open event log %s: %w", path, err)
	}
	l.f = f
	l.w = bufio.NewWriter(f)
	return l, nil
}

func (l *EventLog) read() error {
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("war: read event log %s: %w", l.path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			// A torn last line is a crash during append and costs one event.
			// A torn line in the middle means the file was edited or corrupted,
			// and replaying past it would silently invent a different war.
			return fmt.Errorf("war: event log %s line %d is malformed: %w", l.path, line, err)
		}
		if ev.Seq <= l.seq && l.seq != 0 {
			return fmt.Errorf("war: event log %s line %d is out of order (seq %d after %d)",
				l.path, line, ev.Seq, l.seq)
		}
		l.seq = ev.Seq
		l.events = append(l.events, ev)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("war: read event log %s: %w", l.path, err)
	}
	return nil
}

// Events returns every event, oldest first. The slice is a copy.
func (l *EventLog) Events() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.events...)
}

// Since returns up to limit events after seq, oldest first. This is the world
// recap: "while you were away" is a query against the log, not a second store.
func (l *EventLog) Since(seq uint64, limit int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	out := make([]Event, 0, limit)
	for _, ev := range l.events {
		if ev.Seq <= seq {
			continue
		}
		out = append(out, ev)
		if len(out) == limit {
			break
		}
	}
	return out
}

// Tail returns the last n events, oldest first.
func (l *EventLog) Tail(n int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.events) {
		n = len(l.events)
	}
	return append([]Event(nil), l.events[len(l.events)-n:]...)
}

// Seq is the sequence number of the newest event.
func (l *EventLog) Seq() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// append writes one event, assigning its sequence number. The event is durable
// before this returns: the war must never advance further than its log.
func (l *EventLog) append(ev *Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	ev.Seq = l.seq
	raw, err := json.Marshal(ev)
	if err != nil {
		l.seq--
		return fmt.Errorf("war: encode event: %w", err)
	}
	if l.w != nil {
		if _, err := l.w.Write(append(raw, '\n')); err != nil {
			l.seq--
			return fmt.Errorf("war: append event: %w", err)
		}
		if err := l.w.Flush(); err != nil {
			l.seq--
			return fmt.Errorf("war: flush event log: %w", err)
		}
		if err := l.f.Sync(); err != nil {
			return fmt.Errorf("war: sync event log: %w", err)
		}
	}
	l.events = append(l.events, *ev)
	return nil
}

// Close flushes and closes the log file.
func (l *EventLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	if err := l.w.Flush(); err != nil {
		return err
	}
	err := l.f.Close()
	l.f, l.w = nil, nil
	return err
}

// MemoryLog is an event log with no file behind it, for tests and dry runs.
func MemoryLog() *EventLog { return &EventLog{path: ""} }
