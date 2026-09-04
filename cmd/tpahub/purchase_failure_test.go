package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type networkFailureTransport struct{}

func (networkFailureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network unavailable")
}

func TestStripeRetryableFailuresKeepPurchasePending(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport http.RoundTripper
	}{{"server-5xx", &checkoutTransport{status: 500}}, {"network", networkFailureTransport{}}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testServer(t)
			s.cfg.BillingPurchasesEnabled = true
			s.cfg.BillingProvider = "stripe"
			s.cfg.BillingEnvironment = "test"
			s.cfg.BillingRateCard = "production-nano-v1"
			s.cfg.StripeTestSecret = "sk_test_example"
			s.cfg.StripeWebhookSecret = "whsec_example"
			provider, _ := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
			provider.BaseURL = "https://stripe.test"
			provider.Client = &http.Client{Transport: tc.transport}
			s.purchaseProvider = provider
			purchase, _, err := s.startNanoPurchase(context.Background(), "a", false)
			if err == nil {
				t.Fatal("expected retryable provider failure")
			}
			saved, readErr := s.db.Purchase(context.Background(), purchase.ID)
			if readErr != nil || saved.Status != "pending" {
				t.Fatalf("purchase=%+v readErr=%v", saved, readErr)
			}
		})
	}
}

func TestStripeFourHundredTerminalizesPendingPurchase(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.BillingPurchasesEnabled = true
	s.cfg.BillingProvider = "stripe"
	s.cfg.BillingEnvironment = "test"
	s.cfg.BillingRateCard = "production-nano-v1"
	s.cfg.StripeTestSecret = "sk_test_example"
	s.cfg.StripeWebhookSecret = "whsec_example"
	transport := &checkoutTransport{status: 400}
	provider, _ := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
	provider.BaseURL = "https://stripe.test"
	provider.Client = &http.Client{Transport: transport}
	s.purchaseProvider = provider
	purchase, _, err := s.startNanoPurchase(context.Background(), "a", false)
	if err == nil {
		t.Fatal("expected provider rejection")
	}
	saved, readErr := s.db.Purchase(context.Background(), purchase.ID)
	if readErr != nil || saved.Status != "failed" {
		t.Fatalf("purchase=%+v readErr=%v", saved, readErr)
	}
}
