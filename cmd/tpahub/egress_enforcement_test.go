package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
)

func TestEgressAccountingFailureIsObservable(t *testing.T) {
	s, _ := testServer(t)
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	s.db.Close()
	s.recordRepositoryEgress("missing-repository", "2026-01-01", 10)
	if !strings.Contains(output.String(), "repository egress accounting") {
		t.Fatalf("accounting failure was not logged: %q", output.String())
	}
	if _, _, err := s.accountEgress("a", time.Now()); err == nil {
		t.Fatal("accounting failure did not degrade subsequent authority checks")
	}
}

func TestEgressEnforcementUsesGrantLimit(t *testing.T) {
	s, h := testServer(t)
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if err := s.db.PutGrant(context.Background(), commerce.Grant{CustomerID: "a", ProductID: commerce.ProductTPAHub, CapabilityID: CapabilityRepositoryDownload, OfferID: "custom", Enabled: true, ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Period: commerce.PeriodAnnual, Limits: commerce.Limits{EgressBytes: 1000}, Source: "local", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	id := "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: id, AccountID: "a", Name: "grant-limited", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", id)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.deb"), make([]byte, 10), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.db.AddRepositoryEgressDaily(context.Background(), id, "2026-08-31", 1000, "now"); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(h.URL + "/r/" + id + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.Header.Get("X-TPAHub-Egress-Limited") != "true" {
		t.Fatal("custom grant egress limit was not enforced")
	}
	for _, grantErr := range []error{commerce.ErrGrantNotFound, commerce.ErrGrantDisabled, commerce.ErrGrantInactive} {
		s.grants = grantServiceFunc(func(context.Context, commerce.CustomerID, commerce.ProductID, commerce.CapabilityID, time.Time) (commerce.Grant, error) {
			return commerce.Grant{}, grantErr
		})
		response, err = http.Get(h.URL + "/r/" + id + "/package.deb")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.Header.Get("X-TPAHub-Egress-Limited") != "true" {
			t.Fatalf("grant error %v did not fail closed", grantErr)
		}
	}
	s.grants = grantServiceFunc(func(context.Context, commerce.CustomerID, commerce.ProductID, commerce.CapabilityID, time.Time) (commerce.Grant, error) {
		return commerce.Grant{}, errors.New("database unavailable")
	})
	response, err = http.Get(h.URL + "/r/" + id + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected authority failure status=%d", response.StatusCode)
	}
	s.grants = nil
	s.egressLookup = func(string, time.Time) (int64, int64, error) {
		return 0, 0, errors.New("usage database unavailable")
	}
	response, err = http.Get(h.URL + "/r/" + id + "/package.deb")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected usage authority failure status=%d", response.StatusCode)
	}
	privateID := "edededededededededededededededed"
	if err = s.db.CreateRepository(context.Background(), store.Repository{ID: privateID, AccountID: "a", Name: "private-egress", Visibility: "private", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	privateRoot := filepath.Join(s.cfg.DataDir, "repos", privateID)
	if err = os.MkdirAll(privateRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(privateRoot, "package.deb"), []byte("private"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = s.db.CreateCredential(context.Background(), store.ReaderCredential{ID: "private-reader", RepositoryID: privateID, Name: "reader", TokenPrefix: "tok", CreatedAt: "now"}, store.HashToken("reader-token")); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, h.URL+"/r/"+privateID+"/package.deb", nil)
	request.SetBasicAuth("reader", "reader-token")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("private authority failure status=%d", response.StatusCode)
	}
}

func TestEgressGracefulPackageLimitingAndAnnualPool(t *testing.T) {
	s, h := testServer(t)
	clock := time.Date(2026, 8, 31, 23, 59, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	if err := s.db.SetEntitlement(context.Background(), store.AccountEntitlement{AccountID: "a", PlanCode: "nano", StartedAt: "2025-08-31T00:00:00Z", CreatedAt: "2025-08-31T00:00:00Z", UpdatedAt: "2025-08-31T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	id := "55555555555555555555555555555555"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: id, AccountID: "a", Name: "limited", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", id)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.deb"), make([]byte, 1000), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Packages.gz"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.db.AddRepositoryEgressDaily(context.Background(), id, "2026-08-31", NanoEgressBytes-1000, "now"); err != nil {
		t.Fatal(err)
	}
	get := func(path string) *http.Response {
		response, err := http.Get(h.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := get("/r/" + id + "/package.deb")
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || len(body) != 1000 || response.Header.Get("X-TPAHub-Egress-Limited") != "" {
		t.Fatalf("threshold-crossing response status=%d bytes=%d limited=%q", response.StatusCode, len(body), response.Header.Get("X-TPAHub-Egress-Limited"))
	}
	response = get("/r/" + id + "/package.deb")
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || len(body) != 1000 || response.Header.Get("X-TPAHub-Egress-Limited") != "true" {
		t.Fatalf("limited response status=%d bytes=%d limited=%q", response.StatusCode, len(body), response.Header.Get("X-TPAHub-Egress-Limited"))
	}
	response = get("/r/" + id + "/Packages.gz")
	response.Body.Close()
	if response.Header.Get("X-TPAHub-Egress-Limited") != "" {
		t.Fatal("metadata was rate-limited")
	}
	if got, err := s.db.RepositoryEgress(context.Background(), id, "2026-08"); err != nil || got != NanoEgressBytes+1100 {
		t.Fatalf("August egress=%d, %v", got, err)
	}
	clock = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	response = get("/r/" + id + "/package.deb")
	response.Body.Close()
	if response.Header.Get("X-TPAHub-Egress-Limited") != "true" {
		t.Fatal("annual pool unexpectedly reset at month boundary")
	}
	if got, err := s.db.RepositoryEgress(context.Background(), id, "2026-09"); err != nil || got != 1000 {
		t.Fatalf("September egress=%d, %v", got, err)
	}
}
