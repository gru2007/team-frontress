package support

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Transaction is a Steam observation, not a client assertion of payment.
type Transaction struct {
	OrderID         string            `json:"orderid"`
	TransID         string            `json:"transid"`
	SteamID         string            `json:"steamid"`
	Status          string            `json:"status"`
	Currency        string            `json:"currency"`
	Time            string            `json:"time"`
	Created         string            `json:"timecreated"`
	AgreementID     string            `json:"agreementid,omitempty"`
	AgreementStatus string            `json:"agreementstatus,omitempty"`
	NextPayment     string            `json:"nextpayment,omitempty"`
	Items           []TransactionItem `json:"items"`
}
type TransactionItem struct {
	ItemID uint32 `json:"itemid"`
	Amount int64  `json:"amount"`
	VAT    int64  `json:"vat"`
	Status string `json:"itemstatus"`
}

type Management interface {
	QueryTxn(context.Context, string) (Transaction, error)
	Report(context.Context, string, string) ([]Transaction, error)
	RefundTxn(context.Context, string) error
	Agreements(context.Context, uint64) ([]Agreement, error)
	CancelAgreement(context.Context, uint64, string) error
}

func (w *WebAPI) QueryTxn(ctx context.Context, order string) (Transaction, error) {
	var t Transaction
	err := w.call(ctx, http.MethodGet, "QueryTxn", "v3", url.Values{"orderid": {order}}, &t)
	return t, err
}
func (w *WebAPI) Report(ctx context.Context, kind, since string) ([]Transaction, error) {
	var p struct {
		Orders []Transaction `json:"orders"`
	}
	err := w.call(ctx, http.MethodGet, "GetReport", "v5", url.Values{"type": {kind}, "time": {since}, "maxresults": {"10000"}}, &p)
	return p.Orders, err
}
func (w *WebAPI) RefundTxn(ctx context.Context, order string) error {
	return w.call(ctx, http.MethodPost, "RefundTxn", "v2", url.Values{"orderid": {order}}, nil)
}

func (b *Book) Orders() []Order {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Order, 0, len(b.orders))
	for _, o := range b.orders {
		out = append(out, *o)
	}
	return out
}

func (b *Book) PaidSubscriptionUntil(id uint64, until time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.supporters[id]
	if s == nil {
		return nil
	}
	on := until.After(b.now())
	if s.PaidUntil.Equal(until) && s.Monthly == on {
		return nil
	}
	return b.write(event{Type: "monthly", ID: id, On: on, Until: until})
}

// RevokeRefund removes cosmetic credit for a refunded/charged-back order.
func (b *Book) RevokeRefund(order string) error {
	id, err := strconv.ParseUint(order, 10, 64)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	o := b.orders[id]
	if o == nil || o.Refunded {
		return nil
	}
	return b.write(event{Type: "refunded", ID: id})
}
