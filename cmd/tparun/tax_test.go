package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/payments"
)

func TestPlanTaxClassifications(t *testing.T) {
	if planCatalog["nano"].TaxClassification != TaxSaaSPersonal || planCatalog["standard"].TaxClassification != TaxSaaSPersonal || planCatalog["pro"].TaxClassification != TaxSaaSBusiness {
		t.Fatalf("unexpected classifications: %+v", planCatalog)
	}
}
func TestCheckoutCarriesFrozenTaxClassificationWithoutManagedPaymentsDisable(t *testing.T) {
	s, _ := testServer(t)
	transport := &checkoutTransport{}
	p, _ := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
	p.BaseURL = "https://stripe.test"
	p.Client = &http.Client{Transport: transport}
	for _, tc := range []struct {
		class ProductTaxClassification
		code  string
	}{{TaxSaaSPersonal, "txcd_10103000"}, {TaxSaaSBusiness, "txcd_10103001"}} {
		_, e := p.CreateCheckout(context.Background(), payments.Operation{OperationID: "purchase-" + tc.code, IdempotencyKey: "key-" + tc.code, CustomerID: "a", ProductID: commerce.ProductID("neutral-product"), Description: "Neutral SaaS service", Currency: "EUR", AmountMinor: 500, TaxClass: payments.TaxClass(tc.class), SuccessURL: "https://example.invalid/success", CancelURL: "https://example.invalid/cancel", Environment: payments.EnvironmentTest})
		if e != nil {
			t.Fatal(e)
		}
		body := transport.bodies[len(transport.bodies)-1]
		if !strings.Contains(body, tc.code) || strings.Contains(body, "managed_payments%5Benabled%5D=false") {
			t.Fatalf("checkout body=%s", body)
		}
	}
}
func TestPurchaseTaxClassificationIsPersisted(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.BillingPurchasesEnabled = true
	s.cfg.BillingProvider = "stripe"
	s.cfg.BillingEnvironment = "test"
	s.cfg.BillingRateCard = "production-nano-v1"
	s.cfg.StripeTestSecret = "sk_test_example"
	s.cfg.StripeWebhookSecret = "whsec_example"
	p, _ := NewStripeBillingProvider(s.db, "sk_test_example", BillingEnvironmentTest)
	p.Client = &http.Client{Transport: &checkoutTransport{}}
	p.BaseURL = "https://stripe.test"
	s.purchaseProvider = p
	purchase, _, e := s.startNanoPurchase(context.Background(), "a", true)
	if e != nil {
		t.Fatal(e)
	}
	saved, e := s.db.Purchase(context.Background(), purchase.ID)
	if e != nil || saved.TaxClassification != string(TaxSaaSBusiness) {
		t.Fatalf("saved purchase=%+v err=%v", saved, e)
	}
}
