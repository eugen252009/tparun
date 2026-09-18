package main

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/eugen252009/tpahub/internal/billing"
	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/hostedbilling"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/store"
)

// These are defaults of the hosted adapter, not repository/client endpoints.
const hostedCheckoutReturnURL = "https://tpa.lupricht.net/"

func hostedCheckoutURLs(c config.Config) (string, string, error) {
	success, cancel := c.CheckoutSuccessURL, c.CheckoutCancelURL
	if success == "" {
		success = hostedCheckoutReturnURL
	}
	if cancel == "" {
		cancel = hostedCheckoutReturnURL
	}
	for _, raw := range []string{success, cancel} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
			return "", "", fmt.Errorf("invalid hosted checkout return URL")
		}
	}
	return success, cancel, nil
}

// composeCommercial is the sole production hosted wiring point. None means no
// service and no provider construction. In-process today; the generic service
// port does not prescribe a payment backend or future transport.
func (s *server) composeCommercial() error {
	if err := ValidateBillingConfiguration(s.cfg); err != nil {
		return err
	}
	mode, err := config.ResolveBillingMode(s.cfg)
	if err != nil {
		return err
	}
	s.commercial = nil
	if mode == config.BillingHosted && s.cfg.BillingPurchasesEnabled {
		if err = s.configureHostedPayment(); err != nil {
			return err
		}
		s.commercial = s.newHostedBilling()
	} else {
		s.purchaseProvider, s.paymentWebhook = nil, nil
	}
	s.commercialResolved = true
	return nil
}

func (s *server) billingService() billing.Service {
	if s.commercialResolved {
		return s.commercial
	}
	if s.commercial != nil {
		return s.commercial
	}
	// Compatibility for existing in-process callers/tests constructing a server
	// directly. Production resolves the integration before exposing routes.
	mode, err := config.ResolveBillingMode(s.cfg)
	if err != nil || mode != config.BillingHosted || !s.cfg.BillingPurchasesEnabled || ValidateBillingConfiguration(s.cfg) != nil {
		return nil
	}
	return s.newHostedBilling()
}
func (s *server) billingEnabled() bool {
	mode, err := config.ResolveBillingMode(s.cfg)
	return err == nil && mode != config.BillingNone
}

func (s *server) newHostedBilling() *hostedbilling.Service {
	success, cancel, _ := hostedCheckoutURLs(s.cfg) // validated by selected composition
	return &hostedbilling.Service{Store: s.db, Payment: s.purchaseProvider, Prepare: s.prepareHostedPurchase, Now: s.currentTime, Lock: &s.purchaseMu, Provider: s.cfg.BillingProvider, Environment: payments.Environment(s.cfg.BillingEnvironment), SuccessURL: success, CancelURL: cancel}
}

// Hosted product/rate-card policy stays on the commercial side of the port.
// Local grant checks in delivery/publication never invoke this policy.
func (s *server) prepareHostedPurchase(ctx context.Context, request commerce.PurchaseRequest, businessUse bool) (hostedbilling.PreparedPurchase, error) {
	now := s.currentTime().UTC()
	period, err := entitlementPeriod(now.Format("2006-01-02"), now)
	if err != nil {
		return hostedbilling.PreparedPurchase{}, fmt.Errorf("entitlement period unavailable")
	}
	offer, err := commerce.AdmitPurchase(ctx, s.commerceCatalog(), request)
	if err != nil {
		return hostedbilling.PreparedPurchase{}, err
	}
	card, err := rateCardByID(s.cfg.BillingRateCard)
	if err != nil || validateRateCard(card) != nil {
		return hostedbilling.PreparedPurchase{}, fmt.Errorf("Nano pricing unavailable")
	}
	classification := TaxSaaSPersonal
	if businessUse {
		classification = TaxSaaSBusiness
	}
	created := now.Format(time.RFC3339Nano)
	p := commerce.Purchase{ID: newID(), AccountID: string(request.CustomerID), ProductID: string(offer.ProductID), PlanCode: offer.ID, OfferVersion: offer.Version, RateCardID: card.ID, RateCardMode: string(card.Mode), TaxClassification: string(classification), Currency: card.Currency, CheckoutRequestVersion: "v2", AmountMinor: offer.AmountMinor, BillingEnvironment: s.cfg.BillingEnvironment, Provider: s.cfg.BillingProvider, Status: "pending", EntitlementPeriodStart: period.Start.Format(time.RFC3339Nano), EntitlementPeriodEnd: period.End.Format(time.RFC3339Nano), CreatedAt: created, UpdatedAt: created}
	return hostedbilling.PreparedPurchase{Purchase: p, Grants: offer.Grants, Description: offer.Description}, nil
}

// Compatibility entry point for explicit retries of already-persisted purchases.
func (s *server) createPurchaseCheckout(ctx context.Context, purchase store.BillingPurchase, description string) (CheckoutResult, error) {
	adapter := s.newHostedBilling()
	// Historical callers need not configure a fresh checkout offer. The persisted
	// purchase, not current pricing/configuration, supplies its provider identity.
	adapter.Provider = purchase.Provider
	adapter.Environment = payments.Environment(purchase.BillingEnvironment)
	return adapter.Checkout(ctx, purchase, description)
}
