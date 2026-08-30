package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
)

func ValidateBillingConfiguration(c config.Config) error {
	if c.BillingPurchasesEnabled && c.BillingProvider != "stripe" {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("purchase flow requires Stripe test provider")}
	}
	env := BillingEnvironment(c.BillingEnvironment)
	card, cardErr := rateCardByID(c.BillingRateCard)
	if c.BillingRateCard == "" {
		card, cardErr = mockRateCardV1, nil
	}
	if cardErr != nil || validateRateCard(card) != nil {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("invalid billing rate card: %s", c.BillingRateCard)}
	}
	if err := validateProviderCompatibility(card.Mode, env); err != nil {
		return err
	}
	if c.BillingProvider == "mock" {
		if env != BillingEnvironmentMock || card.Mode != RateCardMock {
			return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("mock provider requires mock billing environment")}
		}
		return nil
	}
	if c.BillingProvider == "stripe" {
		if env != BillingEnvironmentTest {
			return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe is only enabled in test environment")}
		}
		if !strings.HasPrefix(c.StripeTestSecret, "sk_test_") {
			return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe test secret is required")}
		}
		if c.BillingPurchasesEnabled {
			if c.BillingRateCard != "production-nano-v1" || c.StripeWebhookSecret == "" {
				return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Nano TEST purchases require production-nano-v1 and a webhook secret")}
			}
		}
		return nil
	}
	return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("unknown billing provider: %s", c.BillingProvider)}
}

type BillingEnvironment string

const (
	BillingEnvironmentMock       BillingEnvironment = "mock"
	BillingEnvironmentTest       BillingEnvironment = "test"
	BillingEnvironmentProduction BillingEnvironment = "production"
)

type FinalizedPurchase struct {
	PurchaseID, AccountID, PlanCode, RateCardID string
	RateCardMode                                RateCardMode
	TaxClassification                           ProductTaxClassification
	Currency                                    string
	AmountMinor                                 int64
	BillingEnvironment                          BillingEnvironment
	IdempotencyKey                              string
	FinalizedAt                                 time.Time
}
type CheckoutResult struct {
	Provider          string
	Environment       BillingEnvironment
	ExternalReference string
	CheckoutURL       string
	ExpiresAt         time.Time
}
type PurchaseProvider interface {
	CreateCheckout(context.Context, FinalizedPurchase) (CheckoutResult, error)
}

type ProviderBillingRequest struct {
	CustomerReference string
	Period            BillingPeriod
	Currency          string
	LineItems         []BillingLineItem
	TotalAmountMinor  int64
	IdempotencyKey    string
}
type providerError struct {
	Code  string
	Cause error
}

func (e providerError) Error() string {
	if e.Cause == nil {
		return e.Code
	}
	return e.Code + ": " + e.Cause.Error()
}
func (e providerError) Unwrap() error { return e.Cause }

func validateProviderCompatibility(mode RateCardMode, environment BillingEnvironment) error {
	if mode == RateCardMock && environment == BillingEnvironmentProduction {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("mock rate card cannot be submitted to a production provider")}
	}
	if environment == BillingEnvironmentProduction {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("production billing is disabled until a production rate card exists")}
	}
	if mode == RateCardProduction && environment != BillingEnvironmentTest {
		return providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("production rate cards are currently enabled only with the Stripe test provider")}
	}
	return nil
}
func validateFinalized(f FinalizedBillingPeriod) error {
	if f.AccountID == "" || f.Period == "" || f.PlanCode == "" || f.RateCardID == "" || f.RateCardMode == "" || f.Currency == "" || f.IdempotencyKey == "" {
		return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("incomplete finalized billing result")}
	}
	sum := int64(0)
	for _, item := range f.LineItems {
		if item.Quantity < 0 || item.UnitAmountMinor < 0 || item.AmountMinor < 0 || item.Currency != f.Currency {
			return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("invalid billing line item")}
		}
		amount, e := safeMultiply(item.Quantity, item.UnitAmountMinor)
		if e != nil || amount != item.AmountMinor {
			return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("billing line item amount is inconsistent")}
		}
		sum, e = safeAdd(sum, item.AmountMinor)
		if e != nil {
			return providerError{Code: "provider-invalid-request", Cause: e}
		}
	}
	if sum != f.TotalAmountMinor {
		return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("billing line item sum does not equal total")}
	}
	if f.RateCardMode == RateCardProduction {
		card, e := rateCardByID(f.RateCardID)
		parts := strings.Split(string(f.Period), "/")
		start, se := time.Parse("2006-01-02", "")
		end, ee := time.Parse("2006-01-02", "")
		if len(parts) == 2 {
			start, se = time.Parse("2006-01-02", parts[0])
			end, ee = time.Parse("2006-01-02", parts[1])
		}
		if e != nil || validateRateCard(card) != nil || f.PlanCode != "nano" || f.Currency != "EUR" || f.TotalAmountMinor != 500 || se != nil || ee != nil || !end.Equal(start.AddDate(1, 0, 0)) {
			return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("invalid production Nano finalization")}
		}
	}
	return nil
}

type StripeBillingProvider struct {
	Store       *store.Store
	SecretKey   string
	Environment BillingEnvironment
	BaseURL     string
	Client      *http.Client
	SuccessURL  string
	CancelURL   string
}

func NewStripeBillingProvider(st *store.Store, secret string, environment BillingEnvironment) (*StripeBillingProvider, error) {
	if environment != BillingEnvironmentTest {
		return nil, providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe is only enabled in test environment")}
	}
	if !strings.HasPrefix(secret, "sk_test_") {
		return nil, providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe test secret is required")}
	}
	return &StripeBillingProvider{Store: st, SecretKey: secret, Environment: environment, BaseURL: "https://api.stripe.com", Client: http.DefaultClient}, nil
}
func (p *StripeBillingProvider) EnvironmentName() BillingEnvironment { return p.Environment }
func (p *StripeBillingProvider) CreateCheckout(ctx context.Context, purchase FinalizedPurchase) (CheckoutResult, error) {
	if p.Environment != BillingEnvironmentTest || purchase.BillingEnvironment != BillingEnvironmentTest {
		return CheckoutResult{}, providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe Checkout is test-only")}
	}
	if err := validateFinalizedPurchase(purchase); err != nil {
		return CheckoutResult{}, err
	}
	if p.Store == nil {
		return CheckoutResult{}, providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("purchase mapping store unavailable")}
	}
	customer, err := p.Store.ProviderAccount(ctx, purchase.AccountID, "stripe", string(p.Environment))
	if err != nil {
		cid, ce := p.createCustomerForAccount(ctx, purchase.AccountID, purchase.IdempotencyKey+":customer")
		if ce != nil {
			return CheckoutResult{}, ce
		}
		customer = store.BillingProviderAccount{AccountID: purchase.AccountID, Provider: "stripe", Environment: string(p.Environment), ProviderCustomerID: cid, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if ce = p.Store.SaveProviderAccount(ctx, customer); ce != nil {
			return CheckoutResult{}, ce
		}
	}
	values := url.Values{
		"mode": {"payment"}, "customer": {customer.ProviderCustomerID},
		"line_items[0][price_data][currency]":               {strings.ToLower(purchase.Currency)},
		"line_items[0][price_data][unit_amount]":            {strconv.FormatInt(purchase.AmountMinor, 10)},
		"line_items[0][price_data][product_data][name]":     {"TPA Hub Nano annual plan"},
		"line_items[0][price_data][product_data][tax_code]": {stripeTaxCode(purchase.TaxClassification)},
		"line_items[0][quantity]":                           {"1"}, "metadata[tpahub_purchase_id]": {purchase.PurchaseID},
		"success_url": {p.SuccessURL}, "cancel_url": {p.CancelURL},
	}
	var raw struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err = p.request(ctx, "/v1/checkout/sessions", values, purchase.IdempotencyKey, &raw); err != nil {
		return CheckoutResult{}, err
	}
	if raw.ID == "" || raw.URL == "" {
		return CheckoutResult{}, providerError{Code: "provider-invalid-response", Cause: fmt.Errorf("Stripe Checkout response incomplete")}
	}
	return CheckoutResult{Provider: "stripe", Environment: p.Environment, ExternalReference: raw.ID, CheckoutURL: raw.URL}, nil
}
func (p *StripeBillingProvider) createCustomerForAccount(ctx context.Context, accountID, key string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	e := p.request(ctx, "/v1/customers", url.Values{"description": {"TPA Hub account " + accountID}, "metadata[tpahub_account_id]": {accountID}}, key, &out)
	if e != nil {
		return "", e
	}
	if out.ID == "" {
		return "", providerError{Code: "provider-invalid-response", Cause: fmt.Errorf("Stripe customer ID missing")}
	}
	return out.ID, nil
}
func stripeTaxCode(classification ProductTaxClassification) string {
	switch classification {
	case TaxSaaSPersonal:
		return "txcd_10103000"
	case TaxSaaSBusiness:
		return "txcd_10103001"
	default:
		return ""
	}
}
func validateFinalizedPurchase(p FinalizedPurchase) error {
	card, cardErr := rateCardByID(p.RateCardID)
	if p.PurchaseID == "" || p.AccountID == "" || p.PlanCode != "nano" || p.RateCardID != "production-nano-v1" || p.RateCardMode != RateCardProduction || p.Currency != "EUR" || p.AmountMinor != 500 || p.BillingEnvironment != BillingEnvironmentTest || p.IdempotencyKey == "" || stripeTaxCode(p.TaxClassification) == "" || cardErr != nil || validateRateCard(card) != nil || card.BillingCadence != CadenceAnnual {
		return providerError{Code: "provider-invalid-request", Cause: fmt.Errorf("invalid Nano purchase")}
	}
	return nil
}
func (p *StripeBillingProvider) Submit(ctx context.Context, f FinalizedBillingPeriod) (ProviderSubmissionResult, error) {
	if err := validateProviderCompatibility(f.RateCardMode, p.Environment); err != nil {
		return ProviderSubmissionResult{}, err
	}
	if err := validateFinalized(f); err != nil {
		return ProviderSubmissionResult{}, err
	}
	if p.Store == nil {
		return ProviderSubmissionResult{}, providerError{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe mapping store is unavailable")}
	}
	existing, e := p.Store.ProviderSubmission(ctx, f.AccountID, string(f.Period), "stripe", string(p.Environment))
	if e == nil && existing.Status == "submitted" {
		return ProviderSubmissionResult{Provider: "stripe", Environment: p.Environment, ExternalReference: existing.ProviderReference, Status: existing.Status}, nil
	}
	now := time.Now().UTC()
	if customer, e := p.Store.ProviderAccount(ctx, f.AccountID, "stripe", string(p.Environment)); e == nil {
		refs, e := p.submitItems(ctx, customer.ProviderCustomerID, f)
		if e == nil {
			_ = p.Store.SaveProviderSubmission(ctx, store.BillingProviderSubmission{AccountID: f.AccountID, Period: string(f.Period), Provider: "stripe", Environment: string(p.Environment), FinalizationID: f.IdempotencyKey, Status: "submitted", ProviderReference: strings.Join(refs, ","), AttemptedAt: now.Format(time.RFC3339Nano), CompletedAt: now.Format(time.RFC3339Nano)})
			return ProviderSubmissionResult{Provider: "stripe", Environment: p.Environment, ExternalReference: strings.Join(refs, ","), Status: "submitted"}, nil
		} else {
			_ = p.Store.SaveProviderSubmission(ctx, store.BillingProviderSubmission{AccountID: f.AccountID, Period: string(f.Period), Provider: "stripe", Environment: string(p.Environment), FinalizationID: f.IdempotencyKey, Status: "failed", AttemptedAt: now.Format(time.RFC3339Nano), LastError: e.Error()})
			return ProviderSubmissionResult{}, e
		}
	}
	customerID, e := p.createCustomer(ctx, f)
	if e != nil {
		return p.failed(ctx, f, now, e)
	}
	if e = p.Store.SaveProviderAccount(ctx, store.BillingProviderAccount{AccountID: f.AccountID, Provider: "stripe", Environment: string(p.Environment), ProviderCustomerID: customerID, CreatedAt: now.Format(time.RFC3339Nano)}); e != nil {
		return p.failed(ctx, f, now, providerError{Code: "provider-configuration-invalid", Cause: e})
	}
	refs, e := p.submitItems(ctx, customerID, f)
	if e != nil {
		return p.failed(ctx, f, now, e)
	}
	reference := strings.Join(refs, ",")
	_ = p.Store.SaveProviderSubmission(ctx, store.BillingProviderSubmission{AccountID: f.AccountID, Period: string(f.Period), Provider: "stripe", Environment: string(p.Environment), FinalizationID: f.IdempotencyKey, Status: "submitted", ProviderReference: reference, AttemptedAt: now.Format(time.RFC3339Nano), CompletedAt: now.Format(time.RFC3339Nano)})
	return ProviderSubmissionResult{Provider: "stripe", Environment: p.Environment, ExternalReference: reference, Status: "submitted"}, nil
}
func (p *StripeBillingProvider) failed(ctx context.Context, f FinalizedBillingPeriod, now time.Time, e error) (ProviderSubmissionResult, error) {
	_ = p.Store.SaveProviderSubmission(ctx, store.BillingProviderSubmission{AccountID: f.AccountID, Period: string(f.Period), Provider: "stripe", Environment: string(p.Environment), FinalizationID: f.IdempotencyKey, Status: "failed", AttemptedAt: now.Format(time.RFC3339Nano), LastError: e.Error()})
	return ProviderSubmissionResult{}, e
}
func (p *StripeBillingProvider) request(ctx context.Context, path string, values url.Values, idempotency string, out any) error {
	base := p.BaseURL
	if base == "" {
		base = "https://api.stripe.com"
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+path, strings.NewReader(values.Encode()))
	if e != nil {
		return providerError{Code: "provider-invalid-request", Cause: e}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", idempotency)
	req.SetBasicAuth(p.SecretKey, "")
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, e := client.Do(req)
	if e != nil {
		return providerError{Code: "provider-unavailable", Cause: e}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		code := "provider-rejected"
		if res.StatusCode >= 400 && res.StatusCode < 500 {
			code = "provider-rejected-4xx"
		}
		return providerError{Code: code, Cause: fmt.Errorf("Stripe returned HTTP %d", res.StatusCode)}
	}
	if e = json.Unmarshal(body, out); e != nil {
		return providerError{Code: "provider-invalid-response", Cause: e}
	}
	return nil
}
func (p *StripeBillingProvider) createCustomer(ctx context.Context, f FinalizedBillingPeriod) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	e := p.request(ctx, "/v1/customers", url.Values{"description": {"TPA Hub account " + f.AccountID}, "metadata[tpahub_account_id]": {"" + f.AccountID}}, f.IdempotencyKey+":customer", &out)
	if e != nil {
		return "", e
	}
	if out.ID == "" {
		return "", providerError{Code: "provider-invalid-response", Cause: fmt.Errorf("Stripe customer ID missing")}
	}
	return out.ID, nil
}
func (p *StripeBillingProvider) submitItems(ctx context.Context, customer string, f FinalizedBillingPeriod) ([]string, error) {
	refs := make([]string, 0, len(f.LineItems))
	for i, item := range f.LineItems {
		values := url.Values{"customer": {customer}, "amount": {strconv.FormatInt(item.AmountMinor, 10)}, "currency": {strings.ToLower(f.Currency)}, "description": {item.Kind}}
		var out struct {
			ID string `json:"id"`
		}
		if e := p.request(ctx, "/v1/invoiceitems", values, fmt.Sprintf("%s:item-%d", f.IdempotencyKey, i), &out); e != nil {
			return nil, e
		}
		if out.ID == "" {
			return nil, providerError{Code: "provider-invalid-response", Cause: fmt.Errorf("Stripe invoice item ID missing")}
		}
		refs = append(refs, out.ID)
	}
	return refs, nil
}
