package support

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type managementFake struct {
	tx         Transaction
	refunds    int
	cancels    int
	failRefund bool
	canceled   bool
	queryErr   error
}

func (f *managementFake) QueryTxn(context.Context, string) (Transaction, error) {
	return f.tx, f.queryErr
}
func (f *managementFake) Report(context.Context, string, string) ([]Transaction, error) {
	return []Transaction{f.tx}, nil
}
func (f *managementFake) RefundTxn(context.Context, string) error {
	f.refunds++
	if f.failRefund {
		return errors.New("reply lost")
	}
	f.tx.Status = "Refunded"
	return nil
}
func (f *managementFake) Agreements(context.Context, uint64) ([]Agreement, error) {
	status := "Active"
	if f.canceled {
		status = "Canceled"
	}
	return []Agreement{{ID: "11", Status: status}}, nil
}
func (f *managementFake) CancelAgreement(context.Context, uint64, string) error {
	f.cancels++
	f.canceled = true
	return nil
}

func TestRefundIntentReplayDoesNotRepeatMoneyOperation(t *testing.T) {
	book, _ := OpenBook("")
	book.Open(Order{ID: 42, SteamID: 76561197960265729, Amount: 499, Currency: "USD"})
	book.Paid(42)
	f := &managementFake{tx: Transaction{OrderID: "42", TransID: "7", SteamID: "76561197960265729", Status: "Succeeded", Currency: "USD", Time: time.Now().UTC().Format(time.RFC3339), Items: []TransactionItem{{Amount: 459, VAT: 40}}}, failRefund: true}
	path := filepath.Join(t.TempDir(), "admin.json")
	a, err := OpenAdmin(path, f, book, 5147380, false)
	if err != nil {
		t.Fatal(err)
	}
	op := Operation{ID: "operation-00000001", Action: "refund", Order: "42", Actor: "76561197960265729", Reason: "Player requested refund", AppID: 5147380, Amount: 499, Currency: "USD"}
	got, err := a.Execute(context.Background(), op)
	if err != nil || got.State != "unknown" || f.refunds != 1 {
		t.Fatal(got, err, f.refunds)
	}
	a, err = OpenAdmin(path, f, book, 5147380, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Execute(context.Background(), op); err != nil || f.refunds != 1 {
		t.Fatal("replayed a refund", err)
	}
	f.tx.Status = "Refunded"
	if err = a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got = a.View().Operations[0]; got.State != "complete" || f.refunds != 1 {
		t.Fatal("did not reconcile", got)
	}
	if supporter, _ := book.IsSupporter(76561197960265729); supporter {
		t.Fatal("refunded credit retained")
	}
}

func TestRefundChecksRealmAmountAndAgreementCancellationIsIdempotent(t *testing.T) {
	book, _ := OpenBook("")
	f := &managementFake{tx: Transaction{OrderID: "42", TransID: "7", Status: "Succeeded", Currency: "USD", Items: []TransactionItem{{Amount: 499}}}}
	a, _ := OpenAdmin("", f, book, 5147380, true)
	op := Operation{ID: "operation-00000002", Action: "refund", Order: "42", Actor: "76561197960265729", Reason: "Requested", AppID: 5147380, Amount: 499, Currency: "USD", Sandbox: false}
	if _, err := a.Execute(context.Background(), op); err == nil {
		t.Fatal("crossed sandbox/live boundary")
	}
	op.Sandbox = true
	op.Amount = 100
	if got, err := a.Execute(context.Background(), op); err != nil || got.State != "rejected" || f.refunds != 0 {
		t.Fatal("amount change was not rejected", got, err)
	}
	cancel := Operation{ID: "operation-00000003", Action: "cancel_subscription", SteamID: "76561197960265729", Agreement: "11", Actor: op.Actor, Reason: "Requested", AppID: 5147380, Sandbox: true}
	if got, err := a.Execute(context.Background(), cancel); err != nil || got.State != "complete" {
		t.Fatal(got, err)
	}
	a.Execute(context.Background(), cancel)
	if f.cancels != 1 {
		t.Fatal("duplicate cancellation")
	}
}

func TestRefundPreflightFailureDoesNotBlockFreshReview(t *testing.T) {
	book, _ := OpenBook("")
	f := &managementFake{queryErr: errors.New("Steam unavailable"), tx: Transaction{OrderID: "42", TransID: "7", Status: "Succeeded", Currency: "USD", Items: []TransactionItem{{Amount: 499}}}}
	a, _ := OpenAdmin("", f, book, 5147380, false)
	op := Operation{ID: "preflight-failed-0001", Action: "refund", Order: "42", Actor: "76561197960265729", Reason: "Requested", AppID: 5147380, Amount: 499, Currency: "USD"}
	if got, err := a.Execute(context.Background(), op); err != nil || got.State != "rejected" || f.refunds != 0 {
		t.Fatal("preflight failure treated as a submitted refund", got, err, f.refunds)
	}
	f.queryErr = nil
	op.ID = "preflight-retry-00002"
	if got, err := a.Execute(context.Background(), op); err != nil || got.State != "complete" || f.refunds != 1 {
		t.Fatal("fresh review blocked after preflight failure", got, err, f.refunds)
	}
}
