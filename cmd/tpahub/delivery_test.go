package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/usage"
)

var deliveryTestTime = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

const deliveryTestRepo = "12121212121212121212121212121212"

var localDeliveryModes = []string{config.DeliveryUnmetered, config.DeliveryMetricsOnly, config.DeliveryStrictAccounted}

type unusedPaymentProvider struct{ calls atomic.Int64 }

func (p *unusedPaymentProvider) CreateCheckout(context.Context, payments.Operation) (payments.CheckoutSession, error) {
	p.calls.Add(1)
	return payments.CheckoutSession{}, errors.New("payment must not be called for usage")
}

func deliveryFixture(t *testing.T, mode string, consumers ...*usage.Mailbox) (*server, string, *unusedPaymentProvider) {
	t.Helper()
	provider := &unusedPaymentProvider{}
	s, _ := configuredTestServer(t, func(s *server) {
		s.cfg.DeliveryMode = mode
		s.cfg.BillingProvider = "none"
		s.cfg.BillingRateCard = "does-not-exist"
		s.now = func() time.Time { return deliveryTestTime }
		s.usageConsumers = consumers
		s.purchaseProvider = provider
	})
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: deliveryTestRepo, AccountID: "a", Name: "delivery", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", deliveryTestRepo)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.deb"), []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}
	return s, root, provider
}

func deliveryRequest(s *server, method, suffix string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/r/"+deliveryTestRepo+"/"+suffix, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

// data_version observes commits made by the real Store connection, including a
// transient Begin+Complete that would disappear from a final logical digest.
func databaseVersionReader(t *testing.T, s *server) func() int64 {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(s.cfg.DataDir, "tpahub.db"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); db.Close() })
	return func() int64 {
		t.Helper()
		var n int64
		if err := conn.QueryRowContext(context.Background(), "PRAGMA data_version").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
}
func assertEmptyAccounting(t *testing.T, s *server) {
	t.Helper()
	markers, err := s.db.EgressDeliveries(context.Background(), "a")
	if err != nil || len(markers) != 0 {
		t.Fatalf("delivery markers=%+v err=%v", markers, err)
	}
	n, err := s.db.RepositoryEgress(context.Background(), deliveryTestRepo, "2026-08")
	if err != nil || n != 0 {
		t.Fatalf("accounted bytes=%d err=%v", n, err)
	}
}

func TestLocalDeliveryUnmeteredAndMetricsNeverAccount(t *testing.T) {
	for _, mode := range localDeliveryModes[:2] {
		t.Run(mode, func(t *testing.T) {
			consumer := usage.NewMailbox(4)
			s, _, payment := deliveryFixture(t, mode, consumer)
			var begins, reserves, completes atomic.Int64
			s.beginEgressDelivery = func(context.Context, string, string, string) error {
				begins.Add(1)
				return errors.New("unexpected begin")
			}
			s.reserveEgressDelivery = func(context.Context, string, string, string, string, int64, int64, string) (bool, error) {
				reserves.Add(1)
				return false, errors.New("unexpected reserve")
			}
			s.completeEgressDelivery = func(context.Context, string, string, string, int64, string) error {
				completes.Add(1)
				return errors.New("unexpected complete")
			}
			// A broken accounting authority is irrelevant to these explicitly non-strict modes.
			s.egressLookup = func(string, time.Time) (int64, int64, error) {
				t.Error("egress enforcement called")
				return 0, 0, errors.New("unavailable")
			}
			version := databaseVersionReader(t, s)
			before := version()
			w := deliveryRequest(s, "GET", "package.deb", nil)
			if w.Code != 200 || w.Body.String() != "0123456789" || w.Header().Get("X-TPAHub-Egress-Limited") != "" {
				t.Fatalf("response=%d %q %v", w.Code, w.Body.String(), w.Header())
			}
			if version() != before || begins.Load() != 0 || reserves.Load() != 0 || completes.Load() != 0 || payment.calls.Load() != 0 {
				t.Fatal("delivery performed accounting/payment effects")
			}
			assertEmptyAccounting(t, s)
			view, err := s.readDeliveryUsage("a", deliveryTestTime)
			if err != nil {
				t.Fatal(err)
			}
			if mode == config.DeliveryUnmetered {
				if view.Measured || s.delivery.local != nil || len(consumer.Events()) != 0 {
					t.Fatal("unmetered delivery measured")
				}
			} else {
				fact := <-consumer.Events()
				if fact.Bytes != 10 || fact.RepositoryID != deliveryTestRepo || fact.AccountID != "a" || fact.Method != "GET" || fact.Status != 200 || fact.StartedAt != deliveryTestTime || fact.FinishedAt != deliveryTestTime {
					t.Fatalf("fact=%+v", fact)
				}
				if view.Bytes != 10 || view.Requests != 1 || !strings.Contains(view.Status, "Volatile") {
					t.Fatalf("read model=%+v", view)
				}
			}
		})
	}
}

func TestLocalDeliveryMetricsHTTPSemantics(t *testing.T) {
	s, root, _ := deliveryFixture(t, config.DeliveryMetricsOnly)
	info, err := os.Stat(filepath.Join(root, "package.deb"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method  string
		headers map[string]string
		status  int
		bytes   int64
	}{
		{"GET", nil, 200, 10},
		{"GET", map[string]string{"Range": "bytes=2-5"}, 206, 4},
		{"HEAD", nil, 200, 0},
		{"GET", map[string]string{"If-Modified-Since": info.ModTime().UTC().Format(http.TimeFormat)}, 304, 0},
		{"GET", map[string]string{"Range": "bytes=999-"}, 416, -1},
	}
	var total int64
	for i, tc := range cases {
		w := deliveryRequest(s, tc.method, "package.deb", tc.headers)
		if w.Code != tc.status {
			t.Fatalf("case %d status=%d", i, w.Code)
		}
		n := int64(w.Body.Len())
		if tc.bytes >= 0 && n != tc.bytes {
			t.Fatalf("case %d bytes=%d", i, n)
		}
		// Compatibility: error bodies generated after admission are written bytes too.
		total += n
		view, err := s.readDeliveryUsage("a", deliveryTestTime)
		if err != nil || view.Bytes != total || view.Requests != uint64(i+1) {
			t.Fatalf("view=%+v err=%v", view, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "empty.deb"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if w := deliveryRequest(s, "GET", "empty.deb", nil); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("empty=%+v", w)
	}
	before := s.delivery.local.Account("a")
	if w := deliveryRequest(s, "GET", "missing.deb", nil); w.Code != 404 {
		t.Fatalf("missing status=%d", w.Code)
	}
	if got := s.delivery.local.Account("a"); got != before {
		t.Fatalf("pre-admission error counted: %+v", got)
	}
	assertEmptyAccounting(t, s)
}

type observedPartialWriter struct {
	*httptest.ResponseRecorder
	beforeWrite func()
}

func (w observedPartialWriter) Write(p []byte) (int, error) {
	if w.beforeWrite != nil {
		w.beforeWrite()
	}
	n := min(3, len(p))
	w.ResponseRecorder.Write(p[:n])
	return n, context.Canceled
}
func TestLocalDeliveryActualPartialWritesAndStrictOrdering(t *testing.T) {
	for _, mode := range []string{config.DeliveryMetricsOnly, config.DeliveryStrictAccounted} {
		t.Run(mode, func(t *testing.T) {
			first, second := usage.NewMailbox(2), usage.NewMailbox(2)
			s, _, _ := deliveryFixture(t, mode, first, second)
			var events []string
			s.beginEgressDelivery = func(ctx context.Context, id, repo, at string) error {
				events = append(events, "begin")
				return s.db.BeginEgressDelivery(ctx, id, repo, at)
			}
			s.reserveEgressDelivery = func(ctx context.Context, id, repo, start, end string, n, limit int64, at string) (bool, error) {
				events = append(events, "reserve")
				return s.db.ReserveEgressDelivery(ctx, id, repo, start, end, n, limit, at)
			}
			var id, at string
			s.completeEgressDelivery = func(ctx context.Context, delivery, repo, usageAt string, n int64, finished string) error {
				events = append(events, "complete")
				id, at = delivery, usageAt
				if n != 3 {
					t.Errorf("settlement bytes=%d", n)
				}
				return s.db.CompleteEgressDelivery(ctx, delivery, repo, usageAt, n, finished)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := observedPartialWriter{httptest.NewRecorder(), func() {
				cancel() // transfer cancellation after reservation must not cancel settlement
				if mode == config.DeliveryStrictAccounted {
					markers, err := s.db.EgressDeliveries(context.Background(), "a")
					if err != nil || len(markers) != 1 || markers[0].ReservedBytes != 10 {
						t.Fatalf("reservation before write=%+v err=%v", markers, err)
					}
				}
				events = append(events, "write")
			}}
			s.routes().ServeHTTP(w, httptest.NewRequest("GET", "/r/"+deliveryTestRepo+"/package.deb", nil).WithContext(ctx))
			a, b := <-first.Events(), <-second.Events()
			if a != b || a.Bytes != 3 || !a.WriteFailed || !a.Interrupted || len(first.Events()) != 0 || len(second.Events()) != 0 {
				t.Fatalf("observations=%+v %+v", a, b)
			}
			if mode == config.DeliveryStrictAccounted {
				if !reflect.DeepEqual(events, []string{"begin", "reserve", "write", "complete"}) {
					t.Fatalf("order=%v", events)
				}
				n, err := s.db.RepositoryEgress(context.Background(), deliveryTestRepo, "2026-08")
				if err != nil || n != 3 {
					t.Fatalf("double accounting: %d %v", n, err)
				}
				if err := s.db.CompleteEgressDelivery(context.Background(), id, deliveryTestRepo, at, 3, at); err == nil {
					t.Fatal("second settlement accepted")
				}
				n, _ = s.db.RepositoryEgress(context.Background(), deliveryTestRepo, "2026-08")
				if n != 3 {
					t.Fatal("duplicate settlement changed amount")
				}
			} else {
				assertEmptyAccounting(t, s)
			}
		})
	}
}

func TestLocalDeliveryConsumersNeverBlockAndPolicyIsFrozen(t *testing.T) {
	slow, fast := usage.NewMailbox(1), usage.NewMailbox(4)
	s, _, _ := deliveryFixture(t, config.DeliveryMetricsOnly, slow, fast)
	// Changing the source Config after composition is NOT a runtime mode switch.
	s.cfg.DeliveryMode = config.DeliveryStrictAccounted
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			deliveryRequest(s, "GET", "package.deb", nil)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("slow consumer blocked delivery")
	}
	if slow.Dropped() != 2 || fast.Dropped() != 0 || len(fast.Events()) != 3 {
		t.Fatalf("fanout drops=%d/%d fast=%d", slow.Dropped(), fast.Dropped(), len(fast.Events()))
	}
	if got := s.delivery.local.Account("a"); got.Bytes != 30 || got.Requests != 3 {
		t.Fatalf("local=%+v", got)
	}
	assertEmptyAccounting(t, s)
}

func TestLocalDeliveryAuthorizationAndFileAuthorityAllModes(t *testing.T) {
	for _, mode := range localDeliveryModes {
		t.Run(mode, func(t *testing.T) {
			s, root, _ := deliveryFixture(t, mode)
			// Simulate the existing pre-commit pointer-swap window. Only the DB generation wins.
			for name, body := range map[string]string{"old": "authorized", "new": "uncommitted"} {
				dir := filepath.Join(root, "generations", name)
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
			if w := deliveryRequest(s, "GET", "package.deb", nil); w.Code != 200 || w.Body.String() != "authorized" {
				t.Fatalf("authority=%d %q", w.Code, w.Body.String())
			}
			if err := os.Symlink(filepath.Join(root, "generations", "new", "package.deb"), filepath.Join(root, "generations", "old", "escape.deb")); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"escape.deb", "../../../tpahub.db", "generations/new/package.deb", "missing"} {
				w := deliveryRequest(s, "GET", path, nil)
				if w.Code == 200 {
					t.Fatalf("unsafe path %q served", path)
				}
			}
			// Change visibility only in this disposable fixture, using SQL because there is
			// deliberately no new repository administration feature in this extraction.
			db, err := sql.Open("sqlite", filepath.Join(s.cfg.DataDir, "tpahub.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("UPDATE repositories SET visibility='private' WHERE id=?", deliveryTestRepo); err != nil {
				t.Fatal(err)
			}
			if err := s.db.CreateCredential(context.Background(), store.ReaderCredential{ID: "reader", RepositoryID: deliveryTestRepo, Name: "test", TokenPrefix: "test"}, store.HashToken("test-reader")); err != nil {
				t.Fatal(err)
			}
			for _, password := range []string{"", "wrong", "test-reader"} {
				r := httptest.NewRequest("GET", "/r/"+deliveryTestRepo+"/package.deb", nil)
				if password != "" {
					r.SetBasicAuth("reader", password)
				}
				w := httptest.NewRecorder()
				s.routes().ServeHTTP(w, r)
				expected := 401
				if password == "test-reader" {
					expected = 200
				}
				if w.Code != expected {
					t.Fatalf("reader %q status=%d want=%d", password, w.Code, expected)
				}
			}
			// Exercise ownership through the real authenticated administration route.
			if err := s.db.CreateAccount(context.Background(), store.Account{ID: "foreign", Email: "foreign@example.invalid", PasswordHash: "x", Status: "active", CreatedAt: "2024-01-01T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
			if err := s.db.CreateSession(context.Background(), store.Session{ID: "foreign-session", AccountID: "foreign", TokenPrefix: "foreign"}, store.HashToken("foreign-token")); err != nil {
				t.Fatal(err)
			}
			for _, who := range []string{"a", "foreign"} {
				r := httptest.NewRequest("GET", "/v1/repositories/"+deliveryTestRepo+"/reader-credentials", nil)
				token := "tph_s_test"
				if who == "foreign" {
					token = "foreign-token"
				}
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				s.routes().ServeHTTP(w, r)
				if who == "foreign" && w.Code != 404 {
					t.Fatalf("foreign publication=%d", w.Code)
				}
				if who == "a" && w.Code != 200 {
					t.Fatalf("owner status=%d", w.Code)
				}
			}
			grant, err := s.db.GetGrant(context.Background(), "a", "tpa-hub", CapabilityRepositoryReadPrivate, deliveryTestTime)
			if err != nil {
				t.Fatal(err)
			}
			grant.Enabled = false
			if err := s.db.PutGrant(context.Background(), grant); err != nil {
				t.Fatal(err)
			}
			denied := httptest.NewRequest("GET", "/r/"+deliveryTestRepo+"/package.deb", nil)
			denied.SetBasicAuth("reader", "test-reader")
			deniedResponse := httptest.NewRecorder()
			s.routes().ServeHTTP(deniedResponse, denied)
			if deniedResponse.Code != 403 {
				t.Fatalf("disabled private grant=%d", deniedResponse.Code)
			}
			if err := s.db.SetAccountStatus(context.Background(), "a", "suspended"); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/v1/repositories/"+deliveryTestRepo+"/publishes?filename=x.deb", strings.NewReader("x"))
			r.Header.Set("Authorization", "Bearer tph_s_test")
			w := httptest.NewRecorder()
			s.routes().ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("suspended publication=%d", w.Code)
			}
		})
	}
}

func TestLocalUsageDashboardIndependentAndPreviewExplicit(t *testing.T) {
	for _, mode := range localDeliveryModes {
		t.Run(mode, func(t *testing.T) {
			s, _, payment := deliveryFixture(t, mode)
			deliveryRequest(s, "GET", "package.deb", nil)
			version := databaseVersionReader(t, s)
			before := version()
			render := func(query string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", "/account"+query, nil)
				r = r.WithContext(context.WithValue(r.Context(), accountKey, "a"))
				w := httptest.NewRecorder()
				s.routes().ServeHTTP(w, r)
				return w
			}
			if w := render("?billing=preview"); strings.Contains(w.Body.String(), "virtual usage preview") {
				t.Fatal("disabled billing prepared a preview")
			}
			for _, provider := range []string{"none", "mock", "stripe"} {
				s.cfg.BillingProvider = provider // billing selection cannot change the meter
				w := render("")
				if w.Code != 200 || !strings.Contains(w.Body.String(), "Storage") || strings.Contains(w.Body.String(), "Egress usage unavailable") || strings.Contains(w.Body.String(), "virtual usage preview") {
					t.Fatalf("dashboard=%d %s", w.Code, w.Body.String())
				}
				if mode == config.DeliveryMetricsOnly && (!strings.Contains(w.Body.String(), "10 B") || !strings.Contains(w.Body.String(), "reset on restart")) {
					t.Fatal("local usage not displayed")
				}
			}
			if version() != before || payment.calls.Load() != 0 {
				t.Fatal("basic dashboard mutated state or called payment")
			}
			// Existing mock preview is still available, but only by explicit request.
			s.cfg.BillingProvider = "mock"
			w := render("?billing=preview")
			if w.Code != 200 || !strings.Contains(w.Body.String(), "virtual usage preview") {
				t.Fatalf("explicit preview=%s", w.Body.String())
			}
			if version() == before {
				t.Fatal("explicit preview did not exercise existing persistence")
			}
			if payment.calls.Load() != 0 {
				t.Fatal("usage preview called payment")
			}
		})
	}
}

func TestLocalDeliveryBillingDoesNotChangeMeasurementAndRestartResets(t *testing.T) {
	s, _, payment := deliveryFixture(t, config.DeliveryMetricsOnly)
	for _, provider := range []string{"none", "mock", "stripe"} {
		s.cfg.BillingProvider = provider
		before := s.delivery.local.Account("a")
		w := deliveryRequest(s, "GET", "package.deb", nil)
		after := s.delivery.local.Account("a")
		if w.Code != 200 || after.Bytes-before.Bytes != 10 {
			t.Fatalf("%s changes measurement", provider)
		}
	}
	// Reconstruct the production composition over the same durable Store.
	restarted := &server{cfg: s.cfg, db: s.db, now: s.now}
	if err := restarted.initDelivery(); err != nil {
		t.Fatal(err)
	}
	v, err := restarted.readDeliveryUsage("a", deliveryTestTime)
	if err != nil || v.Bytes != 0 || v.Requests != 0 {
		t.Fatalf("volatile restart=%+v %v", v, err)
	}
	if payment.calls.Load() != 0 {
		t.Fatal("metering called payment")
	}
	assertEmptyAccounting(t, s)
}

func TestLocalDeliveryConfigurationValidation(t *testing.T) {
	for _, mode := range localDeliveryModes {
		cfg := config.Config{DeliveryMode: mode, BillingProvider: "none", BillingRateCard: "not-resolved"}
		if err := ValidateBillingConfiguration(cfg); err != nil {
			t.Fatal(err)
		}
		cfg.BillingPurchasesEnabled = true
		if err := ValidateBillingConfiguration(cfg); err == nil {
			t.Fatal("disabled billing enabled purchases")
		}
	}
	dir := filepath.Join(t.TempDir(), "must-not-be-created")
	if err := run(config.Config{DataDir: dir, DeliveryMode: "reported"}); err == nil {
		t.Fatal("unimplemented mode accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("invalid mode touched data dir: %v", err)
	}
	s := &server{cfg: config.Config{DeliveryMode: "invalid"}}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal("invalid composition did not fail closed")
	}
}

func TestLocalStorageObservationUsesCanonicalGauge(t *testing.T) {
	s, _ := testServer(t)
	root := generationUsageFixture(t, s, deliveryTestRepo, map[string][]int{"old": {10}, "new": {3}}, "old")
	repo, err := s.db.Repository(context.Background(), deliveryTestRepo)
	if err != nil {
		t.Fatal(err)
	}
	_, a, err := s.repositoryStorageObservation(repo)
	if err != nil || a.Bytes != 10 || a.Generation != "old" || a.AccountID != "a" || a.Scope != usage.PublishedPackagePayloads {
		t.Fatalf("storage=%+v %v", a, err)
	}
	setCurrentGeneration(t, s, deliveryTestRepo, root, "new")
	_, b, err := s.repositoryStorageObservation(repo)
	if err != nil || b.Bytes != 3 || b.Generation != "new" {
		t.Fatalf("storage=%+v %v", b, err)
	}
	account, err := s.accountUsage(context.Background(), "a")
	if err != nil || account.StoredBytes != 3 {
		t.Fatalf("storage gauges summed across observations: %+v %v", account, err)
	}
}

func TestLocalUsageOperatorDashboardReadsMetrics(t *testing.T) {
	s, _, payment := deliveryFixture(t, config.DeliveryMetricsOnly)
	// Historical strict bytes must not be mislabeled as this process's metrics.
	if err := s.db.AddRepositoryEgressDaily(context.Background(), deliveryTestRepo, "2026-08-30", 999, "now"); err != nil {
		t.Fatal(err)
	}
	deliveryRequest(s, "GET", "package.deb", nil)
	token, err := s.newOperatorSession()
	if err != nil {
		t.Fatal(err)
	}
	version := databaseVersionReader(t, s)
	before := version()
	r := httptest.NewRequest("GET", "/ops?scope=all", nil)
	r.AddCookie(&http.Cookie{Name: operatorSessionCookie, Value: token})
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "resets on restart") || !strings.Contains(body, "Billing is disabled.") || strings.Contains(body, `data-used-bytes="999"`) || strings.Contains(body, "Egress (30d)") {
		t.Fatalf("operator dashboard=%d %s", w.Code, body)
	}
	if version() != before || payment.calls.Load() != 0 {
		t.Fatal("operator usage mutated accounting or called payment")
	}
}

func Example_deliveryConfiguration() {
	fmt.Println("TPAHUB_DELIVERY_MODE=metrics-only TPAHUB_BILLING_PROVIDER=none tpahub serve")
	// Output: TPAHUB_DELIVERY_MODE=metrics-only TPAHUB_BILLING_PROVIDER=none tpahub serve
}
