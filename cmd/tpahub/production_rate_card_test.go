package main

import (
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
)

func productionNanoSnapshot(egress int64) BillingUsageSnapshot {
	return BillingUsageSnapshot{AccountID: "nano-account", PlanCode: "nano", Period: "2026-09-15/2027-09-15", EgressBytes: egress}
}

func TestProductionNanoRateCardIsImmutableAndFixed(t *testing.T) {
	card, err := rateCardByID("production-nano-v1")
	if err != nil {
		t.Fatal(err)
	}
	if card.Mode != RateCardProduction || card.PlanCode != "nano" || card.Currency != "EUR" || card.BillingCadence != CadenceAnnual || card.FixedPlanAmountsMinor["nano"] != 500 {
		t.Fatalf("card=%+v", card)
	}
	for _, usage := range []int64{0, 1_000_000_000, 6_000_000_000, 11_900_000_000, 12_000_000_000, 13_000_000_000} {
		result, err := rateBillingWithRateCard(productionNanoSnapshot(usage), card)
		if err != nil || result.TotalAmountMinor != 500 || len(result.LineItems) != 1 || result.LineItems[0].AmountMinor != 500 {
			t.Fatalf("usage=%d result=%+v err=%v", usage, result, err)
		}
	}
	card.FixedPlanAmountsMinor["nano"] = 999
	fresh, _ := rateCardByID("production-nano-v1")
	if fresh.FixedPlanAmountsMinor["nano"] != 500 {
		t.Fatal("rate card identity was mutable")
	}
}

func TestProductionNanoFinalizationAndStripeTestCompatibility(t *testing.T) {
	card, _ := rateCardByID("production-nano-v1")
	finalized, err := finalizeBillingWithRateCard(productionNanoSnapshot(12_000_000_000), card, time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err = validateProviderCompatibility(finalized.RateCardMode, BillingEnvironmentTest); err != nil {
		t.Fatal(err)
	}
	if err = validateFinalized(finalized); err != nil {
		t.Fatal(err)
	}
	if finalized.TotalAmountMinor != 500 || finalized.Currency != "EUR" || finalized.RateCardID != "production-nano-v1" {
		t.Fatalf("finalized=%+v", finalized)
	}
	if err = ValidateBillingConfiguration(config.Config{BillingProvider: "stripe", BillingEnvironment: "test", BillingRateCard: "production-nano-v1", StripeTestSecret: "sk_test_example"}); err != nil {
		t.Fatal(err)
	}
	if err = ValidateBillingConfiguration(config.Config{BillingProvider: "mock", BillingEnvironment: "mock", BillingRateCard: "production-nano-v1"}); err == nil {
		t.Fatal("production card accepted by mock environment")
	}
}
