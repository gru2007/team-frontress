package support

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Operation struct {
	AppID     uint32    `json:"app_id"`
	Sandbox   bool      `json:"sandbox"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	Order     string    `json:"order,omitempty"`
	SteamID   string    `json:"steam_id,omitempty"`
	Agreement string    `json:"agreement,omitempty"`
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason"`
	State     string    `json:"state"` // pending, complete, rejected, unknown
	Message   string    `json:"message,omitempty"`
	At        time.Time `json:"at"`
}
type adminState struct {
	Transactions  map[string]Transaction `json:"transactions"`
	Operations    map[string]Operation   `json:"operations"`
	Cursors       map[string]string      `json:"cursors"`
	LastSync      time.Time              `json:"last_sync"`
	SyncError     string                 `json:"sync_error,omitempty"`
	PendingCursor int                    `json:"pending_cursor"`
}
type AdminView struct {
	AppID        uint32        `json:"app_id"`
	Sandbox      bool          `json:"sandbox"`
	Orders       []Order       `json:"orders"`
	Transactions []Transaction `json:"transactions"`
	Operations   []Operation   `json:"operations"`
	LastSync     time.Time     `json:"last_sync"`
	SyncError    string        `json:"sync_error,omitempty"`
}

// Admin serializes reconciliation and money-changing operations. It persists
// an intent before touching Steam; ambiguous operations are queried, not retried.
type Admin struct {
	mu      sync.Mutex
	path    string
	steam   Management
	book    *Book
	appID   uint32
	sandbox bool
	state   adminState
	view    atomic.Pointer[AdminView]
}

func OpenAdmin(path string, steam Management, book *Book, appID uint32, sandbox bool) (*Admin, error) {
	a := &Admin{path: path, steam: steam, book: book, appID: appID, sandbox: sandbox, state: adminState{Transactions: map[string]Transaction{}, Operations: map[string]Operation{}, Cursors: map[string]string{}}}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err == nil {
			if err = json.Unmarshal(raw, &a.state); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if a.state.Transactions == nil {
		a.state.Transactions = map[string]Transaction{}
	}
	if a.state.Operations == nil {
		a.state.Operations = map[string]Operation{}
	}
	if a.state.Cursors == nil {
		a.state.Cursors = map[string]string{}
	}
	a.publish()
	return a, nil
}
func (a *Admin) save() error {
	defer a.publish()
	if a.path == "" {
		return nil
	}
	raw, err := json.Marshal(a.state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(a.path), ".payment-admin-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), a.path)
}

func (a *Admin) View() AdminView {
	v := *a.view.Load()
	v.Orders = a.book.Orders()
	sort.Slice(v.Orders, func(i, j int) bool { return v.Orders[i].Opened.After(v.Orders[j].Opened) })
	if len(v.Orders) > 200 {
		v.Orders = v.Orders[:200]
	}
	return v
}

// publish is called while mutation owns mu, so views never wait on Steam.
func (a *Admin) publish() {
	v := AdminView{AppID: a.appID, Sandbox: a.sandbox, Orders: a.book.Orders(), Transactions: []Transaction{}, Operations: []Operation{}, LastSync: a.state.LastSync, SyncError: a.state.SyncError}
	for _, t := range a.state.Transactions {
		t.Items = append([]TransactionItem{}, t.Items...)
		v.Transactions = append(v.Transactions, t)
	}
	for _, o := range a.state.Operations {
		v.Operations = append(v.Operations, o)
	}
	sort.Slice(v.Orders, func(i, j int) bool { return v.Orders[i].Opened.After(v.Orders[j].Opened) })
	sort.Slice(v.Transactions, func(i, j int) bool { return v.Transactions[i].Time > v.Transactions[j].Time })
	sort.Slice(v.Operations, func(i, j int) bool { return v.Operations[i].At.After(v.Operations[j].At) })
	if len(v.Orders) > 200 {
		v.Orders = v.Orders[:200]
	}
	if len(v.Transactions) > 200 {
		v.Transactions = v.Transactions[:200]
	}
	if len(v.Operations) > 200 {
		v.Operations = v.Operations[:200]
	}
	a.view.Store(&v)
}

func refunded(status string) bool {
	return strings.HasPrefix(strings.ToLower(status), "refunded") || strings.HasPrefix(strings.ToLower(status), "chargedback")
}
func (a *Admin) observe(t Transaction) error {
	if previous, ok := a.state.Transactions[t.TransID]; ok && refunded(previous.Status) && !refunded(t.Status) {
		return nil
	}
	if previous, ok := a.state.Transactions[t.TransID]; ok && previous.Time > t.Time {
		return nil
	}
	if t.TransID != "" && t.TransID != "0" {
		a.state.Transactions[t.TransID] = t
	}
	if t.OrderID != "" && t.OrderID != "0" {
		if refunded(t.Status) {
			if err := a.book.RevokeRefund(t.OrderID); err != nil {
				return err
			}
		}
		if strings.EqualFold(t.Status, "Succeeded") {
			id, _ := strconv.ParseUint(t.OrderID, 10, 64)
			if o := a.book.Order(id); o != nil && strconv.FormatUint(o.SteamID, 10) == t.SteamID {
				if _, err := a.book.MarkPaid(id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (a *Admin) Reconcile(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.reconcile(ctx)
	if err != nil {
		a.state.SyncError = err.Error()
	} else {
		a.state.SyncError = ""
		a.state.LastSync = time.Now().UTC()
	}
	if saveErr := a.save(); saveErr != nil {
		return saveErr
	}
	return err
}
func (a *Admin) reconcile(ctx context.Context) error {
	// Recover a lost authorization callback/finalization reply from Steam's
	// authoritative status, without constructing a second order.
	pending := a.book.Orders()
	sort.Slice(pending, func(i, j int) bool { return pending[i].Opened.Before(pending[j].Opened) })
	start := 0
	if len(pending) > 0 {
		start = a.state.PendingCursor % len(pending)
		pending = append(append([]Order{}, pending[start:]...), pending[:start]...)
	}
	checked := 0
	for position, o := range pending {
		if !o.Paid.IsZero() || o.Refunded || time.Since(o.Opened) > 24*time.Hour {
			continue
		}
		if checked >= 20 {
			break
		}
		checked++
		a.state.PendingCursor = (start + position + 1) % len(pending)
		t, err := a.steam.QueryTxn(ctx, strconv.FormatUint(o.ID, 10))
		if err != nil {
			continue
		}
		if t.SteamID != strconv.FormatUint(o.SteamID, 10) {
			continue
		}
		if strings.EqualFold(t.Status, "Approved") {
			if finalizer, ok := a.steam.(interface {
				FinalizeTxn(context.Context, uint64) error
			}); ok {
				if err := finalizer.FinalizeTxn(ctx, o.ID); err == nil {
					t, err = a.steam.QueryTxn(ctx, strconv.FormatUint(o.ID, 10))
					if err != nil {
						continue
					}
				}
			}
		}
		if err := a.observe(t); err != nil {
			return err
		}
	}
	for _, kind := range []string{"GAMESALES", "SUBSCRIPTION", "CHARGEBACK"} {
		since := a.state.Cursors[kind]
		if since == "" {
			since = time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
			for _, o := range a.book.Orders() {
				if o.Opened.Format(time.RFC3339) < since {
					since = o.Opened.UTC().Format(time.RFC3339)
				}
			}
		}
		queryAt, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return err
		}
		txs, err := a.steam.Report(ctx, kind, queryAt.Add(-time.Second).Format(time.RFC3339))
		if err != nil {
			return err
		}
		if len(txs) >= 10000 {
			return fmt.Errorf("Steam report is saturated; cursor retained, operator reconciliation required")
		}
		for _, t := range txs {
			if err := a.observe(t); err != nil {
				return err
			}
			if at, err := time.Parse(time.RFC3339, t.Time); err == nil && at.After(queryAt) {
				queryAt = at
			}
		}
		a.state.Cursors[kind] = queryAt.UTC().Format(time.RFC3339)
	}
	// Renewal entitlement is derived from successful charges, not merely an
	// agreement's renewal flag. Cancellation preserves the already paid term.
	untils := map[uint64]time.Time{}
	for _, t := range a.state.Transactions {
		if t.AgreementID == "" {
			continue
		}
		uid, _ := strconv.ParseUint(t.SteamID, 10, 64)
		if _, ok := untils[uid]; !ok {
			untils[uid] = time.Time{}
		}
		if strings.EqualFold(t.Status, "Succeeded") {
			if date, err := time.Parse("20060102", t.NextPayment); err == nil && date.After(untils[uid]) {
				untils[uid] = date
			}
		}
	}
	for id, until := range untils {
		if err := a.book.PaidSubscriptionUntil(id, until); err != nil {
			return err
		}
	}
	// Only inspect uncertain intents. Never repeat a refund after a timeout.
	for id, op := range a.state.Operations {
		if op.State == "pending" {
			op.State = "rejected"
			op.Message = "Interrupted before submission to Steam"
			a.state.Operations[id] = op
			continue
		}
		if op.State != "pending" && op.State != "unknown" {
			continue
		}
		if op.Action == "refund" {
			t, err := a.steam.QueryTxn(ctx, op.Order)
			if err == nil {
				if err = a.observe(t); err != nil {
					return err
				}
				if refunded(t.Status) {
					op.State = "complete"
					op.Message = "Refund confirmed by Steam"
				}
			}
		} else {
			uid, _ := strconv.ParseUint(op.SteamID, 10, 64)
			ags, err := a.steam.Agreements(ctx, uid)
			if err == nil {
				for _, ag := range ags {
					if ag.ID == op.Agreement && strings.EqualFold(ag.Status, "Canceled") {
						op.State = "complete"
						op.Message = "Cancellation confirmed by Steam"
					}
				}
			}
		}
		a.state.Operations[id] = op
	}
	return nil
}

// Execute requires a caller-generated idempotency key and a reviewable reason.
func (a *Admin) Execute(ctx context.Context, op Operation) (Operation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return op, err
	}
	if op.AppID != a.appID || op.Sandbox != a.sandbox {
		return op, fmt.Errorf("payment environment changed; review this operation again")
	}
	if old, ok := a.state.Operations[op.ID]; ok {
		if old.Action != op.Action || old.Order != op.Order || old.SteamID != op.SteamID || old.Agreement != op.Agreement || old.Actor != op.Actor || old.Amount != op.Amount || old.Currency != op.Currency {
			return old, fmt.Errorf("idempotency key belongs to a different operation")
		}
		return old, nil
	}
	if len(op.ID) < 16 || len(op.ID) > 80 || len(strings.TrimSpace(op.Reason)) < 3 || len(op.Reason) > 500 || op.Actor == "" {
		return op, fmt.Errorf("invalid operation, actor or reason")
	}
	if op.Action != "refund" && op.Action != "cancel_subscription" {
		return op, fmt.Errorf("unknown operation")
	}
	for _, old := range a.state.Operations {
		if (old.State == "pending" || old.State == "unknown") && old.Action == op.Action && old.Order == op.Order && old.Agreement == op.Agreement {
			return op, fmt.Errorf("an unresolved operation already exists for this target")
		}
	}
	op.At = time.Now().UTC()
	op.State = "pending"
	a.state.Operations[op.ID] = op
	if err := a.save(); err != nil {
		delete(a.state.Operations, op.ID)
		a.publish()
		return op, err
	}
	var err error
	submitted := false
	beginSubmission := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		op.State = "unknown"
		a.state.Operations[op.ID] = op
		if err := a.save(); err != nil {
			return err
		}
		submitted = true
		return nil
	}
	if op.Action == "refund" {
		var t Transaction
		t, err = a.steam.QueryTxn(ctx, op.Order)
		if err == nil && t.OrderID != op.Order {
			err = fmt.Errorf("Steam returned a different order")
		}
		if err == nil && !refunded(t.Status) {
			total := int64(0)
			for _, item := range t.Items {
				total += item.Amount + item.VAT
			}
			if op.Amount <= 0 || op.Amount != total || op.Currency != t.Currency {
				err = fmt.Errorf("amount or currency changed; review the transaction again")
				op.State = "rejected"
			}
		}
		if err == nil && refunded(t.Status) {
			op.State = "complete"
			err = a.observe(t)
		} else if err == nil && !strings.EqualFold(t.Status, "Succeeded") {
			err = fmt.Errorf("only a succeeded Steam order can be refunded")
			op.State = "rejected"
		} else if err == nil {
			if err = beginSubmission(); err == nil {
				err = a.steam.RefundTxn(ctx, op.Order)
			}
			if err == nil {
				op.State = "complete"
				t.Status = "Refunded"
				t.Time = time.Now().UTC().Format(time.RFC3339)
				err = a.observe(t)
			}
		}
	} else {
		uid, parseErr := strconv.ParseUint(op.SteamID, 10, 64)
		if parseErr != nil {
			err = parseErr
			op.State = "rejected"
		} else {
			ags, queryErr := a.steam.Agreements(ctx, uid)
			err = queryErr
			found := false
			if err == nil {
				for _, ag := range ags {
					if ag.ID == op.Agreement {
						found = true
						if strings.EqualFold(ag.Status, "Canceled") {
							op.State = "complete"
						} else {
							if err = beginSubmission(); err == nil {
								err = a.steam.CancelAgreement(ctx, uid, op.Agreement)
							}
							if err == nil {
								op.State = "complete"
							}
						}
						break
					}
				}
				if !found {
					err = fmt.Errorf("agreement not found for this player")
					op.State = "rejected"
				}
			}
		}
	}
	if err != nil {
		if op.State != "complete" {
			if submitted {
				op.State = "unknown"
			} else {
				op.State = "rejected"
			}
		}
		op.Message = err.Error()
	}
	a.state.Operations[op.ID] = op
	if saveErr := a.save(); saveErr != nil {
		return op, saveErr
	}
	return op, nil
}

func (a *Admin) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		call, cancel := context.WithTimeout(ctx, 25*time.Second)
		if err := a.Reconcile(call); err != nil {
			slog.Error("Steam payment reconciliation failed", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
