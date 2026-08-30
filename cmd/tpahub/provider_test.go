package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
)

type stripeTestTransport struct {
	requests int
	bodies   []string
}

func (t *stripeTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.requests++
	b, _ := io.ReadAll(r.Body)
	t.bodies = append(t.bodies, string(b))
	var body string
	if strings.HasSuffix(r.URL.Path, "/customers") {
		body = `{"id":"cus_test_1"}`
	} else {
		body = `{"id":"ii_test_1"}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}
func testFinalized() FinalizedBillingPeriod {
	return FinalizedBillingPeriod{BillingPreview: BillingPreview{AccountID: "a", PlanCode: "pro", Period: "2026-08", RateCardID: "mock-rate-card-v1", Currency: "EUR", LineItems: []BillingLineItem{{Kind: "base", Quantity: 1, UnitAmountMinor: 500, AmountMinor: 500, Currency: "EUR"}}, TotalAmountMinor: 500, IsVirtual: true}, RateCardMode: RateCardMock, FinalizedAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), IdempotencyKey: "tpahub:a:2026-08:final"}
}
func TestStripeTestAdapterConsumesFinalizedRatedResult(t *testing.T) {
	s, _ := testServer(t)
	transport := &stripeTestTransport{}
	provider, e := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
	if e != nil {
		t.Fatal(e)
	}
	provider.BaseURL = "https://stripe.test"
	provider.Client = &http.Client{Transport: transport}
	f := testFinalized()
	first, e := provider.Submit(context.Background(), f)
	if e != nil {
		t.Fatal(e)
	}
	second, e := provider.Submit(context.Background(), f)
	if e != nil {
		t.Fatal(e)
	}
	if first.ExternalReference != second.ExternalReference || transport.requests != 2 {
		t.Fatalf("submission not idempotent: first=%+v second=%+v requests=%d", first, second, transport.requests)
	}
	for _, body := range transport.bodies {
		if strings.Contains(body, "storage_high_water") || strings.Contains(body, "egress_bytes") {
			t.Fatalf("raw usage crossed provider boundary: %s", body)
		}
	}
}
func TestProviderSafetyGuards(t *testing.T) {
	if _, e := NewStripeBillingProvider(nil, "sk_live_example", BillingEnvironmentProduction); e == nil {
		t.Fatal("live Stripe provider accepted")
	}
	if e := ValidateBillingConfiguration(config.Config{BillingProvider: "stripe", BillingEnvironment: "production", StripeTestSecret: "sk_live_example"}); e == nil {
		t.Fatal("production Stripe configuration accepted")
	}
	f := testFinalized()
	f.RateCardMode = RateCardMock
	if e := validateProviderCompatibility(f.RateCardMode, BillingEnvironmentProduction); e == nil {
		t.Fatal("mock rate card crossed production boundary")
	}
	f.LineItems[0].AmountMinor = 499
	var pe providerError
	if e := validateFinalized(f); !errors.As(e, &pe) || pe.Code != "provider-invalid-request" {
		t.Fatalf("invalid finalization not rejected: %v", e)
	}
}
