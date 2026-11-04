// Package support is the "support the developers" tab: one-off donations and
// a monthly subscription, paid through Steam microtransactions.
//
// The flow is Steam's (partner.steamgames.com/doc/features/microtransactions):
//
//  1. the game asks the GC for a donation (a tier, once or monthly);
//  2. the GC asks Steam for the player's currency (GetUserInfo) and opens the
//     order (InitTxn, usersession=client), with the publisher key, which never
//     leaves the GC;
//  3. Steam shows the player the approval dialog in the overlay;
//  4. the game gets MicroTxnAuthorizationResponse_t and tells the GC;
//  5. the GC finalizes the order (FinalizeTxn). Only then is it paid.
//
// A monthly tier is the same order with a Steam-billed agreement on it
// (billingtype Steam, period Month); Steam bills it every month until the
// player cancels it, here or in their Steam account.
//
// Donations buy nothing that matters in a fight: a supporter badge, a name on
// the thank-you wall, and the developers' gratitude.
package support

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kind of donation.
type Kind string

const (
	Once    Kind = "once"
	Monthly Kind = "monthly"
)

// Prices are one currency's tiers, in its cents.
type Prices struct {
	Once    []int64 `json:"once"`
	Monthly []int64 `json:"monthly"`
}

// DefaultPrices are the tiers per currency. Steam wants some currencies in
// round steps (UAH, KZT in hundreds); these respect that. A currency missing
// here is charged in USD and Steam converts.
var DefaultPrices = map[string]Prices{
	"USD": {Once: []int64{199, 499, 999, 2499}, Monthly: []int64{299, 999}},
	"EUR": {Once: []int64{199, 499, 999, 2499}, Monthly: []int64{299, 999}},
	"GBP": {Once: []int64{179, 449, 899, 1999}, Monthly: []int64{249, 899}},
	"RUB": {Once: []int64{14900, 39900, 79900, 199000}, Monthly: []int64{24900, 79900}},
	"UAH": {Once: []int64{8000, 20000, 40000, 100000}, Monthly: []int64{10000, 40000}},
	"KZT": {Once: []int64{100000, 250000, 500000, 1200000}, Monthly: []int64{150000, 500000}},
	"PLN": {Once: []int64{799, 1999, 3999, 9999}, Monthly: []int64{1199, 3999}},
	"BRL": {Once: []int64{990, 2490, 4990, 12490}, Monthly: []int64{1490, 4990}},
	"CNY": {Once: []int64{1400, 3500, 7000, 17500}, Monthly: []int64{2100, 7000}},
}

// Tiers are named in the game: fui_support_once_<i>, fui_support_monthly_<i>.

// --- Steam ------------------------------------------------------------------

// Steam is the part of ISteamMicroTxn the GC uses.
type Steam interface {
	UserCurrency(ctx context.Context, steamID uint64) (string, error)
	InitTxn(ctx context.Context, t Txn) error
	FinalizeTxn(ctx context.Context, orderID uint64) error
	Agreements(ctx context.Context, steamID uint64) ([]Agreement, error)
	CancelAgreement(ctx context.Context, steamID uint64, agreementID string) error
}

// Txn is one order.
type Txn struct {
	OrderID     uint64
	SteamID     uint64
	Language    string // ISO 639-1
	Currency    string
	Amount      int64
	ItemID      uint32
	Description string
	Monthly     bool
}

// Agreement is a recurring billing agreement, as Steam reports it.
type Agreement struct {
	ID          string `json:"agreementid"`
	ItemID      string `json:"itemid"`
	Status      string `json:"status"` // Active, Canceled, Processing
	Period      string `json:"period"`
	Amount      string `json:"recurringamt"`
	Currency    string `json:"currency"`
	NextPayment string `json:"nextpayment"`
}

// WebAPI is the real Steam: ISteamMicroTxn, or its sandbox.
type WebAPI struct {
	Key     string
	AppID   uint32
	Sandbox bool
	Base    string // tests; default https://partner.steam-api.com
	Client  *http.Client
}

// SteamError is Steam saying no, with its code.
type SteamError struct {
	Code string
	Desc string
}

func (e *SteamError) Error() string { return "steam: " + e.Code + " " + e.Desc }

func (w *WebAPI) url(method, version string) string {
	base := w.Base
	if base == "" {
		base = "https://partner.steam-api.com"
	}
	iface := "ISteamMicroTxn"
	if w.Sandbox {
		iface = "ISteamMicroTxnSandbox"
	}
	return fmt.Sprintf("%s/%s/%s/%s/", strings.TrimRight(base, "/"), iface, method, version)
}

func (w *WebAPI) call(ctx context.Context, httpMethod, method, version string, form url.Values, into any) error {
	form.Set("key", w.Key)
	form.Set("appid", strconv.FormatUint(uint64(w.AppID), 10))
	var req *http.Request
	var err error
	if httpMethod == http.MethodGet {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, w.url(method, version)+"?"+form.Encode(), nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, w.url(method, version), strings.NewReader(form.Encode()))
		if req != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return err
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("steam %s: request failed", method)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("steam %s: HTTP %d", method, resp.StatusCode)
	}
	var env struct {
		Response struct {
			Result string          `json:"result"`
			Params json.RawMessage `json:"params"`
			Error  struct {
				Code json.Number `json:"errorcode"`
				Desc string      `json:"errordesc"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("steam %s: %w", method, err)
	}
	if env.Response.Result != "OK" {
		return &SteamError{Code: env.Response.Error.Code.String(), Desc: env.Response.Error.Desc}
	}
	if into != nil && len(env.Response.Params) > 0 {
		return json.Unmarshal(env.Response.Params, into)
	}
	return nil
}

// UserCurrency is the currency Steam will charge the player in.
func (w *WebAPI) UserCurrency(ctx context.Context, steamID uint64) (string, error) {
	var p struct {
		Currency string `json:"currency"`
		Status   string `json:"status"`
	}
	err := w.call(ctx, http.MethodGet, "GetUserInfo", "v2", url.Values{"steamid": {strconv.FormatUint(steamID, 10)}}, &p)
	if err != nil {
		return "", err
	}
	if p.Status == "Locked" || strings.HasPrefix(p.Status, "Locked") {
		return "", &SteamError{Code: "locked", Desc: p.Status}
	}
	return p.Currency, nil
}

// InitTxn opens an order; Steam shows the player the approval dialog.
func (w *WebAPI) InitTxn(ctx context.Context, t Txn) error {
	form := url.Values{
		"orderid":        {strconv.FormatUint(t.OrderID, 10)},
		"steamid":        {strconv.FormatUint(t.SteamID, 10)},
		"itemcount":      {"1"},
		"language":       {t.Language},
		"currency":       {t.Currency},
		"usersession":    {"client"},
		"itemid[0]":      {strconv.FormatUint(uint64(t.ItemID), 10)},
		"qty[0]":         {"1"},
		"amount[0]":      {strconv.FormatInt(t.Amount, 10)},
		"description[0]": {t.Description},
		"category[0]":    {"donation"},
	}
	if t.Monthly {
		form.Set("category[0]", "subscription")
		form.Set("billingtype[0]", "Steam")
		form.Set("period[0]", "Month")
		form.Set("frequency[0]", "1")
		form.Set("recurringamt[0]", strconv.FormatInt(t.Amount, 10))
	}
	return w.call(ctx, http.MethodPost, "InitTxn", "v3", form, nil)
}

// FinalizeTxn completes an approved order: the money moves.
func (w *WebAPI) FinalizeTxn(ctx context.Context, orderID uint64) error {
	return w.call(ctx, http.MethodPost, "FinalizeTxn", "v2", url.Values{"orderid": {strconv.FormatUint(orderID, 10)}}, nil)
}

// Agreements are the player's billing agreements for the app.
func (w *WebAPI) Agreements(ctx context.Context, steamID uint64) ([]Agreement, error) {
	var p struct {
		Agreements json.RawMessage `json:"agreements"`
	}
	if err := w.call(ctx, http.MethodGet, "GetUserAgreementInfo", "v2", url.Values{"steamid": {strconv.FormatUint(steamID, 10)}}, &p); err != nil {
		return nil, err
	}
	return parseAgreements(p.Agreements), nil
}

// parseAgreements reads either shape Steam uses: a list, or
// {"agreement": [...]} (the XML-shaped JSON).
func parseAgreements(raw json.RawMessage) []Agreement {
	var list []Agreement
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var wrapped struct {
		Agreement json.RawMessage `json:"agreement"`
	}
	if json.Unmarshal(raw, &wrapped) != nil {
		return nil
	}
	if json.Unmarshal(wrapped.Agreement, &list) == nil {
		return list
	}
	var one Agreement
	if json.Unmarshal(wrapped.Agreement, &one) == nil && one.ID != "" {
		return []Agreement{one}
	}
	return nil
}

// CancelAgreement stops a subscription.
func (w *WebAPI) CancelAgreement(ctx context.Context, steamID uint64, agreementID string) error {
	return w.call(ctx, http.MethodPost, "CancelAgreement", "v1", url.Values{
		"steamid": {strconv.FormatUint(steamID, 10)}, "agreementid": {agreementID},
	}, nil)
}

// Fake approves everything, for working on the screens without Steam.
type Fake struct {
	mu      sync.Mutex
	Monthly map[uint64]bool
}

func (f *Fake) UserCurrency(context.Context, uint64) (string, error) { return "RUB", nil }
func (f *Fake) InitTxn(_ context.Context, t Txn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.Monthly {
		if f.Monthly == nil {
			f.Monthly = map[uint64]bool{}
		}
		f.Monthly[t.SteamID] = true
	}
	return nil
}
func (f *Fake) FinalizeTxn(context.Context, uint64) error { return nil }
func (f *Fake) Agreements(_ context.Context, id uint64) ([]Agreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Monthly[id] {
		return []Agreement{{ID: "fake-" + strconv.FormatUint(id, 10), Status: "Active", Period: "Month", Currency: "RUB"}}, nil
	}
	return nil, nil
}
func (f *Fake) CancelAgreement(_ context.Context, id uint64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.Monthly, id)
	return nil
}

// --- the record of who gave --------------------------------------------------

// Order is one donation, from opening to paid.
type Order struct {
	ID       uint64    `json:"order,string"`
	SteamID  uint64    `json:"steam_id,string"`
	Name     string    `json:"name,omitempty"`
	Kind     Kind      `json:"kind"`
	Tier     int       `json:"tier"`
	Amount   int64     `json:"amount"`
	Currency string    `json:"currency"`
	Anon     bool      `json:"anon,omitempty"`
	Opened   time.Time `json:"opened"`
	Paid     time.Time `json:"paid,omitempty"`
	Failed   string    `json:"failed,omitempty"`
	Refunded bool      `json:"refunded"`
}

// Supporter is everything one player gave.
type Supporter struct {
	SteamID   uint64
	Name      string
	Donations int
	Since     time.Time
	Last      time.Time
	Monthly   bool // has an active subscription, as far as we last heard
	PaidUntil time.Time
	Anon      bool // the last donation asked not to be named
}

// Thanks is one line on the thank-you wall.
type Thanks struct {
	Name string    `json:"name"`
	Kind Kind      `json:"kind"`
	At   time.Time `json:"at"`
}

type event struct {
	Type  string    `json:"t"`
	At    time.Time `json:"at"`
	Order *Order    `json:"order,omitempty"`
	ID    uint64    `json:"id,string,omitempty"`
	Why   string    `json:"why,omitempty"`
	On    bool      `json:"on,omitempty"`
	Until time.Time `json:"until,omitempty"`
}

// Book is the record of orders and supporters.
type Book struct {
	mu         sync.Mutex
	orders     map[uint64]*Order
	supporters map[uint64]*Supporter
	wall       []Thanks
	f          *os.File
	now        func() time.Time
	writeErr   error
}

// OpenBook opens the record. An empty path keeps it in memory.
func OpenBook(path string) (*Book, error) {
	b := &Book{orders: map[uint64]*Order{}, supporters: map[uint64]*Supporter{}, now: time.Now}
	if path == "" {
		return b, nil
	}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			var ev event
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				f.Close()
				return nil, fmt.Errorf("support log is corrupt: %w", err)
			}
			b.apply(ev)
		}
		if err := sc.Err(); err != nil {
			f.Close()
			return nil, err
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("support: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("support: %w", err)
	}
	b.f = f
	return b, nil
}

// UseClock replaces the clock. Tests only.
func (b *Book) UseClock(f func() time.Time) { b.mu.Lock(); b.now = f; b.mu.Unlock() }

// Close flushes the record.
func (b *Book) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.f == nil {
		return nil
	}
	err := b.f.Close()
	b.f = nil
	return err
}

func (b *Book) write(ev event) error {
	if b.writeErr != nil {
		return b.writeErr
	}
	ev.At = b.now()
	if b.f != nil {
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err = b.f.Write(append(raw, '\n')); err != nil {
			b.writeErr = err
			return err
		}
		if err = b.f.Sync(); err != nil {
			b.writeErr = err
			return err
		}
	}
	b.apply(ev)
	return nil
}

func (b *Book) apply(ev event) {
	switch ev.Type {
	case "refunded":
		o := b.orders[ev.ID]
		if o == nil || o.Refunded {
			return
		}
		o.Refunded = true
		if s := b.supporters[o.SteamID]; s != nil {
			s.Donations = 0
			hasMonthly := false
			for _, past := range b.orders {
				if past.SteamID == o.SteamID && !past.Paid.IsZero() && !past.Refunded {
					s.Donations++
					hasMonthly = hasMonthly || past.Kind == Monthly
				}
			}
			if !hasMonthly {
				s.Monthly = false
			}
			if s.Donations == 0 {
				delete(b.supporters, o.SteamID)
			}
		}
		b.wall = nil
		orders := make([]Order, 0, len(b.orders))
		for _, past := range b.orders {
			if !past.Paid.IsZero() && !past.Refunded && !past.Anon {
				orders = append(orders, *past)
			}
		}
		sort.Slice(orders, func(i, j int) bool { return orders[i].Paid.Before(orders[j].Paid) })
		for _, past := range orders {
			b.wall = append(b.wall, Thanks{Name: past.Name, Kind: past.Kind, At: past.Paid})
		}
		if len(b.wall) > 40 {
			b.wall = b.wall[len(b.wall)-40:]
		}
	case "open":
		if ev.Order != nil {
			o := *ev.Order
			b.orders[o.ID] = &o
		}
	case "paid":
		o := b.orders[ev.ID]
		if o == nil || !o.Paid.IsZero() {
			return
		}
		o.Paid = ev.At
		o.Failed = ""
		s := b.supporters[o.SteamID]
		if s == nil {
			s = &Supporter{SteamID: o.SteamID, Since: ev.At}
			b.supporters[o.SteamID] = s
		}
		s.Donations++
		s.Last = ev.At
		s.Anon = o.Anon
		if o.Name != "" {
			s.Name = o.Name
		}
		if o.Kind == Monthly {
			s.Monthly = true
		}
		if !o.Anon {
			b.wall = append(b.wall, Thanks{Name: o.Name, Kind: o.Kind, At: ev.At})
			if len(b.wall) > 40 {
				b.wall = b.wall[len(b.wall)-40:]
			}
		}
	case "failed":
		if o := b.orders[ev.ID]; o != nil && o.Paid.IsZero() {
			o.Failed = ev.Why
		}
	case "monthly":
		if b.supporters[ev.ID] == nil && ev.On {
			b.supporters[ev.ID] = &Supporter{SteamID: ev.ID, Since: ev.At}
		}
		if s := b.supporters[ev.ID]; s != nil {
			s.Monthly = ev.On
			s.PaidUntil = ev.Until
			if !ev.On && s.Donations == 0 {
				delete(b.supporters, ev.ID)
			}
		}
	}
}

// Open records a new order.
func (b *Book) Open(o Order) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	o.Opened = b.now()
	return b.write(event{Type: "open", Order: &o})
}

// Order is an order by id, a copy, or nil.
func (b *Book) Order(id uint64) *Order {
	b.mu.Lock()
	defer b.mu.Unlock()
	if o := b.orders[id]; o != nil {
		out := *o
		return &out
	}
	return nil
}

// Paid marks an order paid. It reports false if it already was.
func (b *Book) Paid(id uint64) bool {
	paid, _ := b.MarkPaid(id)
	return paid
}

func (b *Book) MarkPaid(id uint64) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o := b.orders[id]
	if o == nil || !o.Paid.IsZero() {
		return false, nil
	}
	if err := b.write(event{Type: "paid", ID: id}); err != nil {
		return false, err
	}
	return true, nil
}

// Failed marks an order as not going ahead.
func (b *Book) Failed(id uint64, why string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if o := b.orders[id]; o != nil && o.Paid.IsZero() {
		b.write(event{Type: "failed", ID: id, Why: why})
	}
}

// SetMonthly records what Steam says about a player's subscription.
func (b *Book) SetMonthly(id uint64, on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.supporters[id]; s != nil && s.Monthly != on {
		b.write(event{Type: "monthly", ID: id, On: on})
	}
}

// Supporter is a player's record, or nil.
func (b *Book) Supporter(id uint64) *Supporter {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.supporters[id]; s != nil {
		out := *s
		if !out.PaidUntil.IsZero() && !b.now().Before(out.PaidUntil) {
			out.Monthly = false
		}
		return &out
	}
	return nil
}

// IsSupporter reports whether a player has ever given, and whether they
// subscribe.
func (b *Book) IsSupporter(id uint64) (supporter, monthly bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.supporters[id]
	return s != nil, s != nil && s.Monthly && (s.PaidUntil.IsZero() || b.now().Before(s.PaidUntil))
}

// Wall is the thank-you wall, newest first.
func (b *Book) Wall(n int) []Thanks {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Thanks, 0, n)
	for i := len(b.wall) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, b.wall[i])
	}
	return out
}

// Count is how many players have given.
func (b *Book) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.supporters)
}

// Subscribers is how many subscribe.
func (b *Book) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, s := range b.supporters {
		if s.Monthly && (s.PaidUntil.IsZero() || b.now().Before(s.PaidUntil)) {
			n++
		}
	}
	return n
}

// --- prices -----------------------------------------------------------------

// PricesFor are the tiers for a currency, and the currency they are in.
func PricesFor(currency string, custom map[string]Prices) (string, Prices) {
	currency = strings.ToUpper(currency)
	if p, ok := custom[currency]; ok && len(p.Once) > 0 {
		return currency, p
	}
	if p, ok := DefaultPrices[currency]; ok {
		return currency, p
	}
	if p, ok := custom["USD"]; ok && len(p.Once) > 0 {
		return "USD", p
	}
	return "USD", DefaultPrices["USD"]
}

// Amount is a tier's price.
func (p Prices) Amount(kind Kind, tier int) (int64, error) {
	list := p.Once
	if kind == Monthly {
		list = p.Monthly
	}
	if tier < 0 || tier >= len(list) {
		return 0, errors.New("no such tier")
	}
	return list[tier], nil
}

// ItemID is the order's item: one per kind and tier, as Steam reports them.
func ItemID(kind Kind, tier int) uint32 {
	if kind == Monthly {
		return uint32(2000 + tier)
	}
	return uint32(1000 + tier)
}

// Sorted helps tests and the HTTP API list supporters by first donation.
func (b *Book) Sorted() []Supporter {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Supporter, 0, len(b.supporters))
	for _, s := range b.supporters {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}
