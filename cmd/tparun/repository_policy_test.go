package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
)

func TestRepositoryPolicySeparatesOwnershipFromEntitlement(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	if err := s.db.CreateAccount(ctx, store.Account{ID: "b", Email: "b@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "2024-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.PutGrant(ctx, commerce.Grant{CustomerID: "b", ProductID: tpahubproduct.ProductID, CapabilityID: tpahubproduct.CapabilityRepositoryPublish, OfferID: "administrative", Enabled: true, ValidFrom: time.Now().Add(-time.Hour), Period: commerce.PeriodAnnual, Limits: commerce.Limits{StorageBytes: 1 << 40}, Source: "administrative", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateRepository(ctx, store.Repository{ID: "owned", AccountID: "a", Name: "owned", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.repositoryPolicy().AuthorizePublication(ctx, "b", "owned"); err == nil {
		t.Fatal("foreign account was authorized despite having no ownership")
	}
	if _, _, err := s.repositoryPolicy().AuthorizePublication(ctx, "a", "owned"); err != nil {
		t.Fatalf("owner with effective Nano entitlement was denied: %v", err)
	}
}

func TestNormalAccountSessionCannotUseOperatorBootstrapRoute(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if err := s.db.CreateAccount(ctx, store.Account{ID: "normal", Email: "normal@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "2024-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateSession(ctx, store.Session{ID: "normal-session", AccountID: "normal", TokenPrefix: "tph_s_normal"}, store.HashToken("tph_s_normal")); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, h.URL+"/ops/bootstrap", nil)
	r.AddCookie(&http.Cookie{Name: "tpahub_session", Value: "tph_s_normal"})
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/ops/login" {
		t.Fatalf("normal account reached operator route: status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
}

func TestForeignPrivatePublicationIsCloaked(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if err := s.db.CreateAccount(ctx, store.Account{ID: "b", Email: "b@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "2024-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateSession(ctx, store.Session{ID: "b-session", AccountID: "b", TokenPrefix: "tph_s_b"}, store.HashToken("tph_s_b")); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateRepository(ctx, store.Repository{ID: "private-owned", AccountID: "a", Name: "private-owned", Visibility: "private", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	request := func(id string) int {
		r, err := http.NewRequest(http.MethodPost, h.URL+"/v1/repositories/"+id+"/publishes", strings.NewReader("not a deb"))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer tph_s_b")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if got := request("private-owned"); got != http.StatusNotFound {
		t.Fatalf("foreign private publication status=%d, want 404", got)
	}
	if got := request("does-not-exist"); got != http.StatusNotFound {
		t.Fatalf("missing publication status=%d, want 404", got)
	}
}

func TestRepositoryPolicyQuarantinesUnownedRepository(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	if err := insertCorruptUnownedRepository(s, store.Repository{ID: "legacy", Name: "legacy", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.repositoryPolicy().AuthorizeMutation(ctx, "a", "legacy"); err == nil {
		t.Fatal("unowned repository was mutable")
	}
	if err := s.repositoryPolicy().AuthorizeCreation(ctx, ""); err == nil {
		t.Fatal("repository creation accepted an empty owner")
	}
}
