package tf2pickup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/mm"
)

func TestCreateGameSendsMatchMode(t *testing.T) {
	const matchID = "0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/frontress/v1/games" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			MatchMode string `json:"matchMode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.MatchMode != "ranked" {
			t.Errorf("matchMode = %q, want ranked", body.MatchMode)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"externalMatchId":%q,"map":"cp_process_final","matchGroup":2,"maxPlayers":12,"state":"created","players":[]}`, matchID)
	}))
	defer server.Close()

	client, err := New(server.URL, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateGame(context.Background(), mm.BackendGameRequest{
		ExternalMatchID: matchID,
		Map:             "cp_process_final",
		MatchGroup:      2,
		MatchMode:       "ranked",
		MaxPlayers:      12,
		ServerConfig:    "frontress_ranked",
		MatchEmulation:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestActiveGamesAndForceEnd(t *testing.T) {
	const matchID = "0123456789abcdef"
	createdAt := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	readyAt := createdAt.Add(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "secret test-secret" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/frontress/v1/games":
			fmt.Fprintf(w, `{"games":[{"externalMatchId":%q,"map":"koth_product_final","matchGroup":7,"maxPlayers":24,"state":"launching","createdAt":%q,"readyAt":%q,"startedAt":null,"players":[],"server":{"connect":"127.0.0.1:27015","password":"pw","stv":null}}]}`, matchID, createdAt.Format(time.RFC3339), readyAt.Format(time.RFC3339))
		case r.Method == http.MethodPut && r.URL.Path == "/api/frontress/v1/games/"+matchID+"/force-end":
			fmt.Fprintf(w, `{"externalMatchId":%q,"map":"koth_product_final","matchGroup":7,"maxPlayers":24,"state":"interrupted","createdAt":%q,"readyAt":null,"startedAt":null,"players":[],"server":null}`, matchID, createdAt.Format(time.RFC3339))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, "test-secret")
	if err != nil {
		t.Fatal(err)
	}

	games, err := client.ActiveGames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].ExternalMatchID != matchID {
		t.Fatalf("games = %#v", games)
	}
	if !games[0].CreatedAt.Equal(createdAt) || !games[0].ReadyAt.Equal(readyAt) {
		t.Fatalf("timestamps = %v, %v", games[0].CreatedAt, games[0].ReadyAt)
	}
	ended, err := client.ForceEnd(context.Background(), matchID)
	if err != nil {
		t.Fatal(err)
	}
	if !ended.Over() {
		t.Fatalf("ForceEnd state = %q", ended.State)
	}
}
