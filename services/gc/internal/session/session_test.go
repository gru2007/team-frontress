package session

import (
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/wire"
)

func TestAcceptDropsReplays(t *testing.T) {
	m := NewManager(time.Minute, nil)
	s := m.Open(Client, 1, nil)
	now := time.Now()
	got, _ := s.Accept([]wire.Frame{{Seq: 1}, {Seq: 2}}, now)
	if len(got) != 2 {
		t.Fatal(got)
	}
	got, _ = s.Accept([]wire.Frame{{Seq: 2}, {Seq: 3}}, now)
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatal(got)
	}
}

func TestPollKeepsUntilAcked(t *testing.T) {
	m := NewManager(time.Minute, nil)
	s := m.Open(Client, 1, nil)
	s.Push(1, []byte("a"))
	s.Push(2, []byte("b"))
	fs, _ := s.Poll(0, time.Millisecond, time.Now)
	if len(fs) != 2 {
		t.Fatal(fs)
	}
	// The response was lost: the same frames come again.
	fs, _ = s.Poll(0, time.Millisecond, time.Now)
	if len(fs) != 2 {
		t.Fatal(fs)
	}
	fs, _ = s.Poll(2, time.Millisecond, time.Now)
	if len(fs) != 0 {
		t.Fatal(fs)
	}
}

func TestSecondLoginReplacesFirst(t *testing.T) {
	var ended []*Session
	m := NewManager(time.Minute, func(s *Session) { ended = append(ended, s) })
	a := m.Open(Client, 1, nil)
	b := m.Open(Client, 1, nil)
	if !a.Closed() || b.Closed() || m.BySteamID(1) != b || len(ended) != 1 || ended[0] != a {
		t.Fatal("first session was not replaced")
	}
}

func TestSweepExpiresSilentPeers(t *testing.T) {
	now := time.Now()
	m := NewManager(time.Minute, nil)
	m.UseClock(func() time.Time { return now })
	s := m.Open(Client, 1, nil)
	now = now.Add(2 * time.Minute)
	m.Sweep()
	if !s.Closed() || m.BySteamID(1) != nil {
		t.Fatal("silent session survived")
	}
}
