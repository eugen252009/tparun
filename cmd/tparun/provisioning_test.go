package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/provisioning"
	"github.com/eugen252009/tpahub/internal/store"
)

func provisioningRequest(s *server, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if strings.HasPrefix(token, "tph_r_") {
		r.SetBasicAuth("reader", token)
	} else if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

func registerLocalPolicyAccount(t *testing.T, s *server) (id, token string) {
	t.Helper()
	// Unrecognized customer-supplied policy must not become authority.
	w := provisioningRequest(s, "POST", "/v1/accounts", `{"email":"operator-local@example.invalid","password":"long-local-password","capability_source":"legacy-local","local_policy":{"storage_bytes":999999999999,"private_read":true}}`, "")
	var out struct {
		AccountID string `json:"account_id"`
		Token     string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.AccountID == "" || out.Token == "" {
		t.Fatalf("registration: %d %s %v", w.Code, w.Body.String(), err)
	}
	return out.AccountID, out.Token
}

func createLocalPolicyRepo(t *testing.T, s *server, token, visibility string) string {
	t.Helper()
	w := provisioningRequest(s, "POST", "/v1/repositories", fmt.Sprintf(`{"name":%q,"visibility":%q}`, visibility, visibility), token)
	var out struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.ID == "" {
		t.Fatalf("create: %d %s %v", w.Code, w.Body.String(), err)
	}
	return out.ID
}

func TestLocalPolicyRegistrationAuthorizationAndPrivateRead(t *testing.T) {
	for _, private := range []bool{true, false} {
		t.Run(fmt.Sprint(private), func(t *testing.T) {
			p := provisioning.LocalPolicy{StorageBytes: 23, EgressBytes: 0, PrivateRead: private}
			s, _ := configuredTestServer(t, func(s *server) {
				s.cfg.CapabilitySource = config.CapabilitiesLocal
				s.cfg.LocalPolicy = &p
				s.cfg.LocalProvisioningEnabled = false
				s.cfg.BillingIntegration = config.BillingNone
				s.cfg.BillingProvider = "none"
				s.cfg.DeliveryMode = config.DeliveryMetricsOnly
			})
			if err := s.composeCommercial(); err != nil {
				t.Fatal(err)
			}
			if s.commercial != nil || s.purchaseProvider != nil {
				t.Fatal("local provisioning constructed commercial participant")
			}
			id, token := registerLocalPolicyAccount(t, s)
			g, err := s.grantService().GetGrant(context.Background(), commerce.CustomerID(id), TPAHubProductID, CapabilityRepositoryPublish, s.currentTime())
			if err != nil || g.Source != "local" || g.OfferID != "local-policy-v1" || g.PurchaseID != "" || g.Limits.StorageBytes != 23 || g.ValidUntil != nil || g.Period != commerce.PeriodAnnual {
				t.Fatalf("grant %+v %v", g, err)
			}
			pub := createLocalPolicyRepo(t, s, token, "public")
			if _, grant, err := s.repositoryPolicy().AuthorizePublication(context.Background(), id, pub); err != nil || grant.Limits.StorageBytes != 23 {
				t.Fatalf("publication authority %+v %v", grant, err)
			}
			// Both admission cases pass through the unchanged real upload path.
			w := provisioningRequest(s, "POST", "/v1/repositories/"+pub+"/publishes?filename=local.deb", strings.Repeat("x", 24), token)
			if w.Code != 413 {
				t.Fatalf("over-limit publication=%d %s", w.Code, w.Body.String())
			}
			w = provisioningRequest(s, "POST", "/v1/repositories/"+pub+"/publishes?filename=local.deb", "x", token)
			if w.Code != 202 {
				t.Fatalf("within-limit publication=%d %s", w.Code, w.Body.String())
			}
			repo := createLocalPolicyRepo(t, s, token, "private")
			root := filepath.Join(s.cfg.DataDir, "repos", repo)
			if err := os.WriteFile(filepath.Join(root, "package.deb"), []byte("local"), 0644); err != nil {
				t.Fatal(err)
			}
			w = provisioningRequest(s, "POST", "/v1/repositories/"+repo+"/reader-credentials", `{"name":"reader"}`, token)
			_, grantErr := s.grantService().GetGrant(context.Background(), commerce.CustomerID(id), TPAHubProductID, CapabilityRepositoryReadPrivate, s.currentTime())
			if private {
				if w.Code != 200 || grantErr != nil {
					t.Fatalf("private enabled: %d %v", w.Code, grantErr)
				}
				var reader struct{ Token string }
				if err := json.Unmarshal(w.Body.Bytes(), &reader); err != nil {
					t.Fatal(err)
				}
				w = provisioningRequest(s, "GET", "/r/"+repo+"/package.deb", "", reader.Token)
				if w.Code != 200 || w.Body.String() != "local" {
					t.Fatalf("private read=%d %s", w.Code, w.Body.String())
				}
				w = provisioningRequest(s, "GET", "/r/"+repo+"/package.deb", "", "")
				if w.Code == 200 {
					t.Fatal("anonymous private access")
				}
			} else {
				if w.Code != 403 || !errors.Is(grantErr, commerce.ErrGrantDisabled) {
					t.Fatalf("private disabled: %d %v", w.Code, grantErr)
				}
				w = provisioningRequest(s, "GET", "/r/"+repo+"/package.deb", "", token)
				if w.Code != 403 {
					t.Fatalf("disabled private read=%d", w.Code)
				}
			}
			if err := s.db.SetAccountStatus(context.Background(), id, "suspended"); err != nil {
				t.Fatal(err)
			}
			if err := s.repositoryPolicy().AuthorizeCreation(context.Background(), id); err == nil {
				t.Fatal("suspended creation accepted")
			}
			if _, _, err := s.repositoryPolicy().AuthorizePublication(context.Background(), id, pub); err == nil {
				t.Fatal("suspended publication accepted")
			}
		})
	}
}

// Capture persisted rows, including disabled grants and their original timestamps,
// not merely the effective runtime view.
func persistedProvisioningFacts(t *testing.T, dir string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var result []any
	for _, table := range []string{"accounts", "product_grants", "purchase_grant_instances"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1,2,3")
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			result = append(result, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLocalPolicyExistingFactsDuplicateAndRestart(t *testing.T) {
	s, _ := configuredTestServer(t, func(s *server) { s.cfg.CapabilitySource = config.CapabilitiesLocal })
	id, _ := registerLocalPolicyAccount(t, s)
	before := persistedProvisioningFacts(t, s.cfg.DataDir)
	// Duplicate registration uses a different proposed policy, but must roll back.
	p := provisioning.LocalPolicy{StorageBytes: 100, EgressBytes: 200, PrivateRead: false}
	s.cfg.LocalPolicy = &p
	w := provisioningRequest(s, "POST", "/v1/accounts", `{"email":"operator-local@example.invalid","password":"long-local-password"}`, "")
	if w.Code != 409 {
		t.Fatalf("duplicate status=%d", w.Code)
	}
	// Direct retry of the same account transaction also cannot add authority.
	a, err := s.db.AccountByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := s.initialTPAGrants(a.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateAccount(context.Background(), a, grants...); err == nil {
		t.Fatal("duplicate account accepted")
	}
	if after := persistedProvisioningFacts(t, s.cfg.DataDir); after != before {
		t.Fatal("duplicate provisioning mutated historical or local facts")
	}
	// A failure after initial rows were inserted rolls back the entire creation.
	a.ID, a.Email = "rollback-local", "rollback@example.invalid"
	broken := append(append([]commerce.Grant{}, grants...), grants[0])
	if err := s.db.CreateAccount(context.Background(), a, broken...); err == nil {
		t.Fatal("duplicate capability accepted")
	}
	if _, err := s.db.AccountByID(context.Background(), a.ID); err == nil {
		t.Fatal("partially provisioned account survived")
	}
	if after := persistedProvisioningFacts(t, s.cfg.DataDir); after != before {
		t.Fatal("failed transaction leaked grant facts")
	}
	s.db.Close()
	db, err := store.Open(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.db = db
	if _, err := initialGrantSource(s.cfg); err != nil {
		t.Fatal(err)
	}
	if after := persistedProvisioningFacts(t, s.cfg.DataDir); after != before {
		t.Fatal("restart reinterpreted durable facts")
	}
	legacy, err := s.grantService().GetGrant(context.Background(), "a", TPAHubProductID, CapabilityRepositoryPublish, s.currentTime())
	if err != nil || legacy.OfferID != "nano" || legacy.Source != "local" {
		t.Fatalf("legacy changed %+v %v", legacy, err)
	}
	local, err := s.grantService().GetGrant(context.Background(), commerce.CustomerID(id), TPAHubProductID, CapabilityRepositoryPublish, s.currentTime())
	if err != nil || local.Limits.StorageBytes != 5_000_000 {
		t.Fatalf("new config regranted existing account %+v %v", local, err)
	}
	// Web registration shares the same source and durable transaction.
	account, err := s.createAccount(httptest.NewRequest("POST", "/register", nil), "second@example.invalid", "long-local-password")
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.grantService().GetGrant(context.Background(), commerce.CustomerID(account.ID), TPAHubProductID, CapabilityRepositoryPublish, s.currentTime())
	if err != nil || g.Limits.StorageBytes != 100 || g.OfferID != "local-policy-v1" {
		t.Fatalf("new account did not use current operator policy: %+v %v", g, err)
	}
}

func TestLocalPolicyFactsIndependentOfMeasurementEnforcementAndBilling(t *testing.T) {
	var original []commerce.Grant
	for _, mode := range localDeliveryModes {
		t.Run(mode, func(t *testing.T) {
			p := provisioning.LocalPolicy{StorageBytes: 100, EgressBytes: 0, PrivateRead: true}
			s, _ := configuredTestServer(t, func(s *server) {
				s.cfg.CapabilitySource = config.CapabilitiesLocal
				s.cfg.LocalPolicy = &p
				s.cfg.BillingIntegration = config.BillingNone
				s.cfg.BillingProvider = "none"
				s.cfg.DeliveryMode = mode
				s.now = func() time.Time { return deliveryTestTime }
			})
			created := "2024-01-01T00:00:00Z"
			grants, err := s.initialTPAGrants(created)
			if err != nil {
				t.Fatal(err)
			}
			if original == nil {
				original = grants
			} else if !reflect.DeepEqual(original, grants) {
				t.Fatal("mode changed grant facts")
			}
			for _, billing := range []string{config.BillingHosted, config.BillingNone, config.BillingSimulator} {
				s.cfg.BillingIntegration = billing
				s.cfg.BillingPurchasesEnabled = true
				got, err := s.initialTPAGrants(created)
				if err != nil || !reflect.DeepEqual(grants, got) {
					t.Fatal("commercial config changed explicit local facts", err)
				}
			}
			s.cfg.BillingIntegration = config.BillingNone
			s.cfg.BillingPurchasesEnabled = false
			if err := s.db.CreateAccount(context.Background(), store.Account{ID: "local", Email: "local@invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: created}, grants...); err != nil {
				t.Fatal(err)
			}
			if err := s.db.CreateRepository(context.Background(), store.Repository{ID: deliveryTestRepo, AccountID: "local", Name: "local", Visibility: "public", CreatedAt: created}); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(s.cfg.DataDir, "repos", deliveryTestRepo)
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "package.deb"), []byte("0123456789"), 0644); err != nil {
				t.Fatal(err)
			}
			before := persistedProvisioningFacts(t, s.cfg.DataDir)
			w := deliveryRequest(s, "GET", "package.deb", nil)
			if w.Code != 200 || w.Body.String() != "0123456789" {
				t.Fatalf("delivery=%d %s", w.Code, w.Body.String())
			}
			strict := mode == config.DeliveryStrictAccounted
			if (w.Header().Get("X-TPAHub-Egress-Limited") != "") != strict {
				t.Fatal("zero finite allowance did not select expected action")
			}
			bytes, err := s.db.RepositoryEgress(context.Background(), deliveryTestRepo, "2026-08")
			if err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if strict {
				want = 10
			}
			if bytes != want {
				t.Fatalf("accounted=%d want=%d", bytes, want)
			}
			view, err := s.readDeliveryUsage("local", deliveryTestTime)
			if err != nil {
				t.Fatal(err)
			}
			if view.Measured != (mode != config.DeliveryUnmetered) {
				t.Fatal("unexpected measurement")
			}
			if mode == config.DeliveryMetricsOnly && (view.Bytes != 10 || view.Requests != 1) {
				t.Fatalf("facts=%+v", view)
			}
			if after := persistedProvisioningFacts(t, s.cfg.DataDir); after != before {
				t.Fatal("runtime modified grant facts")
			}
		})
	}
}

func TestInvalidProvisioningFailsBeforeState(t *testing.T) {
	for _, cfg := range []config.Config{{CapabilitySource: "remote"}, {CapabilitySource: config.CapabilitiesLocal, LocalPolicy: &provisioning.LocalPolicy{StorageBytes: -1}}} {
		cfg.DataDir = filepath.Join(t.TempDir(), "must-not-exist")
		if err := run(cfg); err == nil {
			t.Fatal("invalid provisioning accepted")
		}
		if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
			t.Fatal("invalid policy opened state")
		}
	}
}
