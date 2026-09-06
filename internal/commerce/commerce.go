package commerce

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type CustomerID string
type ProductID string
type CapabilityID string
type MetricID string

// Product is the commerce identity of a sellable product.  Product policy
// supplies the meaning of its capabilities; Commerce only records the
// identity and the facts attached to it.
type Product struct {
	ID          ProductID
	Name        string
	Description string
}

// Purchase is the canonical commercial fact. Immutable terms are retained
// alongside lifecycle fields; Store aliases this type only for persistence.
type Purchase struct {
	ID, AccountID, ProductID, PlanCode, OfferVersion, RateCardID, RateCardMode, TaxClassification, Currency, BillingEnvironment, Provider, Status, EntitlementPeriodStart, EntitlementPeriodEnd, ProviderReference, CheckoutURL, CheckoutRequestVersion, CreatedAt, UpdatedAt, PaidAt, FailedAt, ExpiredAt, IntentState, ProviderState, ReconciliationReason string
	AmountMinor                                                                                                                                                                                                                                                                                                                                              int64
}

// ProductCatalog resolves products and offers for checkout admission.  It is
// a policy boundary: the generic Commerce and Store packages never implement
// product-specific capability rules.
type ProductCatalog interface {
	Product(context.Context, ProductID) (Product, error)
	Offer(context.Context, ProductID, string, string) (Offer, error)
}

// PurchaseRequest contains only caller intent. Product policy supplies the
// offer and grant snapshot; callers cannot submit grant definitions directly.
type PurchaseRequest struct {
	CustomerID   CustomerID
	ProductID    ProductID
	OfferID      string
	OfferVersion string
}

// AdmitPurchase is the generic admission boundary. Persistence happens only
// after the catalog has established product/offer/version ownership.
func AdmitPurchase(ctx context.Context, catalog ProductCatalog, request PurchaseRequest) (Offer, error) {
	if catalog == nil {
		return Offer{}, ErrUnknownProduct
	}
	if request.ProductID == "" {
		return Offer{}, ErrUnknownProduct
	}
	if _, err := catalog.Product(ctx, request.ProductID); err != nil {
		return Offer{}, fmt.Errorf("%w: %v", ErrUnknownProduct, err)
	}
	if request.OfferID == "" {
		return Offer{}, ErrUnknownOffer
	}
	offer, err := catalog.Offer(ctx, request.ProductID, request.OfferID, request.OfferVersion)
	if err != nil {
		return Offer{}, err
	}
	if offer.ProductID != request.ProductID {
		return Offer{}, ErrOfferProductMismatch
	}
	if offer.ID != request.OfferID {
		return Offer{}, ErrUnknownOffer
	}
	if request.OfferVersion != "" && offer.Version != request.OfferVersion {
		return Offer{}, ErrUnsupportedOfferVersion
	}
	if !offer.Purchasable {
		return Offer{}, ErrOfferNotPurchasable
	}
	return offer, nil
}

type PeriodCadence string

const (
	PeriodAnnual     PeriodCadence = "annual"
	PeriodMonthly    PeriodCadence = "monthly"
	PeriodContinuous PeriodCadence = "continuous"
)

var (
	ErrUnknownProduct           = errors.New("unknown-product")
	ErrUnknownOffer             = errors.New("unknown-offer")
	ErrOfferProductMismatch     = errors.New("offer-product-mismatch")
	ErrUnsupportedOfferVersion  = errors.New("unsupported-offer-version")
	ErrOfferNotPurchasable      = errors.New("offer-not-purchasable")
	ErrPurchaseConflict         = errors.New("purchase-conflict")
	ErrGrantNotFound            = errors.New("grant-not-found")
	ErrGrantDisabled            = errors.New("grant-disabled")
	ErrGrantInactive            = errors.New("grant-inactive")
	ErrIncompatibleGrantMetrics = errors.New("incompatible-grant-operation-metrics")
)

type Limits struct {
	StorageBytes    int64
	EgressBytes     int64
	OperationMetric MetricID
	OperationCount  int64
}

type GrantTemplate struct {
	CapabilityID CapabilityID
	Period       PeriodCadence
	Limits       Limits
}
type Offer struct {
	ID          string
	Version     string
	ProductID   ProductID
	Cadence     PeriodCadence
	Description string
	Currency    string
	AmountMinor int64
	TaxClass    string
	Purchasable bool
	Grants      []GrantTemplate
}

type Grant struct {
	CustomerID   CustomerID
	ProductID    ProductID
	CapabilityID CapabilityID
	OfferID      string
	Enabled      bool
	ValidFrom    time.Time
	ValidUntil   *time.Time
	Period       PeriodCadence
	Limits       Limits
	Source       string
	PurchaseID   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// Effective is non-nil only when Limits combine multiple authoritative rows.
	// Single-source commercial metadata is then held exclusively by Contributors.
	Effective *EffectiveGrant
}

func (g Grant) ActiveAt(now time.Time) bool {
	if g.Effective != nil {
		for _, contributor := range g.Effective.Contributors {
			if contributor.ActiveAt(now) {
				return true
			}
		}
		return false
	}
	now = now.UTC()
	if !g.Enabled || now.Before(g.ValidFrom.UTC()) {
		return false
	}
	return g.ValidUntil == nil || now.Before(g.ValidUntil.UTC())
}

func (g Grant) PeriodAt(now time.Time) (time.Time, time.Time, error) {
	if g.Effective != nil {
		index := g.Effective.PeriodContributor
		if index < 0 || index >= len(g.Effective.Contributors) {
			return time.Time{}, time.Time{}, fmt.Errorf("effective grant period contributor is invalid")
		}
		return g.Effective.Contributors[index].PeriodAt(now)
	}
	if !g.ActiveAt(now) {
		return time.Time{}, time.Time{}, ErrGrantInactive
	}
	if g.Period != PeriodAnnual && g.Period != PeriodMonthly && g.Period != PeriodContinuous {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid grant period: %s", g.Period)
	}
	if g.ValidUntil != nil {
		return g.ValidFrom.UTC(), g.ValidUntil.UTC(), nil
	}
	anchor := time.Date(g.ValidFrom.UTC().Year(), g.ValidFrom.UTC().Month(), g.ValidFrom.UTC().Day(), 0, 0, 0, 0, time.UTC)
	switch g.Period {
	case PeriodAnnual:
		years := now.UTC().Year() - anchor.Year()
		start := anchor.AddDate(years, 0, 0)
		if now.UTC().Before(start) {
			start = anchor.AddDate(years-1, 0, 0)
		}
		return start, start.AddDate(1, 0, 0), nil
	case PeriodMonthly:
		start := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0), nil
	case PeriodContinuous:
		return anchor, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("invalid grant period: %s", g.Period)
	}
}

// EffectiveGrant keeps aggregate rights separate from source/commercial
// provenance. PeriodContributor identifies the source whose allowance window
// applies to PeriodAt; every Contributor is an unaggregated authoritative row.
type EffectiveGrant struct {
	Contributors      []Grant
	PeriodContributor int
}

type GrantService interface {
	GetGrant(context.Context, CustomerID, ProductID, CapabilityID, time.Time) (Grant, error)
}

type PaymentEventType string

const (
	PaymentSucceeded PaymentEventType = "succeeded"
	PaymentFailed    PaymentEventType = "failed"
	PaymentExpired   PaymentEventType = "expired"
	PaymentIgnored   PaymentEventType = "ignored"
)

type PaymentEvent struct {
	ProviderEventID   string
	OperationID       string
	Type              PaymentEventType
	ProviderReference string
	AmountMinor       int64
	Currency          string
	OccurredAt        time.Time
	Metadata          map[string]string
}
type PurchasePaymentState struct {
	PurchaseID, Provider, Environment, ProviderReference, Currency string
	CustomerID                                                     CustomerID
	ProductID                                                      ProductID
	AmountMinor                                                    int64
}
type PaymentTransition struct {
	Event                 PaymentEvent
	Provider, Environment string
	Kind                  string
	AmountMinor           int64
	Currency              string
	ValidFrom, ValidUntil time.Time
}
type PaymentRepository interface {
	PurchasePaymentState(context.Context, string) (PurchasePaymentState, error)
	ApplyPaymentTransition(context.Context, PaymentTransition) error
}
type PaymentService struct{ Repository PaymentRepository }

func (s PaymentService) Process(ctx context.Context, provider, environment string, event PaymentEvent, validFrom, validUntil time.Time) error {
	if event.Type == PaymentIgnored {
		return nil
	}
	purchase, err := s.Repository.PurchasePaymentState(ctx, event.OperationID)
	if err != nil {
		return err
	}
	if purchase.Provider != provider || purchase.Environment != environment || purchase.ProviderReference != event.ProviderReference {
		return fmt.Errorf("provider-reference-mismatch")
	}
	kind := map[PaymentEventType]string{PaymentSucceeded: "success", PaymentFailed: "failed", PaymentExpired: "expired"}[event.Type]
	if kind == "" {
		return fmt.Errorf("unsupported-payment-event")
	}
	amount, currency := purchase.AmountMinor, purchase.Currency
	if kind == "success" {
		if event.AmountMinor != purchase.AmountMinor || event.Currency != purchase.Currency {
			return fmt.Errorf("payment-verification-failed")
		}
		amount, currency = event.AmountMinor, event.Currency
	}
	return s.Repository.ApplyPaymentTransition(ctx, PaymentTransition{Event: event, Provider: provider, Environment: environment, Kind: kind, AmountMinor: amount, Currency: currency, ValidFrom: validFrom, ValidUntil: validUntil})
}
