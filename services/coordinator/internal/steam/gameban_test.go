package steam

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// call is one request the fake Steam saw.
type call struct {
	method string
	form   map[string]string
}

func fakeSteam(t *testing.T, reportID string, banStatus int) (*CheatReporter, *[]call) {
	t.Helper()
	var seen []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("bad form: %v", err)
		}
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		seen = append(seen, call{method: parts[1], form: form})

		switch parts[1] {
		case "ReportPlayerCheating":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"response":{"reportid":"` + reportID + `"}}`))
		case "RequestPlayerGameBan":
			w.WriteHeader(banStatus)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	c := NewCheatReporter("publisher-key", 5147520)
	c.BaseURL = srv.URL
	return c, &seen
}

func TestGameBanReportsThenBans(t *testing.T) {
	c, seen := fakeSteam(t, "9001", http.StatusOK)

	id, err := c.Ban(context.Background(), GameBanRequest{
		SteamID:     76561198000000001,
		Description: "aimbot",
		Duration:    72 * time.Hour,
	})
	if err != nil {
		t.Fatalf("ban: %v", err)
	}
	if id != 9001 {
		t.Fatalf("report id = %d, want 9001", id)
	}

	if len(*seen) != 2 {
		t.Fatalf("made %d calls, want a report followed by a ban: %+v", len(*seen), *seen)
	}
	report, ban := (*seen)[0], (*seen)[1]
	if report.method != "ReportPlayerCheating" || ban.method != "RequestPlayerGameBan" {
		t.Fatalf("wrong order: %s then %s", report.method, ban.method)
	}
	if ban.form["reportid"] != "9001" {
		t.Fatalf("the ban was not tied to the report: %+v", ban.form)
	}
	if ban.form["appid"] != "5147520" || ban.form["steamid"] != "76561198000000001" {
		t.Fatalf("wrong target: %+v", ban.form)
	}
	// The whole point of the -for flag: the duration has to reach Steam, in
	// seconds, and not be silently dropped into a permanent ban.
	if ban.form["duration"] != "259200" {
		t.Fatalf("duration = %q, want 259200 (72h in seconds)", ban.form["duration"])
	}
	if ban.form["cheatdescription"] != "aimbot" {
		t.Fatalf("description = %q", ban.form["cheatdescription"])
	}
}

func TestPermanentGameBanSendsZeroDuration(t *testing.T) {
	c, seen := fakeSteam(t, "1", http.StatusOK)
	if _, err := c.Ban(context.Background(), GameBanRequest{
		SteamID: 76561198000000001, Description: "cheating",
	}); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if got := (*seen)[1].form["duration"]; got != "0" {
		t.Fatalf("duration = %q, want 0 for a permanent ban", got)
	}
}

func TestGameBanWithNoDescriptionStillSaysSomething(t *testing.T) {
	c, seen := fakeSteam(t, "1", http.StatusOK)
	c.Ban(context.Background(), GameBanRequest{SteamID: 76561198000000001})
	if got := (*seen)[1].form["cheatdescription"]; got == "" {
		t.Fatal("Steam requires a description, so one has to be invented")
	}
}

// A failed ban still returns the report id: the accusation is filed, and an
// operator has to be able to find it.
func TestFailedBanStillReturnsTheReportID(t *testing.T) {
	c, _ := fakeSteam(t, "9001", http.StatusForbidden)

	id, err := c.Ban(context.Background(), GameBanRequest{
		SteamID: 76561198000000001, Description: "aimbot",
	})
	if err == nil {
		t.Fatal("a 403 from Steam should be an error")
	}
	if id != 9001 {
		t.Fatalf("report id = %d, want the filed report 9001", id)
	}
	// 403 is the one that always means the same thing, so the message has to
	// name it rather than leaving an operator to guess at their key.
	if !strings.Contains(err.Error(), "publisher") {
		t.Fatalf("a 403 should explain the publisher key: %v", err)
	}
}

func TestGameBansNeedAKeyAndAnAppID(t *testing.T) {
	for _, c := range []*CheatReporter{
		NewCheatReporter("", 5147520),
		NewCheatReporter("key", 0),
	} {
		if c.Available() {
			t.Fatalf("reporter should not be usable: key=%q appid=%d", c.Key, c.AppID)
		}
		if _, err := c.Ban(context.Background(), GameBanRequest{SteamID: 1}); !errors.Is(err, ErrNoGameBans) {
			t.Fatalf("ban: err = %v, want ErrNoGameBans", err)
		}
		if err := c.Unban(context.Background(), 1); !errors.Is(err, ErrNoGameBans) {
			t.Fatalf("unban: err = %v, want ErrNoGameBans", err)
		}
	}
}

func TestUnban(t *testing.T) {
	c, seen := fakeSteam(t, "1", http.StatusOK)
	if err := c.Unban(context.Background(), 76561198000000001); err != nil {
		t.Fatalf("unban: %v", err)
	}
	if len(*seen) != 1 || (*seen)[0].method != "RemovePlayerGameBan" {
		t.Fatalf("unexpected calls: %+v", *seen)
	}
	if (*seen)[0].form["steamid"] != "76561198000000001" {
		t.Fatalf("wrong target: %+v", (*seen)[0].form)
	}
}

func TestNilTargetsAreRefusedBeforeReachingSteam(t *testing.T) {
	c, seen := fakeSteam(t, "1", http.StatusOK)
	if _, err := c.Ban(context.Background(), GameBanRequest{SteamID: 0}); !errors.Is(err, ErrGameBanRefused) {
		t.Fatalf("err = %v, want ErrGameBanRefused", err)
	}
	if len(*seen) != 0 {
		t.Fatal("a ban with no SteamID should never reach Steam")
	}
}
