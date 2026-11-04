package support

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
)

func TestWebAPISpeaksMicroTxn(t *testing.T) {
	var got url.Values
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		got, _ = url.ParseQuery(string(body))
		if r.Method == http.MethodGet {
			got = r.URL.Query()
		}
		switch path {
		case "/ISteamMicroTxnSandbox/GetUserInfo/v2/":
			io.WriteString(w, `{"response":{"result":"OK","params":{"state":"","country":"RU","currency":"RUB","status":"Active"}}}`)
		case "/ISteamMicroTxnSandbox/InitTxn/v3/":
			io.WriteString(w, `{"response":{"result":"OK","params":{"orderid":"42","transid":"7"}}}`)
		case "/ISteamMicroTxnSandbox/FinalizeTxn/v2/":
			io.WriteString(w, `{"response":{"result":"Failure","params":{"orderid":"42"},"error":{"errorcode":5,"errordesc":"not approved"}}}`)
		case "/ISteamMicroTxnSandbox/GetUserAgreementInfo/v2/":
			io.WriteString(w, `{"response":{"result":"OK","params":{"agreements":{"agreement":[{"agreementid":"99","status":"Active","period":"Month","recurringamt":"24900","currency":"RUB"}]}}}}`)
		}
	}))
	defer srv.Close()
	api := &WebAPI{Key: "k", AppID: 5147520, Sandbox: true, Base: srv.URL}
	ctx := context.Background()

	cur, err := api.UserCurrency(ctx, 76561197960265729)
	if err != nil || cur != "RUB" || got.Get("key") != "k" || got.Get("appid") != "5147520" {
		t.Fatalf("currency %q %v %v", cur, err, got)
	}
	err = api.InitTxn(ctx, Txn{OrderID: 42, SteamID: 76561197960265729, Language: "ru", Currency: "RUB",
		Amount: 24900, ItemID: ItemID(Monthly, 0), Description: "Подписка", Monthly: true})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"orderid": "42", "usersession": "client", "amount[0]": "24900",
		"billingtype[0]": "Steam", "period[0]": "Month", "frequency[0]": "1", "recurringamt[0]": "24900", "itemid[0]": "2000"} {
		if got.Get(k) != v {
			t.Errorf("InitTxn %s = %q, want %q", k, got.Get(k), v)
		}
	}
	var se *SteamError
	if err := api.FinalizeTxn(ctx, 42); !errors.As(err, &se) || se.Code != "5" {
		t.Fatalf("finalize: %v", err)
	}
	ags, err := api.Agreements(ctx, 76561197960265729)
	if err != nil || len(ags) != 1 || ags[0].ID != "99" || ags[0].Status != "Active" {
		t.Fatalf("agreements %+v %v", ags, err)
	}
}

func TestBookRemembersSupporters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "support.jsonl")
	b, _ := OpenBook(path)
	b.Open(Order{ID: 1, SteamID: 10, Name: "Pyro", Kind: Once, Amount: 499, Currency: "USD"})
	b.Open(Order{ID: 2, SteamID: 11, Name: "Spy", Kind: Monthly, Amount: 299, Currency: "USD", Anon: true})
	b.Open(Order{ID: 3, SteamID: 12, Name: "Scout", Kind: Once, Amount: 199, Currency: "USD"})
	if !b.Paid(1) || b.Paid(1) {
		t.Fatal("an order paid twice, or not at all")
	}
	b.Paid(2)
	b.Failed(3, "denied")
	b.Close()

	b, _ = OpenBook(path)
	if s, m := b.IsSupporter(10); !s || m {
		t.Fatal("one-off supporter lost")
	}
	if s, m := b.IsSupporter(11); !s || !m {
		t.Fatal("subscriber lost")
	}
	if s, _ := b.IsSupporter(12); s {
		t.Fatal("a failed order made a supporter")
	}
	wall := b.Wall(10)
	if len(wall) != 1 || wall[0].Name != "Pyro" {
		t.Fatalf("wall %+v (anonymous donors stay off it)", wall)
	}
}

func TestPrices(t *testing.T) {
	if cur, p := PricesFor("rub", nil); cur != "RUB" || p.Once[0] != 14900 {
		t.Fatal(cur, p)
	}
	if cur, _ := PricesFor("XYZ", nil); cur != "USD" {
		t.Fatal(cur)
	}
	for cur, p := range DefaultPrices {
		step := int64(1)
		switch cur {
		case "UAH", "KZT":
			step = 100
		}
		for _, a := range append(append([]int64{}, p.Once...), p.Monthly...) {
			if a%step != 0 {
				t.Errorf("%s %d is not a multiple of %d", cur, a, step)
			}
		}
	}
}
