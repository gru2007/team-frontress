package journal

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestJournalKeepsWhatTheLogSaysAboutPlayers(t *testing.T) {
	dir := t.TempDir()
	j, err := Open(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.Run(ctx); close(done) }()

	log := slog.New(j.Handler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})))
	log.Info("client connected", "steamid", uint64(76561197960265729), "party", uint64(5))
	log.Debug("chat", "steamid", uint64(76561197960265729), "text", "hello")
	log.Info("party queued", "party", uint64(5), "steamids", []uint64{76561197960265729, 76561197960265730})
	log.Info("server connected", "server", uint64(900), "name", "eu-01")
	log.Info("match ended", "lobby", uint64(3), "match", uint64(44), "why", "done")
	log.Info("nothing about anyone")

	page, err := j.Query(Query{Player: "76561197960265730"})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Msg != "party queued" {
		t.Fatalf("by player: %+v %v", page, err)
	}
	if page, _ := j.Query(Query{Player: "76561197960265729", MinLevel: "info"}); len(page.Entries) != 2 {
		t.Fatalf("debug lines must be filterable: %+v", page)
	}
	if page, _ := j.Query(Query{Server: "900"}); len(page.Entries) != 1 {
		t.Fatalf("by server: %+v", page)
	}
	if page, _ := j.Query(Query{Match: "3"}); len(page.Entries) != 1 {
		t.Fatalf("by lobby: %+v", page)
	}
	if page, _ := j.Query(Query{Text: "HELLO"}); len(page.Entries) != 1 {
		t.Fatalf("by text: %+v", page)
	}
	all, _ := j.Query(Query{Limit: 2})
	if len(all.Entries) != 2 || all.Next == 0 || all.Entries[0].Msg != "match ended" {
		t.Fatalf("newest first, paged: %+v", all)
	}
	rest, _ := j.Query(Query{Before: all.Next})
	if len(rest.Entries) != 3 {
		t.Fatalf("second page: %+v", rest)
	}

	cancel()
	<-done
	time.Sleep(10 * time.Millisecond)
	again, err := Open(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	if page, _ := again.Query(Query{}); len(page.Entries) != 5 {
		t.Fatalf("after a restart: %+v", page)
	}
	again.Add(Entry{At: time.Now(), Level: "info", Msg: "next", Players: []string{"1"}})
	if page, _ := again.Query(Query{Limit: 1}); page.Entries[0].Seq != 6 {
		t.Fatalf("numbering must continue: %+v", page)
	}
}
