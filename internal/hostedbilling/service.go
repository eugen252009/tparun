// Package hostedbilling is the current co-located commercial service adapter.
// It owns purchase orchestration; payment-provider SDK/API details stay behind
// payments.Provider. Local durable purchase/grant facts remain in the Hub store.
package hostedbilling

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eugen252009/tpahub/internal/billing"
	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

type PreparedPurchase struct {
	Purchase    commerce.Purchase
	Grants      []commerce.GrantTemplate
	Description string
}

// Policy is hosted commercial policy, not operational GrantService. It supplies
// fresh offer terms; retries always use the already-persisted purchase snapshot.
type Policy func(context.Context, commerce.PurchaseRequest, bool) (PreparedPurchase, error)

type Service struct {
	Store                 *store.Store
	Payment               payments.Provider
	Prepare               Policy
	Now                   func() time.Time
	Lock                  sync.Locker
	Provider              string
	Environment           payments.Environment
	SuccessURL, CancelURL string
}

var _ billing.Service = (*Service)(nil)

func (s *Service) StartPurchase(ctx context.Context, request commerce.PurchaseRequest, businessUse bool) (commerce.Purchase, payments.CheckoutSession, error) {
	s.Lock.Lock()
	defer s.Lock.Unlock()
	prepared, err := s.Prepare(ctx, request, businessUse)
	if err != nil {
		return commerce.Purchase{}, payments.CheckoutSession{}, err
	}
	p := prepared.Purchase
	if p.Provider != s.Provider || p.BillingEnvironment != string(s.Environment) {
		return commerce.Purchase{}, payments.CheckoutSession{}, fmt.Errorf("commercial provider policy mismatch")
	}
	p, err = s.Store.CreatePendingPurchase(ctx, p, p.CreatedAt, prepared.Grants...)
	if err != nil {
		return commerce.Purchase{}, payments.CheckoutSession{}, err
	}
	if p.ProviderReference != "" {
		return p, payments.CheckoutSession{Provider: p.Provider, Environment: payments.Environment(p.BillingEnvironment), ProviderReference: p.ProviderReference, URL: p.CheckoutURL}, nil
	}
	checkout, err := s.Checkout(ctx, p, prepared.Description)
	if err != nil {
		var pe payments.Error
		if errors.As(err, &pe) && pe.Code == "provider-rejected-4xx" {
			_ = s.Store.FailPendingPurchase(ctx, p.ID, prepared.Purchase.CreatedAt)
		}
		return p, payments.CheckoutSession{}, err
	}
	p.ProviderReference = checkout.ProviderReference
	return p, checkout, nil
}

// Checkout uses persisted pricing, provider, environment and idempotency identity.
// The historic Stripe test key remains byte-for-byte identical for old purchases.
func (s *Service) Checkout(ctx context.Context, p commerce.Purchase, description string) (payments.CheckoutSession, error) {
	if s.Payment == nil {
		return payments.CheckoutSession{}, fmt.Errorf("payment provider unavailable")
	}
	if p.Provider != s.Provider || p.BillingEnvironment != string(s.Environment) {
		return payments.CheckoutSession{}, fmt.Errorf("purchase provider mismatch")
	}
	key := "commerce:" + p.ProductID + ":purchase:" + p.ID + ":" + p.Provider + ":" + p.BillingEnvironment + ":checkout:v2"
	if p.ProductID == string(tpahubproduct.ProductID) {
		key = "tpahub:purchase:" + p.ID + ":" + p.Provider + ":" + p.BillingEnvironment + ":checkout:v2"
	}
	checkout, err := s.Payment.CreateCheckout(ctx, payments.Operation{OperationID: p.ID, IdempotencyKey: key, CustomerID: commerce.CustomerID(p.AccountID), ProductID: commerce.ProductID(p.ProductID), Description: description, CustomerDescription: "TPA.run account " + p.AccountID, CustomerMetadata: map[string]string{"tpahub_account_id": p.AccountID}, Currency: p.Currency, AmountMinor: p.AmountMinor, TaxClass: payments.TaxClass(p.TaxClassification), Metadata: map[string]string{"tpahub_purchase_id": p.ID}, SuccessURL: s.SuccessURL, CancelURL: s.CancelURL, Environment: payments.Environment(p.BillingEnvironment)})
	if err != nil {
		return payments.CheckoutSession{}, err
	}
	if err = s.Store.UpdatePurchaseCheckout(ctx, p.ID, checkout.ProviderReference, checkout.URL, s.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return payments.CheckoutSession{}, err
	}
	return checkout, nil
}

func (s *Service) AcceptPayment(ctx context.Context, event commerce.PaymentEvent) error {
	if event.Type == commerce.PaymentIgnored {
		return nil
	}
	// Preserve the production transaction: do not split verification and grant
	// activation across a remote check, a generic event sink, or separate writes.
	return s.Store.ApplyProviderPaymentEvent(ctx, s.Provider, string(s.Environment), event)
}
