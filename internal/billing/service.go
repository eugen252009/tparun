// Package billing defines the optional commercial service boundary. It is not a
// meter, a payment provider, or a synchronous repository-authorization service.
package billing

import (
	"context"
	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/payments"
)

// Service owns commercial purchase orchestration. Existing generic intent,
// purchase and payment types are deliberately reused. Implementations may be
// in-process or, later, authenticated clients; no remote transport exists today.
// Absence is represented by no Service, never a dummy payment provider.
type Service interface {
	StartPurchase(context.Context, commerce.PurchaseRequest, bool) (commerce.Purchase, payments.CheckoutSession, error)
	// AcceptPayment accepts an already-authenticated event. It is NOT a public
	// webhook parser. The implementation binds the provider/environment authority
	// and must preserve durable correlation, validation and idempotent activation.
	AcceptPayment(context.Context, commerce.PaymentEvent) error
}
