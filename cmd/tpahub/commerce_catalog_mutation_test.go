package main

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
)

type mutableQualificationCatalog struct {
	version        string
	price, widgets int64
}

func (c *mutableQualificationCatalog) Product(_ context.Context, id commerce.ProductID) (commerce.Product, error) {
	if id != "test-product" {
		return commerce.Product{}, commerce.ErrUnknownProduct
	}
	return commerce.Product{ID: id, Name: "Test widgets"}, nil
}
func (c *mutableQualificationCatalog) Offer(_ context.Context, product commerce.ProductID, offerID, version string) (commerce.Offer, error) {
	if product != "test-product" || offerID != "standard" || version != c.version {
		return commerce.Offer{}, commerce.ErrUnsupportedOfferVersion
	}
	return commerce.Offer{ID: offerID, Version: c.version, ProductID: product, Currency: "EUR", AmountMinor: c.price, Purchasable: true, Grants: []commerce.GrantTemplate{{CapabilityID: "feature.widgets", Period: commerce.PeriodContinuous, Limits: commerce.Limits{OperationCount: c.widgets, OperationMetric: "widgets"}}}}, nil
}

func TestCatalogMutationDoesNotReinterpretHistoricalPurchase(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"old", "new"} {
		if err = db.CreateAccount(ctx, store.Account{ID: id, Email: id + "@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{db: db}
	catalog := &mutableQualificationCatalog{version: "1", price: 700, widgets: 7}
	admit := func(id, account string) store.BillingPurchase {
		p, createErr := s.persistAdmittedPurchase(ctx, catalog, commerce.PurchaseRequest{CustomerID: commerce.CustomerID(account), ProductID: "test-product", OfferID: "standard", OfferVersion: catalog.version}, store.BillingPurchase{ID: id, AccountID: account, RateCardID: "rate-" + id, RateCardMode: "test", TaxClassification: "generic", BillingEnvironment: "test", Provider: "mock", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(1, 0, 0).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}, now.Format(time.RFC3339Nano))
		if createErr != nil {
			t.Fatal(createErr)
		}
		if createErr = db.UpdatePurchaseCheckout(ctx, p.ID, "ref-"+id, "", now.Format(time.RFC3339Nano)); createErr != nil {
			t.Fatal(createErr)
		}
		return p
	}
	old := admit("old-purchase", "old")
	catalog.version, catalog.price, catalog.widgets = "2", 990, 99
	old = finalizeQualificationPurchase(t, db, old, now)
	grant, err := db.GetGrant(ctx, "old", "test-product", "feature.widgets", now)
	if err != nil || grant.Limits.OperationCount != 7 || old.OfferVersion != "1" || old.AmountMinor != 700 {
		t.Fatalf("historical purchase changed: purchase=%+v grant=%+v err=%v", old, grant, err)
	}
	newPurchase := admit("new-purchase", "new")
	if newPurchase.OfferVersion != "2" || newPurchase.AmountMinor != 990 {
		t.Fatalf("new purchase did not use current catalog: %+v", newPurchase)
	}
	if err = db.ApplyProviderPaymentEvent(ctx, "mock", "test", commerce.PaymentEvent{ProviderEventID: "event-old-purchase", OperationID: old.ID, Type: commerce.PaymentSucceeded, ProviderReference: "ref-old-purchase", AmountMinor: old.AmountMinor, Currency: old.Currency, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	grant, err = db.GetGrant(ctx, "old", "test-product", "feature.widgets", now)
	if err != nil || grant.Limits.OperationCount != 7 {
		t.Fatalf("historical replay changed grant: %+v err=%v", grant, err)
	}
}

func finalizeQualificationPurchase(t *testing.T, db *store.Store, p store.BillingPurchase, now time.Time) store.BillingPurchase {
	t.Helper()
	if err := db.ApplyProviderPaymentEvent(context.Background(), "mock", "test", commerce.PaymentEvent{ProviderEventID: "event-" + p.ID, OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: "ref-" + p.ID, AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	return p
}
