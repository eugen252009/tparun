package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func TestNanoFloorChangesWithoutMutatingRawGrant(t *testing.T) {
	raw := commerce.Grant{CapabilityID: CapabilityRepositoryPublish, Limits: commerce.Limits{StorageBytes: 3, EgressBytes: 3}, Source: "test"}
	floorA := commerce.Grant{CapabilityID: CapabilityRepositoryPublish, OfferID: "nano-a", Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 5, EgressBytes: 5}, ValidFrom: time.Unix(1, 0)}
	floorB := floorA
	floorB.OfferID, floorB.Limits.StorageBytes, floorB.Limits.EgressBytes = "nano-b", 10, 10
	gotA, err := resolveNanoGrant(raw, nil, floorA)
	if err != nil || gotA.Limits.StorageBytes != 5 || gotA.Limits.EgressBytes != 5 {
		t.Fatalf("floor A=%+v err=%v", gotA, err)
	}
	gotB, err := resolveNanoGrant(raw, nil, floorB)
	if err != nil || gotB.Limits.StorageBytes != 10 || gotB.Limits.EgressBytes != 10 {
		t.Fatalf("floor B=%+v err=%v", gotB, err)
	}
	if raw.Limits.StorageBytes != 3 || raw.Limits.EgressBytes != 3 {
		t.Fatalf("raw grant mutated=%+v", raw)
	}
}

func TestNanoFloorLeavesPersistedHistoryUntouched(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	offer := tpahubproduct.NanoOffer()
	purchase := store.BillingPurchase{ID: "nano-floor-history", AccountID: "a", ProductID: string(offer.ProductID), PlanCode: offer.ID, OfferVersion: offer.Version, RateCardID: "nano-history", RateCardMode: "test", TaxClassification: "saas-personal", Currency: offer.Currency, AmountMinor: offer.AmountMinor, BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(1, 0, 0).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if _, err := s.db.CreatePendingPurchase(ctx, purchase, purchase.CreatedAt, offer.Grants...); err != nil {
		t.Fatal(err)
	}
	if err := s.db.PutGrant(ctx, commerce.Grant{CustomerID: "a", ProductID: tpahubproduct.ProductID, CapabilityID: CapabilityRepositoryPublish, OfferID: "historical", Enabled: true, ValidFrom: now.Add(-time.Hour), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 3, EgressBytes: 3}, Source: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	rawBefore, err := s.db.GetGrant(ctx, "a", tpahubproduct.ProductID, CapabilityRepositoryPublish, now)
	if err != nil {
		t.Fatal(err)
	}
	purchaseBefore, err := s.db.Purchase(ctx, purchase.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := commerce.Grant{CapabilityID: CapabilityRepositoryPublish, Limits: commerce.Limits{StorageBytes: 3, EgressBytes: 3}}
	for _, floor := range []int64{5, 10, 120} {
		if _, err := resolveNanoGrant(raw, nil, commerce.Grant{CapabilityID: raw.CapabilityID, Limits: commerce.Limits{StorageBytes: floor, EgressBytes: floor}}); err != nil {
			t.Fatal(err)
		}
	}
	rawAfter, err := s.db.GetGrant(ctx, "a", tpahubproduct.ProductID, CapabilityRepositoryPublish, now)
	if err != nil {
		t.Fatal(err)
	}
	purchaseAfter, err := s.db.Purchase(ctx, purchase.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rawBefore.Limits != rawAfter.Limits || purchaseBefore != purchaseAfter {
		t.Fatalf("floor resolution mutated persisted history: before=%+v/%+v after=%+v/%+v", rawBefore.Limits, purchaseBefore, rawAfter.Limits, purchaseAfter)
	}
}

func TestNanoFloorPreservesHigherAndRejectsInvalid(t *testing.T) {
	floor := commerce.Grant{CapabilityID: CapabilityRepositoryDownload, Limits: commerce.Limits{StorageBytes: 5, EgressBytes: 10}}
	higher := commerce.Grant{CapabilityID: floor.CapabilityID, Limits: commerce.Limits{StorageBytes: 100, EgressBytes: 100}}
	got, err := resolveNanoGrant(higher, nil, floor)
	if err != nil || got.Limits != higher.Limits {
		t.Fatalf("higher grant changed=%+v err=%v", got, err)
	}
	invalid := higher
	invalid.Limits.EgressBytes = -1
	got, err = resolveNanoGrant(invalid, nil, floor)
	if err != nil || got.Limits.EgressBytes != -1 {
		t.Fatalf("negative grant was not preserved for fail-closed validation: %+v err=%v", got, err)
	}
	if _, err = resolveNanoGrant(commerce.Grant{}, errors.New("Nano policy unavailable"), floor); err == nil {
		t.Fatal("policy failure was softened")
	}
}
