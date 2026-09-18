package main

import (
	"github.com/eugen252009/tpahub/internal/commerce"
	"io"
	"net/http"
)

// Stripe transport authentication is confined to the hosted edge. The service
// receives only a verified generic event, never an unsigned HTTP payload.
func (s *server) stripeWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	service := s.billingService()
	if service == nil || !s.cfg.BillingPurchasesEnabled || s.cfg.BillingProvider != "stripe" || s.cfg.BillingEnvironment != string(BillingEnvironmentTest) || s.cfg.StripeWebhookSecret == "" {
		bad(w, http.StatusNotFound, "webhook unavailable")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		bad(w, http.StatusRequestEntityTooLarge, "webhook body too large")
		return
	}
	if s.paymentWebhook == nil {
		bad(w, http.StatusNotFound, "webhook unavailable")
		return
	}
	paymentEvent, err := s.paymentWebhook.ParseWebhook(body, r.Header.Get("Stripe-Signature"), s.cfg.StripeWebhookSecret, s.currentTime())
	if err != nil {
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	if paymentEvent.Type == commerce.PaymentIgnored {
		w.WriteHeader(http.StatusOK)
		return
	}
	// Correlation, recovery, authority validation, and provider evidence are
	// applied by the store in one transaction. Provider event times remain
	// evidence only; local purchased terms remain authoritative.
	if err = service.AcceptPayment(r.Context(), paymentEvent); err != nil {
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}
