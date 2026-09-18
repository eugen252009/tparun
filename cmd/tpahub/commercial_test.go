package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/billing"
	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/payments"
)

type testCommercialService struct {
	calls  int
	intent commerce.PurchaseRequest
}

var _ billing.Service = (*testCommercialService)(nil)

func (s *testCommercialService) StartPurchase(_ context.Context, r commerce.PurchaseRequest, _ bool) (commerce.Purchase, payments.CheckoutSession, error) {
	s.calls++
	s.intent = r
	return commerce.Purchase{ID: "external-intent", Status: "pending", Provider: "contract-system", BillingEnvironment: "test"}, payments.CheckoutSession{URL: "https://contract.invalid/intent"}, nil
}
func (s *testCommercialService) AcceptPayment(context.Context, commerce.PaymentEvent) error {
	return nil
}

func TestGenericHubPurchaseUsesReplaceableBillingService(t *testing.T) {
	s, _ := testServer(t)
	service := &testCommercialService{}
	s.commercial = service
	s.commercialResolved = true
	r := httptest.NewRequest("POST", "/v1/billing/purchases/nano", strings.NewReader(`{"business_use":true}`))
	r.Header.Set("Authorization", "Bearer tph_s_test")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != 200 || service.calls != 1 || service.intent.CustomerID != "a" || !strings.Contains(w.Body.String(), `"provider":"contract-system"`) {
		t.Fatalf("generic purchase=%d %s calls=%d", w.Code, w.Body.String(), service.calls)
	}
}

func TestBillingNoneCompositionSkipsHostedRequirements(t *testing.T) {
	s, _, payment := deliveryFixture(t, config.DeliveryMetricsOnly)
	s.cfg.BillingIntegration = config.BillingNone
	s.cfg.BillingProvider = "stripe"
	s.cfg.StripeTestSecret = "not-a-test-key"
	s.cfg.StripeWebhookSecret = ""
	s.cfg.CheckoutSuccessURL = "javascript:invalid"
	if err := s.composeCommercial(); err != nil {
		t.Fatal(err)
	}
	if s.commercial != nil || s.paymentWebhook != nil {
		t.Fatal("disabled billing constructed an integration")
	}
	if w := deliveryRequest(s, "GET", "package.deb", nil); w.Code != 200 {
		t.Fatalf("delivery=%d", w.Code)
	}
	version := databaseVersionReader(t, s)
	before := version()
	r := httptest.NewRequest("GET", "/account?billing=preview", nil)
	r = r.WithContext(context.WithValue(r.Context(), accountKey, "a"))
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "virtual usage preview") || version() != before || payment.calls.Load() != 0 {
		t.Fatal("disabled billing had effects")
	}
	s.cfg.BillingPurchasesEnabled = true
	if err := ValidateBillingConfiguration(s.cfg); err == nil {
		t.Fatal("billing none accepted purchases")
	}
}

func TestHostedReturnURLsAreAdapterConfiguration(t *testing.T) {
	s, _ := testServer(t)
	provider := &recordingPurchaseProvider{}
	s.purchaseProvider = provider
	s.cfg.BillingProvider = "stripe"
	s.cfg.BillingEnvironment = "test"
	s.cfg.BillingPurchasesEnabled = true
	s.cfg.BillingIntegration = config.BillingHosted
	s.cfg.BillingRateCard = "production-nano-v1"
	s.cfg.StripeTestSecret = "sk_test_example"
	s.cfg.StripeWebhookSecret = "whsec_example"
	s.cfg.CheckoutSuccessURL = "https://operator.invalid/success"
	s.cfg.CheckoutCancelURL = "https://operator.invalid/cancel"
	if _, _, err := s.startNanoPurchase(context.Background(), "a", false); err != nil {
		t.Fatal(err)
	}
	if len(provider.operations) != 1 || provider.operations[0].SuccessURL != s.cfg.CheckoutSuccessURL || provider.operations[0].CancelURL != s.cfg.CheckoutCancelURL {
		t.Fatalf("return URLs=%+v", provider.operations)
	}
	s.cfg.CheckoutSuccessURL = "javascript:alert(1)"
	if err := ValidateBillingConfiguration(s.cfg); err == nil {
		t.Fatal("invalid hosted return URL accepted")
	}
}

func TestCommercialDependencyBoundaries(t *testing.T) {
	// Package-graph assertions include transitive imports. The hosted service
	// itself depends on a generic payment port, not the Stripe adapter.
	for _, pkg := range []string{"internal/billing", "internal/hostedbilling", "internal/commerce", "internal/usage"} {
		cmd := exec.Command("go", "list", "-deps", "./"+pkg)
		cmd.Dir = filepath.Join("..", "..")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("graph: %v %s", err, out)
		}
		if strings.Contains(string(out), "github.com/eugen252009/tpahub/internal/payments/stripe") {
			t.Fatalf("%s imports Stripe", pkg)
		}
		if pkg == "internal/usage" && (strings.Contains(string(out), "tpahub/internal/billing") || strings.Contains(string(out), "tpahub/internal/payments")) {
			t.Fatal("measurement imports commercial policy")
		}
	}
	// main is intentionally one executable, so core-file references are checked
	// separately from the allowed hosted wiring in that same Go package.
	for _, path := range []string{"main.go", "delivery.go", "delivery_strict.go", "egress.go", "usage.go", "repository_policy.go", "purchase.go", "auth.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.ImportSpec:
				if strings.Contains(v.Path.Value, "stripe") {
					t.Errorf("core file %s imports Stripe", path)
				}
			case *ast.Ident:
				if strings.Contains(v.Name, "Stripe") {
					t.Errorf("core file %s references %s", path, v.Name)
				}
			}
			return true
		})
	}
}
