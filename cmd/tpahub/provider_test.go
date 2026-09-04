package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/payments"
)

type stripeTestTransport struct {
	requests     int
	bodies, keys []string
}

func (t *stripeTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.requests++
	body, _ := io.ReadAll(r.Body)
	t.bodies = append(t.bodies, string(body))
	t.keys = append(t.keys, r.Header.Get("Idempotency-Key"))
	response := `{"id":"cus_test_1"}`
	if strings.HasSuffix(r.URL.Path, "/checkout/sessions") {
		response = `{"id":"cs_test_1","url":"https://checkout.stripe.test/1"}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
}
func testFinalized() FinalizedBillingPeriod {
	return FinalizedBillingPeriod{BillingPreview: BillingPreview{AccountID: "a", PlanCode: "pro", Period: "2026-08", RateCardID: "mock-rate-card-v1", Currency: "EUR", LineItems: []BillingLineItem{{Kind: "base", Quantity: 1, UnitAmountMinor: 500, AmountMinor: 500, Currency: "EUR"}}, TotalAmountMinor: 500, IsVirtual: true}, RateCardMode: RateCardMock, FinalizedAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), IdempotencyKey: "commerce:tpa-hub:a:2026-08:final"}
}
func testPaymentOperation(id string, tax payments.TaxClass) payments.Operation {
	return payments.Operation{OperationID: id, IdempotencyKey: "commerce:test-product:purchase:" + id + ":stripe:test:checkout:v2", CustomerID: "a", ProductID: commerce.ProductID("test-product"), Description: "Test execution package", CustomerDescription: "Test customer a", CustomerMetadata: map[string]string{"commerce_customer_id": "a"}, Currency: "EUR", AmountMinor: 500, TaxClass: tax, Metadata: map[string]string{"commerce_operation_id": id, "commerce_product_id": "test-product"}, SuccessURL: "https://example.invalid/success", CancelURL: "https://example.invalid/cancel", Environment: payments.EnvironmentTest}
}
func TestStripeAdapterConsumesProviderNeutralOperation(t *testing.T) {
	s, _ := testServer(t)
	transport := &stripeTestTransport{}
	provider, err := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
	if err != nil {
		t.Fatal(err)
	}
	provider.BaseURL = "https://stripe.test"
	provider.Client = &http.Client{Transport: transport}
	operation := testPaymentOperation("operation-neutral", "saas-personal")
	first, err := provider.CreateCheckout(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.CreateCheckout(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderReference != second.ProviderReference || transport.requests != 3 || transport.keys[1] != transport.keys[2] {
		t.Fatalf("checkout not idempotent: first=%+v second=%+v requests=%d keys=%v", first, second, transport.requests, transport.keys)
	}
	for _, body := range transport.bodies {
		if strings.Contains(body, "nano") || strings.Contains(body, "production-nano-v1") || strings.Contains(body, "storage_high_water") || strings.Contains(body, "egress_bytes") {
			t.Fatalf("TPA semantics crossed provider boundary: %s", body)
		}
	}
}
func TestProviderSafetyGuards(t *testing.T) {
	if _, err := NewStripeBillingProvider(nil, "sk_live_example", BillingEnvironmentProduction); err == nil {
		t.Fatal("live Stripe provider accepted")
	}
	if err := ValidateBillingConfiguration(config.Config{BillingProvider: "stripe", BillingEnvironment: "production", StripeTestSecret: "sk_live_example"}); err == nil {
		t.Fatal("production Stripe configuration accepted")
	}
	finalized := testFinalized()
	finalized.RateCardMode = RateCardMock
	if err := validateProviderCompatibility(finalized.RateCardMode, BillingEnvironmentProduction); err == nil {
		t.Fatal("mock rate card crossed production boundary")
	}
	finalized.LineItems[0].AmountMinor = 499
	var providerErr providerError
	if err := validateFinalized(finalized); !errors.As(err, &providerErr) || providerErr.Code != "provider-invalid-request" {
		t.Fatalf("invalid finalization not rejected: %v", err)
	}
}
