package main

import (
	"context"
	"net/http"
	"testing"
)

func TestIdenticalFrozenCheckoutUsesStableIdentityAndRequest(t *testing.T) {
	s, _ := testServer(t)
	transport := &checkoutTransport{}
	provider, _ := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
	provider.BaseURL = "https://stripe.test"
	provider.Client = &http.Client{Transport: transport}
	purchase := FinalizedPurchase{PurchaseID: "p-exact", AccountID: "a", PlanCode: "nano", RateCardID: "production-nano-v1", RateCardMode: RateCardProduction, TaxClassification: TaxSaaSPersonal, Currency: "EUR", AmountMinor: 500, BillingEnvironment: BillingEnvironmentTest, IdempotencyKey: "tpahub:purchase:p-exact:stripe:test:checkout:v2"}
	if _, err := provider.CreateCheckout(context.Background(), purchase); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.CreateCheckout(context.Background(), purchase); err != nil {
		t.Fatal(err)
	}
	if len(transport.bodies) != 3 || transport.bodies[1] != transport.bodies[2] || transport.keys[1] != transport.keys[2] {
		t.Fatalf("retry was not identical: keys=%v bodies=%d", transport.keys, len(transport.bodies))
	}
}
