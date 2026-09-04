package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestHostedRegistrationDoesNotProvisionPermanentTPAGrants(t *testing.T) {
	s, h := testServer(t)
	s.cfg.BillingPurchasesEnabled = true
	req, err := http.NewRequest(http.MethodPost, h.URL+"/v1/accounts", strings.NewReader(`{"email":"hosted@example.invalid","password":"long-enough-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var out struct {
		AccountID string `json:"account_id"`
	}
	if err = json.NewDecoder(response.Body).Decode(&out); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("registration status=%d account=%q err=%v", response.StatusCode, out.AccountID, err)
	}
	if _, err = s.db.GetGrant(context.Background(), commerce.CustomerID(out.AccountID), commerce.ProductID(TPAHubProductID), CapabilityRepositoryPublish, s.currentTime()); err == nil {
		t.Fatal("hosted registration received an implicit permanent TPA grant")
	}
	if _, err = s.db.Entitlement(context.Background(), out.AccountID); err != sql.ErrNoRows {
		t.Fatalf("hosted registration received legacy entitlement projection: %v", err)
	}
}

func TestLocalRegistrationExplicitlyProvisionsTPAGrants(t *testing.T) {
	s, h := testServer(t)
	s.cfg.BillingPurchasesEnabled = false
	s.cfg.LocalProvisioningEnabled = true
	req, err := http.NewRequest(http.MethodPost, h.URL+"/v1/accounts", strings.NewReader(`{"email":"local@example.invalid","password":"long-enough-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var out struct {
		AccountID string `json:"account_id"`
	}
	if err = json.NewDecoder(response.Body).Decode(&out); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("registration status=%d account=%q err=%v", response.StatusCode, out.AccountID, err)
	}
	grant, err := s.db.GetGrant(context.Background(), commerce.CustomerID(out.AccountID), commerce.ProductID(TPAHubProductID), CapabilityRepositoryPublish, s.currentTime())
	if err != nil || grant.Source != "local" || grant.OfferID != "nano" {
		t.Fatalf("local registration grant=%+v err=%v", grant, err)
	}
}

func TestAccountStatusEnforcedAcrossAuthenticationPaths(t *testing.T) {
	s, h := testServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("long-enough-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.CreateAccount(context.Background(), store.Account{ID: "status-account", Email: "status@example.invalid", PasswordHash: string(hash), Status: "active", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err = s.db.CreateSession(context.Background(), store.Session{ID: "status-session", AccountID: "status-account", TokenPrefix: "tph_s_status"}, store.HashToken("tph_s_status")); err != nil {
		t.Fatal(err)
	}

	apiLogin := func() int {
		response, err := http.Post(h.URL+"/v1/login", "application/json", strings.NewReader(`{"Email":"status@example.invalid","Password":"long-enough-password"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	websiteLogin := func() int {
		request, _ := http.NewRequest(http.MethodPost, h.URL+"/login", strings.NewReader("email=status%40example.invalid&password=long-enough-password"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", h.URL)
		client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if got := apiLogin(); got != http.StatusOK {
		t.Fatalf("active API login=%d", got)
	}
	if got := websiteLogin(); got != http.StatusSeeOther {
		t.Fatalf("active website login=%d", got)
	}

	if err = s.db.SetAccountStatus(context.Background(), "status-account", "suspended"); err != nil {
		t.Fatal(err)
	}
	if got := apiLogin(); got != http.StatusUnauthorized {
		t.Fatalf("suspended API login=%d", got)
	}
	if got := websiteLogin(); got != http.StatusOK {
		t.Fatalf("suspended website login=%d", got)
	}

	request, _ := http.NewRequest(http.MethodGet, h.URL+"/v1/repositories", nil)
	request.Header.Set("Authorization", "Bearer tph_s_status")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("suspended bearer session=%d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodGet, h.URL+"/account", nil)
	request.AddCookie(&http.Cookie{Name: "tpahub_session", Value: "tph_s_status"})
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/login" {
		t.Fatalf("suspended cookie session=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	if err = s.db.SetAccountStatus(context.Background(), "status-account", store.AccountStatusActive); err != nil {
		t.Fatal(err)
	}
	if got := apiLogin(); got != http.StatusOK {
		t.Fatalf("reactivated API login=%d", got)
	}
	if got := websiteLogin(); got != http.StatusSeeOther {
		t.Fatalf("reactivated website login=%d", got)
	}
}

func TestAuthenticationAndPrivateReader(t *testing.T) {
	s, h := testServer(t)
	reg := func(email string) string {
		req, _ := http.NewRequest("POST", h.URL+"/v1/accounts", strings.NewReader(`{"email":"`+email+`","password":"long-enough-password"}`))
		req.Header.Set("Content-Type", "application/json")
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var x struct {
			Token string `json:"token"`
		}
		json.NewDecoder(res.Body).Decode(&x)
		if res.StatusCode != 200 {
			t.Fatalf("register %d", res.StatusCode)
		}
		return x.Token
	}
	tok := reg("new@example.invalid")
	req, _ := http.NewRequest("POST", h.URL+"/v1/repositories", strings.NewReader(`{"name":"private"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	var repo struct {
		ID string `json:"id"`
	}
	json.NewDecoder(res.Body).Decode(&repo)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	req, _ = http.NewRequest("POST", h.URL+"/v1/repositories/"+repo.ID+"/reader-credentials", strings.NewReader(`{"name":"client"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, _ = http.DefaultClient.Do(req)
	var cr struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	json.NewDecoder(res.Body).Decode(&cr)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	p := filepath.Join(s.cfg.DataDir, "repos", repo.ID, "x")
	os.WriteFile(p, []byte("ok"), 0644)
	if res, _ = http.Get(h.URL + "/r/" + repo.ID + "/x"); res.StatusCode != 401 {
		t.Fatalf("anonymous status %d", res.StatusCode)
	}
	req, _ = http.NewRequest("GET", h.URL+"/r/"+repo.ID+"/x", nil)
	req.SetBasicAuth("reader", cr.Token)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 200 {
		t.Fatalf("reader status %d", res.StatusCode)
	}
	deleteRequest, _ := http.NewRequest(http.MethodDelete, h.URL+"/v1/repositories/"+repo.ID+"/reader-credentials/"+cr.ID, nil)
	deleteRequest.Header.Set("Authorization", "Bearer "+tok)
	deleted, _ := http.DefaultClient.Do(deleteRequest)
	if deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("credential revoke status %d", deleted.StatusCode)
	}
	deleted.Body.Close()
	req, _ = http.NewRequest("GET", h.URL+"/r/"+repo.ID+"/x", nil)
	req.SetBasicAuth("reader", cr.Token)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked reader status %d", res.StatusCode)
	}
	res.Body.Close()
	account, err := s.db.AccountByEmail(context.Background(), "new@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.SetAccountStatus(context.Background(), account.ID, "suspended"); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("GET", h.URL+"/r/"+repo.ID+"/x", nil)
	req.SetBasicAuth("reader", cr.Token)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("suspended reader status %d", res.StatusCode)
	}
}
