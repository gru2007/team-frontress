package inventory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
	"google.golang.org/protobuf/proto"
)

func TestTC2SDKLoadsOnlyEconObjects(t *testing.T) {
	owner := uint64(76561198000000001)
	upstream := &gcproto.CMsgSOCacheSubscribed{
		Owner: proto.Uint64(owner), Version: proto.Uint64(41),
		Objects: []*gcproto.CMsgSOCacheSubscribed_SubscribedType{
			{TypeId: proto.Int32(econItemType), ObjectData: [][]byte{{1, 2, 3}}},
			{TypeId: proto.Int32(2003), ObjectData: [][]byte{{9}}},
		},
	}
	raw, err := proto.Marshal(upstream)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("appid"); got != "5147520" {
			t.Errorf("appid = %q", got)
		}
		if got := r.URL.Query().Get("ticket"); got != "inventory-ticket" {
			t.Errorf("ticket = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": 1, "steamID": owner, "version": 41,
			"msg": base64.StdEncoding.EncodeToString(raw),
		})
	}))
	defer server.Close()

	got, err := (&TC2SDK{Endpoint: server.URL}).Load(context.Background(), Request{
		SteamID: wire.SteamID("76561198000000001"), AppID: 5147520, Ticket: "inventory-ticket",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetOwner() != owner || got.GetVersion() != 41 {
		t.Fatalf("owner/version = %d/%d", got.GetOwner(), got.GetVersion())
	}
	if len(got.Objects) != 1 || got.Objects[0].GetTypeId() != econItemType {
		t.Fatalf("objects = %+v", got.Objects)
	}
}

func TestTC2SDKRejectsWrongOwner(t *testing.T) {
	cache, _ := proto.Marshal(&gcproto.CMsgSOCacheSubscribed{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": 1, "steamID": "76561198000000002", "msg": base64.StdEncoding.EncodeToString(cache),
		})
	}))
	defer server.Close()

	_, err := (&TC2SDK{Endpoint: server.URL}).Load(context.Background(), Request{
		SteamID: wire.SteamID("76561198000000001"), AppID: 5147520, Ticket: "ticket",
	})
	if err == nil {
		t.Fatal("expected owner mismatch")
	}
}
