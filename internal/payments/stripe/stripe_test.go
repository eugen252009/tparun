package stripe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
)

func sign(body, secret string, at time.Time) string {
	timestamp := fmt.Sprint(at.Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + body))
	return "t=" + timestamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestParseWebhookNormalizesStripeEvent(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	body := `{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_1","payment_status":"paid","amount_total":725,"currency":"eur","metadata":{"commerce_operation_id":"purchase-1"}}}}`
	event, err := ParseWebhook([]byte(body), sign(body, "secret", now), "secret", now)
	if err != nil {
		t.Fatal(err)
	}
	if event.ProviderEventID != "evt_1" || event.OperationID != "purchase-1" || event.Type != commerce.PaymentSucceeded || event.ProviderReference != "cs_1" || event.AmountMinor != 725 || event.Currency != "EUR" {
		t.Fatalf("event=%+v", event)
	}
}

func TestParseWebhookPreservesProviderMetadataWithoutProductInterpretation(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	body := `{"id":"evt_legacy","type":"checkout.session.expired","data":{"object":{"id":"cs_legacy","metadata":{"tpahub_purchase_id":"legacy-purchase"}}}}`
	event, err := ParseWebhook([]byte(body), sign(body, "secret", now), "secret", now)
	if err != nil || event.OperationID != "" || event.Metadata["tpahub_purchase_id"] != "legacy-purchase" {
		t.Fatalf("event=%+v err=%v", event, err)
	}
}

func TestParseWebhookVerifiesExactRawBody(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	body := `{"id":"evt_1","type":"checkout.session.expired","data":{"object":{"id":"cs_1","metadata":{"commerce_operation_id":"purchase-1"}}}}`
	signature := sign(body, "secret", now)
	if _, err := ParseWebhook([]byte(body+" "), signature, "secret", now); err == nil {
		t.Fatal("modified raw body accepted")
	}
}
