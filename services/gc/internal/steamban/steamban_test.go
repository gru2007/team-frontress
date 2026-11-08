package steamban

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReportBanAndRemoveSendInputJSON(t *testing.T) {
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("key") != "publisher" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var in map[string]any
		if err := json.Unmarshal([]byte(r.PostForm.Get("input_json")), &in); err != nil {
			t.Fatalf("input_json: %v", err)
		}
		in["_path"] = r.URL.Path
		calls = append(calls, in)
		if r.URL.Path == "/ICheatReportingService/ReportPlayerCheating/v1/" {
			_, _ = w.Write([]byte(`{"response":{"reportid":"1234567890123"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{}}`))
	}))
	defer srv.Close()

	c := &Client{Key: "publisher", Base: srv.URL}
	ctx := context.Background()
	id, err := c.ReportPlayerCheating(ctx, Report{SteamID: 76561197960265729, AppID: 5147380, Detection: true})
	if err != nil || id != 1234567890123 {
		t.Fatalf("report: %d %v", id, err)
	}
	if err := c.RequestPlayerGameBan(ctx, Ban{SteamID: 76561197960265729, AppID: 5147380, ReportID: id,
		Description: "Easy Anti-Cheat", Delay: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.RemovePlayerGameBan(ctx, 76561197960265729, 5147380); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("want 3 calls, got %d", len(calls))
	}
	if calls[0]["steamid"] != "76561197960265729" || calls[0]["detection"] != true {
		t.Fatalf("report input: %v", calls[0])
	}
	ban := calls[1]
	if ban["reportid"] != "1234567890123" || ban["duration"] != float64(0) || ban["delayban"] != true || ban["cheatdescription"] != "Easy Anti-Cheat" {
		t.Fatalf("ban input: %v", ban)
	}
	if calls[2]["_path"] != "/ICheatReportingService/RemovePlayerGameBan/v1/" {
		t.Fatalf("remove path: %v", calls[2])
	}

	if err := c.RequestPlayerGameBan(ctx, Ban{SteamID: 1, AppID: 1, Duration: time.Hour}); err == nil {
		t.Fatal("a ban without a reportid was sent")
	}
	if _, err := (&Client{Key: "wrong", Base: srv.URL}).ReportPlayerCheating(ctx, Report{SteamID: 1, AppID: 1}); err == nil {
		t.Fatal("a refused call did not fail")
	}
	if _, err := (&Client{Base: srv.URL}).ReportPlayerCheating(ctx, Report{SteamID: 1, AppID: 1}); err == nil {
		t.Fatal("a call without a key did not fail")
	}
}

func TestNumericReportID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"reportid":42}}`))
	}))
	defer srv.Close()
	id, err := (&Client{Key: "k", Base: srv.URL}).ReportPlayerCheating(context.Background(), Report{SteamID: 1, AppID: 1})
	if err != nil || id != 42 {
		t.Fatalf("got %d %v", id, err)
	}
}
