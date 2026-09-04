package main

import (
	"fmt"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	stripepayments "github.com/eugen252009/tpahub/internal/payments/stripe"
	"github.com/eugen252009/tpahub/internal/store"
)

// stripeProvider is composition-root compatibility wiring. Product and commerce
// code depend only on payment ports; the legacy metadata fallback keeps already
// created Checkout v2 sessions processable during migration.
type stripeProvider struct{ *stripepayments.Adapter }

func (p *stripeProvider) ParseWebhook(body []byte, signature, secret string, now time.Time) (commerce.PaymentEvent, error) {
	event, err := p.Adapter.ParseWebhook(body, signature, secret, now)
	if err != nil {
		return commerce.PaymentEvent{}, err
	}
	if event.OperationID == "" {
		event.OperationID = event.Metadata["tpahub_purchase_id"]
	}
	if event.OperationID == "" {
		return commerce.PaymentEvent{}, fmt.Errorf("purchase reference missing")
	}
	return event, nil
}

func NewStripeBillingProvider(st *store.Store, secret string, environment BillingEnvironment) (*stripeProvider, error) {
	adapter, err := stripepayments.New(st, secret, environment)
	if err != nil {
		return nil, err
	}
	return &stripeProvider{Adapter: adapter}, nil
}

func verifyStripeSignature(header string, body []byte, secret string, now time.Time) error {
	return stripepayments.VerifySignature(header, body, secret, now)
}
