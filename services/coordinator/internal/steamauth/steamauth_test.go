package steamauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

const realID = wire.SteamID("76561198000000001")

func fakeSteam(t *testing.T, body string) *WebAPIVerifier {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &WebAPIVerifier{APIKey: "k", AppID: 5147520, BaseURL: srv.URL, Client: srv.Client()}
}

func TestWebAPIAcceptsAGoodTicket(t *testing.T) {
	v := fakeSteam(t, `{"response":{"params":{"result":"OK","steamid":"76561198000000001","ownersteamid":"76561198000000001","vacbanned":false,"publisherbanned":false}}}`)
	got, err := v.Verify(context.Background(), realID, "abc")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != realID {
		t.Fatalf("id = %s, want %s", got, realID)
	}
	if !v.Verified() {
		t.Error("webapi verification did not report itself as verified")
	}
}

func TestWebAPIRefusesABadTicket(t *testing.T) {
	v := fakeSteam(t, `{"response":{"error":{"errorcode":101,"errordesc":"Invalid ticket"}}}`)
	if _, err := v.Verify(context.Background(), realID, "abc"); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

func TestWebAPIRefusesAPublisherBan(t *testing.T) {
	v := fakeSteam(t, `{"response":{"params":{"result":"OK","steamid":"76561198000000001","publisherbanned":true}}}`)
	if _, err := v.Verify(context.Background(), realID, "abc"); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected for a banned account", err)
	}
}

// The client says it is one account; the ticket belongs to another. The
// coordinator must not act on the claim.
func TestWebAPICatchesAnIdentityMismatch(t *testing.T) {
	v := fakeSteam(t, `{"response":{"params":{"result":"OK","steamid":"76561198000000001"}}}`)
	_, err := v.Verify(context.Background(), "76561198000009999", "abc")
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected when the claim and the ticket disagree", err)
	}
}

func TestWebAPIRefusesAnEmptyTicket(t *testing.T) {
	v := fakeSteam(t, `{"response":{"params":{"result":"OK","steamid":"76561198000000001"}}}`)
	if _, err := v.Verify(context.Background(), realID, "   "); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected for an empty ticket", err)
	}
}

func TestDevVerifierIsHonestAboutBeingUnverified(t *testing.T) {
	var v DevVerifier
	if v.Verified() {
		t.Fatal("dev auth claimed to be proof of identity")
	}
	got, err := v.Verify(context.Background(), realID, "")
	if err != nil || got != realID {
		t.Fatalf("verify = %v, %v", got, err)
	}
	if _, err := v.Verify(context.Background(), "notanid", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected even in dev mode", err)
	}
}

func TestValidSteamIDRejectsWhatIsNotAPlayer(t *testing.T) {
	for _, bad := range []wire.SteamID{"", "0", "1", "76561197960265727", "notanumber", "-1"} {
		if ValidSteamID(bad) {
			t.Errorf("%q was accepted as a SteamID64", bad)
		}
	}
	if !ValidSteamID("76561197960265728") {
		t.Error("the first individual SteamID64 was rejected")
	}
}
