package main

import (
	"context"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

type qualificationCatalog struct{ productionCommerceCatalog }

func (qualificationCatalog) Product(_ context.Context, id commerce.ProductID) (commerce.Product, error) {
	if id == "test-product" {
		return commerce.Product{ID: id, Name: "Test widgets"}, nil
	}
	return productionCommerceCatalog{}.Product(context.Background(), id)
}

func (qualificationCatalog) Offer(_ context.Context, product commerce.ProductID, offerID, version string) (commerce.Offer, error) {
	if product == "test-product" && offerID == "standard" && version == "1" {
		return commerce.Offer{ID: offerID, Version: version, ProductID: product, Currency: "EUR", AmountMinor: 700, Purchasable: true, Grants: []commerce.GrantTemplate{{CapabilityID: "feature.widgets", Period: commerce.PeriodContinuous, Limits: commerce.Limits{OperationCount: 7, OperationMetric: "widgets"}}}}, nil
	}
	return productionCommerceCatalog{}.Offer(context.Background(), product, offerID, version)
}

func TestMultiProductCommerceQualificationUsesApplicationAdmission(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err = db.CreateAccount(ctx, store.Account{ID: "multi", Email: "multi@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	s := &server{db: db}
	catalog := qualificationCatalog{}
	makePurchase := func(id string, request commerce.PurchaseRequest, amount int64) store.BillingPurchase {
		purchase, createErr := s.persistAdmittedPurchase(ctx, catalog, request, store.BillingPurchase{ID: id, RateCardID: "rate-" + id, RateCardMode: "test", TaxClassification: "generic", BillingEnvironment: "test", Provider: "mock", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(1, 0, 0).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}, now.Format(time.RFC3339Nano))
		if createErr != nil {
			t.Fatal(createErr)
		}
		if amount != purchase.AmountMinor {
			t.Fatalf("catalog amount was not authoritative: got %d want %d", purchase.AmountMinor, amount)
		}
		if createErr = db.UpdatePurchaseCheckout(ctx, purchase.ID, "ref-"+id, "", now.Format(time.RFC3339Nano)); createErr != nil {
			t.Fatal(createErr)
		}
		return purchase
	}
	tpa := makePurchase("tpa", commerce.PurchaseRequest{CustomerID: "multi", ProductID: tpahubproduct.ProductID, OfferID: "nano", OfferVersion: tpahubproduct.NanoOfferVersion}, tpahubproduct.NanoPriceMinor)
	productB := makePurchase("product-b", commerce.PurchaseRequest{CustomerID: "multi", ProductID: "test-product", OfferID: "standard", OfferVersion: "1"}, 700)
	pay := func(p store.BillingPurchase, eventID string) {
		if err = db.ApplyProviderPaymentEvent(ctx, "mock", "test", commerce.PaymentEvent{ProviderEventID: eventID, OperationID: p.ID, Type: commerce.PaymentSucceeded, ProviderReference: "ref-" + p.ID, AmountMinor: p.AmountMinor, Currency: p.Currency, OccurredAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	pay(tpa, "event-tpa")
	tpaGrant, err := db.GetGrant(ctx, "multi", tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryPublish, now)
	if err != nil {
		t.Fatal(err)
	}
	pay(productB, "event-product-b")
	productBGrant, err := db.GetGrant(ctx, "multi", "test-product", "feature.widgets", now)
	if err != nil {
		t.Fatal(err)
	}
	beforeReplay := productBGrant.Limits.OperationCount
	if err = db.ApplyProviderPaymentEvent(ctx, "mock", "test", commerce.PaymentEvent{ProviderEventID: "event-product-b", OperationID: productB.ID, Type: commerce.PaymentSucceeded, ProviderReference: "ref-" + productB.ID, AmountMinor: productB.AmountMinor, Currency: productB.Currency, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	afterReplayGrant, err := db.GetGrant(ctx, "multi", "test-product", "feature.widgets", now)
	if err != nil || afterReplayGrant.Limits.OperationCount != beforeReplay {
		t.Fatalf("Product B replay changed grant: before=%d after=%d err=%v", beforeReplay, afterReplayGrant.Limits.OperationCount, err)
	}
	afterTPA, err := db.GetGrant(ctx, "multi", tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryPublish, now)
	if tpaGrant.ProductID != tpahubproduct.ProductID || productBGrant.ProductID != commerce.ProductID("test-product") || productBGrant.Limits.StorageBytes != 0 || tpaGrant.Limits.StorageBytes != tpahubproduct.NanoStorageBytes || afterTPA.Limits.StorageBytes != tpaGrant.Limits.StorageBytes {
		t.Fatalf("cross-product grant state: before=%+v after=%+v productB=%+v err=%v", tpaGrant, afterTPA, productBGrant, err)
	}
	if status, err := db.Purchase(ctx, tpa.ID); err != nil || status.Status != "paid" {
		t.Fatalf("TPA purchase status=%s err=%v", status.Status, err)
	}
	if status, err := db.Purchase(ctx, productB.ID); err != nil || status.Status != "paid" {
		t.Fatalf("Product B purchase status=%s err=%v", status.Status, err)
	}
}
