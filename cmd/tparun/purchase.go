package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"io"
	"net/http"
	"strings"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
)

func (s *server) nanoPurchaseAvailable() bool { return s.billingService() != nil }

func (s *server) startNanoPurchase(ctx context.Context, account string, businessUse bool) (store.BillingPurchase, CheckoutResult, error) {
	service := s.billingService()
	if service == nil {
		return store.BillingPurchase{}, CheckoutResult{}, fmt.Errorf("purchase flow unavailable")
	}
	return service.StartPurchase(ctx, commerce.PurchaseRequest{CustomerID: commerce.CustomerID(account), ProductID: tpahubproduct.ProductID, OfferID: "nano", OfferVersion: tpahubproduct.NanoOfferVersion}, businessUse)
}

func (s *server) createNanoPurchase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	businessUse, parseErr := parseBusinessUseJSON(r)
	if parseErr != nil {
		bad(w, http.StatusBadRequest, parseErr.Error())
		return
	}
	purchase, checkout, err := s.startNanoPurchase(r.Context(), accountID(r), businessUse)
	if err != nil {
		if err.Error() == "purchase-already-active" {
			bad(w, http.StatusConflict, err.Error())
		} else {
			bad(w, http.StatusBadGateway, "checkout creation failed")
		}
		return
	}
	out := map[string]any{"purchase_id": purchase.ID, "status": purchase.Status, "provider": purchase.Provider, "environment": purchase.BillingEnvironment}
	if checkout.URL != "" {
		out["checkout_url"] = checkout.URL
	} else {
		out["checkout_reference"] = checkout.ProviderReference
	}
	jsonOut(w, out)
}

func (s *server) accountNanoPurchase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.webAuth(w, r) {
		return
	}
	businessUse, parseErr := parseBusinessUseForm(r)
	if parseErr != nil {
		bad(w, http.StatusBadRequest, parseErr.Error())
		return
	}
	_, checkout, err := s.startNanoPurchase(r.Context(), accountID(r), businessUse)
	if err != nil {
		if err.Error() == "purchase-already-active" {
			bad(w, http.StatusConflict, "Nano purchase already active")
		} else {
			bad(w, http.StatusBadGateway, "Sandbox checkout unavailable")
		}
		return
	}
	if checkout.URL == "" {
		bad(w, http.StatusBadGateway, "Sandbox checkout unavailable")
		return
	}
	http.Redirect(w, r, checkout.URL, http.StatusSeeOther)
}

func parseBusinessUseJSON(r *http.Request) (bool, error) {
	body, e := io.ReadAll(io.LimitReader(r.Body, 8193))
	if e != nil || len(body) > 8192 {
		return false, fmt.Errorf("invalid purchase request")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return false, nil
	}
	var input struct {
		BusinessUse *bool `json:"business_use"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&input); e != nil {
		return false, fmt.Errorf("invalid purchase request")
	}
	var extra any
	if e = dec.Decode(&extra); e != io.EOF {
		return false, fmt.Errorf("invalid purchase request")
	}
	if input.BusinessUse == nil {
		return false, nil
	}
	return *input.BusinessUse, nil
}
func parseBusinessUseForm(r *http.Request) (bool, error) {
	if e := r.ParseForm(); e != nil {
		return false, fmt.Errorf("invalid purchase request")
	}
	value, ok := r.Form["business_use"]
	if !ok || len(value) == 0 || value[0] == "" {
		return false, nil
	}
	if value[0] != "on" && value[0] != "true" && value[0] != "1" {
		return false, fmt.Errorf("invalid business_use")
	}
	return true, nil
}
