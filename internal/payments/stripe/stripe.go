package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/payments"
)

const ProviderName = "stripe"

type Adapter struct {
	Mappings    payments.CustomerMappingStore
	SecretKey   string
	Environment payments.Environment
	BaseURL     string
	Client      *http.Client
	Now         func() time.Time
}

func New(mappings payments.CustomerMappingStore, secret string, environment payments.Environment) (*Adapter, error) {
	if environment != payments.EnvironmentTest {
		return nil, payments.Error{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe is only enabled in test environment")}
	}
	if !strings.HasPrefix(secret, "sk_test_") {
		return nil, payments.Error{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe test secret is required")}
	}
	return &Adapter{Mappings: mappings, SecretKey: secret, Environment: environment, BaseURL: "https://api.stripe.com", Client: http.DefaultClient, Now: time.Now}, nil
}

func (a *Adapter) CreateCheckout(ctx context.Context, operation payments.Operation) (payments.CheckoutSession, error) {
	if a.Environment != payments.EnvironmentTest || operation.Environment != payments.EnvironmentTest {
		return payments.CheckoutSession{}, payments.Error{Code: "provider-configuration-invalid", Cause: fmt.Errorf("Stripe Checkout is test-only")}
	}
	if operation.OperationID == "" || operation.IdempotencyKey == "" || operation.CustomerID == "" || operation.ProductID == "" || operation.Description == "" || operation.Currency == "" || operation.AmountMinor < 0 || operation.SuccessURL == "" || operation.CancelURL == "" {
		return payments.CheckoutSession{}, payments.Error{Code: "provider-invalid-request", Cause: fmt.Errorf("incomplete payment operation")}
	}
	taxCode := taxCode(operation.TaxClass)
	if taxCode == "" {
		return payments.CheckoutSession{}, payments.Error{Code: "provider-invalid-request", Cause: fmt.Errorf("unsupported tax class")}
	}
	if a.Mappings == nil {
		return payments.CheckoutSession{}, payments.Error{Code: "provider-configuration-invalid", Cause: fmt.Errorf("payment customer mapping store unavailable")}
	}
	mapping, err := a.Mappings.GetPaymentCustomerMapping(ctx, operation.CustomerID, ProviderName, a.Environment)
	if err != nil && !errors.Is(err, payments.ErrCustomerMappingNotFound) {
		return payments.CheckoutSession{}, err
	}
	if errors.Is(err, payments.ErrCustomerMappingNotFound) {
		customerReference, createErr := a.createCustomer(ctx, operation)
		if createErr != nil {
			return payments.CheckoutSession{}, createErr
		}
		now := time.Now().UTC()
		if a.Now != nil {
			now = a.Now().UTC()
		}
		mapping = payments.CustomerMapping{CustomerID: operation.CustomerID, Provider: ProviderName, Environment: string(a.Environment), ProviderCustomerReference: customerReference, CreatedAt: now}
		if err = a.Mappings.SavePaymentCustomerMapping(ctx, mapping); err != nil {
			return payments.CheckoutSession{}, err
		}
	}
	values := url.Values{
		"mode": {"payment"}, "customer": {mapping.ProviderCustomerReference},
		"line_items[0][price_data][currency]":               {strings.ToLower(operation.Currency)},
		"line_items[0][price_data][unit_amount]":            {strconv.FormatInt(operation.AmountMinor, 10)},
		"line_items[0][price_data][product_data][name]":     {operation.Description},
		"line_items[0][price_data][product_data][tax_code]": {taxCode},
		"line_items[0][quantity]":                           {"1"},
		"success_url":                                       {operation.SuccessURL}, "cancel_url": {operation.CancelURL},
	}
	for key, value := range operation.Metadata {
		values["metadata["+key+"]"] = []string{value}
	}
	var response struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err = a.request(ctx, "/v1/checkout/sessions", values, operation.IdempotencyKey, &response); err != nil {
		return payments.CheckoutSession{}, err
	}
	if response.ID == "" || response.URL == "" {
		return payments.CheckoutSession{}, payments.Error{Code: "provider-invalid-response", Cause: fmt.Errorf("Stripe Checkout response incomplete")}
	}
	return payments.CheckoutSession{Provider: ProviderName, Environment: a.Environment, ProviderReference: response.ID, URL: response.URL}, nil
}

func (a *Adapter) createCustomer(ctx context.Context, operation payments.Operation) (string, error) {
	var response struct {
		ID string `json:"id"`
	}
	description := operation.CustomerDescription
	if description == "" {
		description = "Commerce customer " + string(operation.CustomerID)
	}
	values := url.Values{"description": {description}}
	for key, value := range operation.CustomerMetadata {
		values["metadata["+key+"]"] = []string{value}
	}
	if err := a.request(ctx, "/v1/customers", values, operation.IdempotencyKey+":customer", &response); err != nil {
		return "", err
	}
	if response.ID == "" {
		return "", payments.Error{Code: "provider-invalid-response", Cause: fmt.Errorf("Stripe customer ID missing")}
	}
	return response.ID, nil
}

func (a *Adapter) request(ctx context.Context, path string, values url.Values, idempotency string, out any) error {
	base := a.BaseURL
	if base == "" {
		base = "https://api.stripe.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+path, strings.NewReader(values.Encode()))
	if err != nil {
		return payments.Error{Code: "provider-invalid-request", Cause: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", idempotency)
	req.SetBasicAuth(a.SecretKey, "")
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return payments.Error{Code: "provider-unavailable", Cause: err}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		code := "provider-rejected"
		if res.StatusCode >= 400 && res.StatusCode < 500 {
			code = "provider-rejected-4xx"
		}
		return payments.Error{Code: code, Cause: fmt.Errorf("Stripe returned HTTP %d", res.StatusCode)}
	}
	if err = json.Unmarshal(body, out); err != nil {
		return payments.Error{Code: "provider-invalid-response", Cause: err}
	}
	return nil
}

func taxCode(class payments.TaxClass) string {
	switch class {
	case "saas-personal":
		return "txcd_10103000"
	case "saas-business":
		return "txcd_10103001"
	default:
		return ""
	}
}

type webhookEnvelope struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID            string            `json:"id"`
			PaymentStatus string            `json:"payment_status"`
			AmountTotal   int64             `json:"amount_total"`
			Currency      string            `json:"currency"`
			Metadata      map[string]string `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}

func (a *Adapter) ParseWebhook(body []byte, signature, secret string, now time.Time) (commerce.PaymentEvent, error) {
	return ParseWebhook(body, signature, secret, now)
}

func ParseWebhook(body []byte, signature, secret string, now time.Time) (commerce.PaymentEvent, error) {
	if err := VerifySignature(signature, body, secret, now); err != nil {
		return commerce.PaymentEvent{}, err
	}
	var envelope webhookEnvelope
	if json.Unmarshal(body, &envelope) != nil || envelope.ID == "" || envelope.Data.Object.ID == "" {
		return commerce.PaymentEvent{}, fmt.Errorf("invalid provider event")
	}
	operationID := envelope.Data.Object.Metadata["commerce_operation_id"]
	eventType := commerce.PaymentIgnored
	switch envelope.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		if envelope.Data.Object.PaymentStatus == "paid" {
			eventType = commerce.PaymentSucceeded
		}
	case "checkout.session.async_payment_failed":
		eventType = commerce.PaymentFailed
	case "checkout.session.expired":
		eventType = commerce.PaymentExpired
	}
	return commerce.PaymentEvent{ProviderEventID: envelope.ID, OperationID: operationID, Type: eventType, ProviderReference: envelope.Data.Object.ID, AmountMinor: envelope.Data.Object.AmountTotal, Currency: strings.ToUpper(envelope.Data.Object.Currency), OccurredAt: now.UTC(), Metadata: envelope.Data.Object.Metadata}, nil
}

func VerifySignature(header string, body []byte, secret string, now time.Time) error {
	var timestamp string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		if kv[0] == "t" {
			timestamp = kv[1]
		}
		if kv[0] == "v1" {
			signatures = append(signatures, kv[1])
		}
	}
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || timestamp == "" {
		return fmt.Errorf("invalid webhook signature")
	}
	if delta := now.Unix() - unix; delta > 300 || delta < -300 {
		return fmt.Errorf("expired webhook signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, sig := range signatures {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			return nil
		}
	}
	return fmt.Errorf("invalid webhook signature")
}
