package hostedbilling

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

type testPayment struct {
	calls     int
	operation payments.Operation
}

func (p *testPayment) CreateCheckout(_ context.Context, op payments.Operation) (payments.CheckoutSession, error) {
	p.calls++
	p.operation = op
	return payments.CheckoutSession{Provider: "test-invoice", Environment: payments.EnvironmentTest, ProviderReference: "invoice-reference", URL: "https://invoice.invalid/intent"}, nil
}

func TestReplaceablePaymentAndLocalGrantAuthority(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateAccount(ctx, store.Account{ID: "customer", Email: "customer@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	payment := &testPayment{}
	offer := tpahubproduct.NanoOffer()
	service := &Service{Store: db, Payment: payment, Lock: &sync.Mutex{}, Now: func() time.Time { return now }, Provider: "test-invoice", Environment: payments.EnvironmentTest, SuccessURL: "https://local.invalid/success", CancelURL: "https://local.invalid/cancel"}
	service.Prepare = func(_ context.Context, r commerce.PurchaseRequest, _ bool) (PreparedPurchase, error) {
		if r.ProductID != offer.ProductID || r.OfferID != offer.ID {
			return PreparedPurchase{}, fmt.Errorf("unsupported offer")
		}
		p := commerce.Purchase{ID: "intent", AccountID: string(r.CustomerID), ProductID: string(offer.ProductID), PlanCode: offer.ID, OfferVersion: offer.Version, RateCardID: "test-card", RateCardMode: "test", TaxClassification: "saas-personal", Currency: offer.Currency, AmountMinor: offer.AmountMinor, Provider: service.Provider, BillingEnvironment: string(service.Environment), Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(1, 0, 0).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
		return PreparedPurchase{Purchase: p, Grants: offer.Grants, Description: offer.Description}, nil
	}
	request := commerce.PurchaseRequest{CustomerID: "customer", ProductID: offer.ProductID, OfferID: offer.ID, OfferVersion: offer.Version}
	purchase, checkout, err := service.StartPurchase(ctx, request, false)
	if err != nil || checkout.Provider != "test-invoice" {
		t.Fatalf("purchase=%+v %v", purchase, err)
	}
	if payment.operation.SuccessURL != service.SuccessURL || payment.operation.CancelURL != service.CancelURL || payment.operation.AmountMinor != purchase.AmountMinor {
		t.Fatalf("operation=%+v", payment.operation)
	}
	if _, err := db.GetGrant(ctx, "customer", offer.ProductID, "repository.publish", now); err == nil {
		t.Fatal("unpaid purchase activated grants")
	}
	event := commerce.PaymentEvent{ProviderEventID: "wrong", OperationID: purchase.ID, Type: commerce.PaymentSucceeded, ProviderReference: checkout.ProviderReference, AmountMinor: purchase.AmountMinor + 1, Currency: purchase.Currency, OccurredAt: now}
	if err := service.AcceptPayment(ctx, event); err == nil {
		t.Fatal("wrong amount accepted")
	}
	if _, err := db.GetGrant(ctx, "customer", offer.ProductID, "repository.publish", now); err == nil {
		t.Fatal("invalid event activated grants")
	}
	event.ProviderEventID = "paid"
	event.AmountMinor = purchase.AmountMinor
	if err := service.AcceptPayment(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := service.AcceptPayment(ctx, event); err != nil {
		t.Fatal(err)
	}
	// A subsequent local authorization read has no service or payment dependency.
	var grants commerce.GrantService = db
	grant, err := grants.GetGrant(ctx, "customer", offer.ProductID, "repository.publish", now)
	if err != nil || grant.Limits.StorageBytes <= 0 || payment.calls != 1 {
		t.Fatalf("local grant=%+v payment calls=%d error=%v", grant, payment.calls, err)
	}
	saved, err := db.Purchase(ctx, purchase.ID)
	if err != nil || saved.Status != "paid" {
		t.Fatalf("state=%+v %v", saved, err)
	}
}
