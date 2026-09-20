package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"golang.org/x/crypto/bcrypt"
)

func TestRegistrationTTLMinCreatesFixedEphemeralProfile(t *testing.T) {
	s, h := testServer(t)
	before := time.Now().UTC()
	req, err := http.NewRequest(http.MethodPost, h.URL+"/v1/accounts", strings.NewReader(`{"email":"ttl-min@example.invalid","password":"long-enough-password","ttl":"min"}`))
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
		Token     string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d id=%q err=%v", response.StatusCode, out.AccountID, err)
	}
	a, err := s.db.AccountByID(context.Background(), out.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if a.LifecycleKind != store.AccountLifecycleEphemeral {
		t.Fatalf("kind=%q", a.LifecycleKind)
	}
	expires, _ := time.Parse(time.RFC3339Nano, a.MembershipExpiresAt)
	purge, _ := time.Parse(time.RFC3339Nano, a.PurgeAfter)
	if expires.Before(before.Add(4*time.Minute)) || expires.After(time.Now().UTC().Add(6*time.Minute)) {
		t.Fatalf("expiry=%s", expires)
	}
	if purge.Before(before.Add(9*time.Minute)) || purge.After(time.Now().UTC().Add(11*time.Minute)) {
		t.Fatalf("purge=%s", purge)
	}
	grants := tpahubproduct.MinEphemeralGrants(expires.Add(-5 * time.Minute))
	for _, grant := range grants {
		got, err := s.db.GetGrant(context.Background(), commerce.CustomerID(out.AccountID), grant.ProductID, grant.CapabilityID, time.Now().UTC())
		if err != nil {
			t.Fatalf("grant %s: %v", grant.CapabilityID, err)
		}
		if got.Limits != grant.Limits {
			t.Fatalf("grant limits=%+v want %+v", got.Limits, grant.Limits)
		}
	}
	if out.Token == "" {
		t.Fatal("missing session token")
	}
}

func TestRegistrationRejectsArbitraryTTL(t *testing.T) {
	for _, ttl := range []string{`5`, `"5m"`, `"short"`, `5`} {
		s, h := testServer(t)
		body := `{"email":"bad-ttl@example.invalid","password":"long-enough-password","ttl":` + ttl + `}`
		req, err := http.NewRequest(http.MethodPost, h.URL+"/v1/accounts", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("ttl %s status=%d", ttl, response.StatusCode)
		}
		if _, err := s.db.AccountByEmail(context.Background(), "bad-ttl@example.invalid"); err == nil {
			t.Fatalf("ttl %s created an account", ttl)
		}
	}
}

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
		Token     string `json:"token"`
	}
	if err = json.NewDecoder(response.Body).Decode(&out); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("registration status=%d account=%q err=%v", response.StatusCode, out.AccountID, err)
	}
	if _, err = s.db.GetGrant(context.Background(), commerce.CustomerID(out.AccountID), commerce.ProductID(TPAHubProductID), CapabilityRepositoryPublish, s.currentTime()); err == nil {
		t.Fatal("hosted registration received an implicit permanent TPA grant")
	}
	if _, err = s.db.GetGrant(context.Background(), commerce.CustomerID(out.AccountID), tpahubproduct.ProductID, tpahubproduct.CapabilityRepositoryPublish, s.currentTime()); err != commerce.ErrGrantNotFound {
		t.Fatalf("hosted registration received legacy entitlement projection: %v", err)
	}
	if _, err = s.grantService().GetGrant(context.Background(), commerce.CustomerID(out.AccountID), tpahubproduct.ProductID, CapabilityRepositoryCreate, s.currentTime()); !errors.Is(err, commerce.ErrGrantNotFound) {
		t.Fatalf("hosted default grant service=%v, want grant-not-found", err)
	}
	create, err := http.NewRequest(http.MethodPost, h.URL+"/v1/repositories", strings.NewReader(`{"name":"unpaid"}`))
	if err != nil {
		t.Fatal(err)
	}
	create.Header.Set("Authorization", "Bearer "+out.Token)
	create.Header.Set("Content-Type", "application/json")
	created, err := http.DefaultClient.Do(create)
	if err != nil {
		t.Fatal(err)
	}
	defer created.Body.Close()
	if created.StatusCode != http.StatusForbidden {
		t.Fatalf("unpaid hosted repository creation status=%d, want 403", created.StatusCode)
	}
}

func TestDefaultGrantServiceRequiresActivePersistedGrant(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	account := store.Account{ID: "paid-policy", Email: "paid-policy@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano)}
	if err := s.db.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	assertDenied := func(want error) {
		t.Helper()
		if _, err := s.grantService().GetGrant(ctx, commerce.CustomerID(account.ID), tpahubproduct.ProductID, CapabilityRepositoryCreate, now); !errors.Is(err, want) {
			t.Fatalf("default grant service error=%v, want %v", err, want)
		}
		if err := s.repositoryPolicy().AuthorizeCreation(ctx, account.ID); !errors.Is(err, want) {
			t.Fatalf("repository creation error=%v, want %v", err, want)
		}
		if _, _, err := s.accountEgress(account.ID, now); !errors.Is(err, want) {
			t.Fatalf("egress authorization error=%v, want %v", err, want)
		}
	}
	assertDenied(commerce.ErrGrantNotFound)

	until := now.Add(time.Hour)
	for _, grant := range tpahubproduct.DefaultGrants(now.Add(-time.Hour)) {
		grant.CustomerID = commerce.CustomerID(account.ID)
		grant.Source = "local"
		grant.ValidUntil = &until
		if err := s.db.PutGrant(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.repositoryPolicy().AuthorizeCreation(ctx, account.ID); err != nil {
		t.Fatalf("active purchase grant did not authorize repository creation: %v", err)
	}
	if _, _, err := s.accountEgress(account.ID, now); err != nil {
		t.Fatalf("active purchase grant did not authorize egress: %v", err)
	}

	for _, grant := range tpahubproduct.DefaultGrants(now.Add(-time.Hour)) {
		grant.CustomerID = commerce.CustomerID(account.ID)
		grant.Source = "local"
		grant.ValidUntil = &now
		if err := s.db.PutGrant(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	assertDenied(commerce.ErrGrantInactive)

	for _, grant := range tpahubproduct.DefaultGrants(now.Add(-time.Hour)) {
		grant.CustomerID = commerce.CustomerID(account.ID)
		grant.Source = "local"
		grant.Enabled = false
		grant.ValidUntil = &until
		if err := s.db.PutGrant(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	assertDenied(commerce.ErrGrantDisabled)
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

func TestAccountSummaryExposesEntitlementNotOperatorAuthority(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if err := s.db.CreateAccount(ctx, store.Account{ID: "summary-account", Email: "summary@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateSession(ctx, store.Session{ID: "summary-session", AccountID: "summary-account", TokenPrefix: "tph_s_summary"}, store.HashToken("tph_s_summary")); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, h.URL+"/v1/account", nil)
	r.Header.Set("Authorization", "Bearer tph_s_summary")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "operator") || !strings.Contains(w.Body.String(), "storage_limit_state") {
		t.Fatalf("account summary status=%d body=%s", w.Code, w.Body.String())
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
