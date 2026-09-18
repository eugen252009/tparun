package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/enforcement"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/usage"
	"time"
)

func enforcementFixture(t *testing.T, mode, selected, visibility string, consumers ...*usage.Mailbox) (*server, string, *unusedPaymentProvider, *testCommercialService) {
	t.Helper()
	payment, commercial := &unusedPaymentProvider{}, &testCommercialService{}
	s, _ := configuredTestServer(t, func(s *server) {
		s.cfg.DeliveryMode = mode
		s.cfg.EgressEnforcement = selected
		s.cfg.BillingIntegration = config.BillingNone
		s.cfg.BillingProvider = "none"
		s.now = func() time.Time { return deliveryTestTime }
		s.usageConsumers = consumers
		s.purchaseProvider = payment
		s.commercial = commercial
	})
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: deliveryTestRepo, AccountID: "a", Name: "strategy", Visibility: visibility, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", deliveryTestRepo)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.deb"), []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}
	return s, root, payment, commercial
}

func TestExplicitNoEnforcementIgnoresAccountingAuthority(t *testing.T) {
	for _, mode := range []string{config.DeliveryUnmetered, config.DeliveryMetricsOnly, config.DeliveryStrictAccounted} {
		t.Run(mode, func(t *testing.T) {
			consumer := usage.NewMailbox(2)
			s, _, payment, billing := enforcementFixture(t, mode, config.EnforcementNone, "public", consumer)
			// Historical strict uncertainty stays intact but must not gate unenforced delivery.
			if err := s.db.BeginEgressDelivery(context.Background(), "historical", deliveryTestRepo, deliveryTestTime.Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			if err := s.db.MarkEgressDeliveryUncertain(context.Background(), "historical", deliveryTestRepo, "test uncertainty"); err != nil {
				t.Fatal(err)
			}
			var lookup, begin, reserve, complete int
			s.egressLookup = func(string, time.Time) (int64, int64, error) { lookup++; return 0, 0, errors.New("accounting offline") }
			s.beginEgressDelivery = func(context.Context, string, string, string) error { begin++; return errors.New("unavailable") }
			s.reserveEgressDelivery = func(context.Context, string, string, string, string, int64, int64, string) (bool, error) {
				reserve++
				return false, errors.New("unavailable")
			}
			s.completeEgressDelivery = func(context.Context, string, string, string, int64, string) error {
				complete++
				return errors.New("unavailable")
			}
			version := databaseVersionReader(t, s)
			before := version()
			w := deliveryRequest(s, "GET", "package.deb", nil)
			if w.Code != 200 || w.Body.String() != "0123456789" || w.Header().Get("X-TPAHub-Egress-Limited") != "" {
				t.Fatalf("unenforced response=%d %q", w.Code, w.Body.String())
			}
			if version() != before || lookup+begin+reserve+complete != 0 || payment.calls.Load() != 0 || billing.calls != 0 {
				t.Fatal("none consulted/mutated an accounting or commercial participant")
			}
			markers, err := s.db.EgressDeliveries(context.Background(), "a")
			if err != nil || len(markers) != 1 || markers[0].ID != "historical" || markers[0].State != "uncertain" {
				t.Fatalf("historical authority changed: %+v %v", markers, err)
			}
			view, err := s.readDeliveryUsage("a", deliveryTestTime)
			if err != nil {
				t.Fatal(err)
			}
			if mode == config.DeliveryUnmetered {
				if view.Measured || len(consumer.Events()) != 0 {
					t.Fatal("none forced measurement")
				}
			} else {
				if view.Bytes != 10 || view.Requests != 1 || view.Enforced {
					t.Fatalf("metrics=%+v", view)
				}
				if got := <-consumer.Events(); got.Bytes != 10 {
					t.Fatalf("observation=%+v", got)
				}
			}
		})
	}
}

func TestExplicitEnforcementPreservesAuthorizationAndGeneration(t *testing.T) {
	for _, strategy := range []string{config.EnforcementNone, config.EnforcementStrictAccounted} {
		t.Run(strategy, func(t *testing.T) {
			s, root, _, _ := enforcementFixture(t, config.DeliveryMetricsOnly, strategy, "private")
			for generation, body := range map[string]string{"old": "authorized", "new": "uncommitted"} {
				dir := filepath.Join(root, "generations", generation)
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "package.deb"), []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.db.SetActiveGeneration(context.Background(), deliveryTestRepo, "old", "now"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("generations/new", filepath.Join(root, "current")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "generations", "new", "package.deb"), filepath.Join(root, "generations", "old", "escape.deb")); err != nil {
				t.Fatal(err)
			}
			if err := s.db.CreateCredential(context.Background(), store.ReaderCredential{ID: "reader", RepositoryID: deliveryTestRepo, Name: "test"}, store.HashToken("reader-test")); err != nil {
				t.Fatal(err)
			}
			privateChecks := 0
			s.grants = grantServiceFunc(func(ctx context.Context, customer commerce.CustomerID, product commerce.ProductID, capability commerce.CapabilityID, now time.Time) (commerce.Grant, error) {
				if capability == CapabilityRepositoryReadPrivate {
					privateChecks++
				}
				return s.db.GetGrant(ctx, customer, product, capability, now)
			})
			for _, password := range []string{"", "wrong", "reader-test"} {
				req := httptest.NewRequest("GET", "/r/"+deliveryTestRepo+"/package.deb", nil)
				if password != "" {
					req.SetBasicAuth("reader", password)
				}
				w := httptest.NewRecorder()
				s.routes().ServeHTTP(w, req)
				expected := 401
				if password == "reader-test" {
					expected = 200
				}
				if w.Code != expected {
					t.Fatalf("reader authorization=%d want=%d", w.Code, expected)
				}
				if expected == 200 && w.Body.String() != "authorized" {
					t.Fatal("pointer displaced durable generation authority")
				}
			}
			if privateChecks != 3 {
				t.Fatalf("private grant checks=%d", privateChecks)
			}
			req := httptest.NewRequest("GET", "/r/"+deliveryTestRepo+"/escape.deb", nil)
			req.SetBasicAuth("reader", "reader-test")
			w := httptest.NewRecorder()
			s.routes().ServeHTTP(w, req)
			if w.Code != 404 {
				t.Fatal("enforcement selection bypassed rooted safety")
			}
		})
	}
}

func TestEnforcementChangesActionsNotMeasuredFacts(t *testing.T) {
	var expected usage.Delivery
	for _, strategy := range []string{config.EnforcementNone, config.EnforcementStrictAccounted} {
		t.Run(strategy, func(t *testing.T) {
			consumer := usage.NewMailbox(2)
			s, _, payment, billing := enforcementFixture(t, config.DeliveryMetricsOnly, strategy, "public", consumer)
			lookups := 0
			s.egressLookup = func(string, time.Time) (int64, int64, error) { lookups++; return 0, 0, nil } // finite zero, not disabled
			w := deliveryRequest(s, "GET", "package.deb", map[string]string{"Range": "bytes=2-5"})
			if w.Code != 206 || w.Body.String() != "2345" {
				t.Fatalf("response=%d %q", w.Code, w.Body.String())
			}
			fact := <-consumer.Events()
			if fact.Bytes != 4 || fact.Status != 206 {
				t.Fatalf("fact=%+v", fact)
			}
			if strategy == config.EnforcementNone {
				expected = fact
			} else if fact != expected {
				t.Fatalf("enforcement changed facts: %+v / %+v", expected, fact)
			}
			accounted, err := s.db.RepositoryEgress(context.Background(), deliveryTestRepo, "2026-08")
			if err != nil {
				t.Fatal(err)
			}
			limited := w.Header().Get("X-TPAHub-Egress-Limited")
			if strategy == config.EnforcementNone {
				if lookups != 0 || limited != "" || accounted != 0 {
					t.Fatal("none queried/enforced/accounted a finite allowance")
				}
			} else if lookups != 1 || limited != "true" || accounted != 4 {
				t.Fatalf("strict lookup=%d limited=%q accounted=%d", lookups, limited, accounted)
			}
			if payment.calls.Load() != 0 || billing.calls != 0 {
				t.Fatal("enforcement consulted a commercial participant")
			}
		})
	}
}

func TestEnforcementGuardFreezesSettlementAfterPreparation(t *testing.T) {
	first, second := usage.NewMailbox(2), usage.NewMailbox(2)
	s, _, _, _ := enforcementFixture(t, config.DeliveryMetricsOnly, config.EnforcementStrictAccounted, "public", first, second)
	var calls []string
	s.beginEgressDelivery = func(ctx context.Context, id, repo, at string) error {
		calls = append(calls, "begin")
		return s.db.BeginEgressDelivery(ctx, id, repo, at)
	}
	s.reserveEgressDelivery = func(ctx context.Context, id, repo, start, end string, size, limit int64, at string) (bool, error) {
		calls = append(calls, "reserve")
		return s.db.ReserveEgressDelivery(ctx, id, repo, start, end, size, limit, at)
	}
	s.completeEgressDelivery = func(ctx context.Context, id, repo, at string, n int64, finished string) error {
		if ctx.Err() != nil {
			t.Error("settlement inherited cancelled request")
		}
		calls = append(calls, "complete")
		return s.db.CompleteEgressDelivery(ctx, id, repo, at, n, finished)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := observedPartialWriter{httptest.NewRecorder(), func() {
		calls = append(calls, "write")
		// Mutate configuration and the source callback after Prepare. The captured
		// guard, not either replacement, must finish this transfer.
		s.cfg.EgressEnforcement = config.EnforcementNone
		s.cfg.DeliveryMode = config.DeliveryUnmetered
		s.completeEgressDelivery = func(context.Context, string, string, string, int64, string) error {
			t.Error("replacement settlement called")
			return errors.New("wrong backend")
		}
		cancel()
	}}
	req := httptest.NewRequest("GET", "/r/"+deliveryTestRepo+"/package.deb", nil).WithContext(ctx)
	s.routes().ServeHTTP(writer, req)
	if !reflect.DeepEqual(calls, []string{"begin", "reserve", "write", "complete"}) {
		t.Fatalf("lifecycle=%v", calls)
	}
	a, b := <-first.Events(), <-second.Events()
	if a != b || a.Bytes != 3 || !a.WriteFailed || !a.Interrupted || len(first.Events()) != 0 || len(second.Events()) != 0 {
		t.Fatalf("facts=%+v %+v", a, b)
	}
	n, err := s.db.RepositoryEgress(context.Background(), deliveryTestRepo, "2026-08")
	if err != nil || n != 3 {
		t.Fatalf("settlement=%d %v", n, err)
	}
	markers, err := s.db.EgressDeliveries(context.Background(), "a")
	if err != nil || len(markers) != 0 {
		t.Fatalf("uncertainty=%+v %v", markers, err)
	}
}

func TestInvalidEnforcementFailsBeforeOpeningState(t *testing.T) {
	for _, tc := range [][2]string{{config.DeliveryMetricsOnly, "local-quota"}, {config.DeliveryUnmetered, config.EnforcementStrictAccounted}} {
		dir := filepath.Join(t.TempDir(), "absent")
		if err := run(config.Config{DataDir: dir, DeliveryMode: tc[0], EgressEnforcement: tc[1]}); err == nil {
			t.Fatal("invalid strategy accepted")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("invalid selection touched state")
		}
	}
}

type strategyFunc func(context.Context, enforcement.Request) (enforcement.Guard, error)

func (f strategyFunc) Prepare(ctx context.Context, r enforcement.Request) (enforcement.Guard, error) {
	return f(ctx, r)
}

type duplicateCompletionGuard struct{ enforcement.Guard }

func (g duplicateCompletionGuard) Complete(n int64) { g.Guard.Complete(n); g.Guard.Complete(n) }

type abortingWrapGuard struct{ enforcement.Guard }

func (g abortingWrapGuard) Wrap(http.ResponseWriter) http.ResponseWriter { panic(http.ErrAbortHandler) }

func TestStrictGuardCompletesOnceIncludingAbnormalUnwind(t *testing.T) {
	for _, abort := range []bool{false, true} {
		t.Run(map[bool]string{false: "duplicate-completion", true: "abnormal-wrap"}[abort], func(t *testing.T) {
			consumer := usage.NewMailbox(2)
			s, _, _, _ := enforcementFixture(t, config.DeliveryMetricsOnly, config.EnforcementStrictAccounted, "public", consumer)
			calls := 0
			s.completeEgressDelivery = func(ctx context.Context, id, repo, at string, n int64, finished string) error {
				calls++
				return s.db.CompleteEgressDelivery(ctx, id, repo, at, n, finished)
			}
			original := s.delivery.enforcement
			// Static Go composition of a test decorator, not a plugin loader or config mode.
			s.delivery.enforcement = strategyFunc(func(ctx context.Context, r enforcement.Request) (enforcement.Guard, error) {
				guard, err := original.Prepare(ctx, r)
				if err != nil {
					return nil, err
				}
				if abort {
					return abortingWrapGuard{guard}, nil
				}
				return duplicateCompletionGuard{guard}, nil
			})
			didPanic := false
			func() {
				defer func() {
					if p := recover(); p != nil {
						if p != http.ErrAbortHandler {
							t.Fatalf("unexpected panic: %v", p)
						}
						didPanic = true
					}
				}()
				deliveryRequest(s, "GET", "package.deb", nil)
			}()
			if didPanic != abort || calls != 1 {
				t.Fatalf("panic=%t completions=%d", didPanic, calls)
			}
			fact := <-consumer.Events()
			expected := int64(10)
			if abort {
				expected = 0
			}
			if fact.Bytes != expected || fact.Interrupted != abort || len(consumer.Events()) != 0 {
				t.Fatalf("fact=%+v", fact)
			}
			n, err := s.db.RepositoryEgress(context.Background(), deliveryTestRepo, "2026-08")
			if err != nil || n != expected {
				t.Fatalf("ledger=%d %v", n, err)
			}
			markers, err := s.db.EgressDeliveries(context.Background(), "a")
			if err != nil || len(markers) != 0 {
				t.Fatalf("markers=%+v %v", markers, err)
			}
		})
	}
}

func TestUnknownStrategyFailureIsFailClosed(t *testing.T) {
	consumer := usage.NewMailbox(2)
	s, _, _, _ := enforcementFixture(t, config.DeliveryMetricsOnly, config.EnforcementNone, "public", consumer)
	s.delivery.enforcement = strategyFunc(func(context.Context, enforcement.Request) (enforcement.Guard, error) {
		return nil, errors.New("internal policy diagnostics")
	})
	version := databaseVersionReader(t, s)
	before := version()
	w := deliveryRequest(s, "GET", "package.deb", nil)
	if w.Code != 503 || w.Body.String() != "delivery enforcement unavailable\n" || version() != before || len(consumer.Events()) != 0 {
		t.Fatalf("failure=%d %q", w.Code, w.Body.String())
	}
}
