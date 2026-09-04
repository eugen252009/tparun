package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

type Environment string

const (
	EnvironmentMock       Environment = "mock"
	EnvironmentTest       Environment = "test"
	EnvironmentProduction Environment = "production"
)

type TaxClass string

type Operation struct {
	OperationID         string
	IdempotencyKey      string
	CustomerID          commerce.CustomerID
	ProductID           commerce.ProductID
	Description         string
	CustomerDescription string
	CustomerMetadata    map[string]string
	Currency            string
	AmountMinor         int64
	TaxClass            TaxClass
	Metadata            map[string]string
	SuccessURL          string
	CancelURL           string
	Environment         Environment
}

type CheckoutSession struct {
	Provider          string
	Environment       Environment
	ProviderReference string
	URL               string
	ExpiresAt         *time.Time
}

type Provider interface {
	CreateCheckout(context.Context, Operation) (CheckoutSession, error)
}
type WebhookParser interface {
	ParseWebhook(body []byte, signature, secret string, now time.Time) (commerce.PaymentEvent, error)
}

var ErrCustomerMappingNotFound = errors.New("payment-customer-mapping-not-found")

type CustomerMapping struct {
	CustomerID                commerce.CustomerID
	Provider, Environment     string
	ProviderCustomerReference string
	CreatedAt                 time.Time
}

type CustomerMappingStore interface {
	GetPaymentCustomerMapping(context.Context, commerce.CustomerID, string, Environment) (CustomerMapping, error)
	SavePaymentCustomerMapping(context.Context, CustomerMapping) error
}

type Error struct {
	Code  string
	Cause error
}

func (e Error) Error() string {
	if e.Cause == nil {
		return e.Code
	}
	return e.Code + ": " + e.Cause.Error()
}
func (e Error) Unwrap() error             { return e.Cause }
func NewError(code, message string) error { return Error{Code: code, Cause: fmt.Errorf("%s", message)} }
