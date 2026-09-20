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
	operation := testPaymentOperation("p-exact", "saas-personal")
	if _, err := provider.CreateCheckout(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.CreateCheckout(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	if len(transport.bodies) != 3 || transport.bodies[1] != transport.bodies[2] || transport.keys[1] != transport.keys[2] {
		t.Fatalf("retry was not identical: keys=%v bodies=%d", transport.keys, len(transport.bodies))
	}
}
