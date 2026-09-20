package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStripeWebhookPersistsIncompleteAuthorityEvidenceWithoutActivation(t *testing.T) {
	s := configuredPurchaseTestServer(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	purchase, _, err := s.startNanoPurchase(context.Background(), "a", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(s.cfg.DataDir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`DROP TRIGGER purchase_grant_snapshot_sets_immutable_delete; DELETE FROM purchase_grant_snapshot_sets WHERE purchase_id=?`, purchase.ID); err != nil {
		t.Fatal(err)
	}
	event := fmt.Sprintf(`{"id":"evt-r11-http","type":"checkout.session.completed","data":{"object":{"id":%q,"payment_status":"paid","amount_subtotal":500,"amount_total":595,"currency":"eur","metadata":{"tpahub_purchase_id":%q}}}}`, purchase.ProviderReference, purchase.ID)
	h := httptest.NewServer(s.routes())
	defer h.Close()
	request, _ := http.NewRequest(http.MethodPost, h.URL+"/v1/billing/providers/stripe/webhook", strings.NewReader(event))
	request.Header.Set("Stripe-Signature", signedStripeEvent(event, "whsec_example", now))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("webhook status=%d", response.StatusCode)
	}
	stored, err := s.db.Purchase(context.Background(), purchase.ID)
	if err != nil || stored.Status != "reconciliation_required" || stored.ProviderState != "provider_success_unreconciled" || stored.ReconciliationReason == "" {
		t.Fatalf("purchase=%+v err=%v", stored, err)
	}
	if count, countErr := s.db.PurchaseGrantInstanceCount(context.Background(), "a"); countErr != nil || count != 0 {
		t.Fatalf("instances=%d err=%v", count, countErr)
	}
	var evidencePurchase, evidenceReference, status string
	if err = raw.QueryRow(`SELECT purchase_id,provider_reference,status FROM billing_provider_events WHERE provider_event_id='evt-r11-http'`).Scan(&evidencePurchase, &evidenceReference, &status); err != nil || evidencePurchase != purchase.ID || evidenceReference != purchase.ProviderReference || status != "reconciliation_required" {
		t.Fatalf("evidence purchase=%q reference=%q status=%q err=%v", evidencePurchase, evidenceReference, status, err)
	}
}
