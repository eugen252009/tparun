package commerce

import (
	"context"
	"testing"
	"time"
)

type paymentRepositoryStub struct {
	purchase   PurchasePaymentState
	transition PaymentTransition
	applied    int
}

func (r *paymentRepositoryStub) PurchasePaymentState(context.Context, string) (PurchasePaymentState, error) {
	return r.purchase, nil
}
func (r *paymentRepositoryStub) ApplyPaymentTransition(_ context.Context, transition PaymentTransition) error {
	r.transition = transition
	r.applied++
	return nil
}

func TestPaymentServiceValidatesAndNormalizesTransition(t *testing.T) {
	repository := &paymentRepositoryStub{purchase: PurchasePaymentState{PurchaseID: "p", CustomerID: "customer", ProductID: "test-product", Provider: "stripe", Environment: "test", ProviderReference: "cs_1", AmountMinor: 725, Currency: "EUR"}}
	service := PaymentService{Repository: repository}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	event := PaymentEvent{ProviderEventID: "evt_1", OperationID: "p", Type: PaymentSucceeded, ProviderReference: "cs_1", AmountMinor: 725, Currency: "EUR", OccurredAt: now}
	if err := service.Process(context.Background(), "stripe", "test", event, now, now.AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if repository.applied != 1 || repository.transition.Kind != "success" || repository.transition.Event.OperationID != "p" {
		t.Fatalf("transition=%+v", repository.transition)
	}
	event.AmountMinor++
	if err := service.Process(context.Background(), "stripe", "test", event, now, now.AddDate(1, 0, 0)); err == nil || repository.applied != 1 {
		t.Fatalf("invalid amount applied: err=%v count=%d", err, repository.applied)
	}
}

func TestGrantHalfOpenValidityAndPeriods(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := from.AddDate(1, 0, 0)
	grant := Grant{Enabled: true, ValidFrom: from, ValidUntil: &until, Period: PeriodAnnual}
	if grant.ActiveAt(from.Add(-time.Nanosecond)) || !grant.ActiveAt(from) || !grant.ActiveAt(until.Add(-time.Nanosecond)) || grant.ActiveAt(until) {
		t.Fatal("grant validity is not [valid_from, valid_until)")
	}
	start, end, err := grant.PeriodAt(from)
	if err != nil || !start.Equal(from) || !end.Equal(until) {
		t.Fatalf("period=%s/%s err=%v", start, end, err)
	}
}
