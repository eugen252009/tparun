package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/store"
)

func TestEgressAdminCLIRecoversExactMarkerAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = db.CreateAccount(ctx, store.Account{ID: "admin-account", Email: "admin-r6@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateRepository(ctx, store.Repository{ID: "admin-repo", AccountID: "admin-account", Name: "admin", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.BeginEgressDelivery(ctx, "admin-marker", "admin-repo", "2026-09-04T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err = db.MarkEgressDeliveryUncertain(ctx, "admin-marker", "admin-repo", "settlement unavailable"); err != nil {
		t.Fatal(err)
	}
	if err = db.BeginEgressDelivery(ctx, "unrelated-marker", "admin-repo", "2026-09-04T10:01:00Z"); err != nil {
		t.Fatal(err)
	}
	if err = db.MarkEgressDeliveryUncertain(ctx, "unrelated-marker", "admin-repo", "other"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var inspected store.EgressDelivery
	var deliveries []store.EgressDelivery
	if err = captureJSONOutput(t, func() error { return adminCLI(config.Config{DataDir: dir}, []string{"egress", "list-uncertain"}) }, &deliveries); err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("listed deliveries=%+v", deliveries)
	}
	if err = captureJSONOutput(t, func() error {
		return adminCLI(config.Config{DataDir: dir}, []string{"egress", "inspect", "admin-marker"})
	}, &inspected); err != nil {
		t.Fatal(err)
	}
	if inspected.ID != "admin-marker" || inspected.AccountID != "admin-account" || inspected.RepositoryID != "admin-repo" || inspected.State != "uncertain" || inspected.Version != 2 || inspected.Reason != "settlement unavailable" {
		t.Fatalf("inspected marker=%+v", inspected)
	}
	if err = adminCLI(config.Config{DataDir: dir}, []string{"egress", "recover", "admin-marker", "--version", "2", "--usage-at", "2026-09-04T10:00:00Z", "--bytes", "42", "--data-dir", dir}); err != nil {
		t.Fatal(err)
	}
	if err = adminCLI(config.Config{DataDir: dir}, []string{"egress", "recover", "admin-marker", "--version", "2", "--usage-at", "2026-09-04T10:00:00Z", "--bytes", "42"}); err == nil {
		t.Fatal("recovery was not exactly once")
	}

	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var markers, audit int
	var usage int64
	remaining, err := db.EgressDeliveries(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range remaining {
		if delivery.ID == "admin-marker" {
			markers++
		}
		if delivery.ID == "unrelated-marker" {
			audit++
		}
	}
	usage, err = db.AccountEgressWindow(ctx, "admin-account", time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if markers != 0 || usage != 42 || audit != 1 {
		t.Fatalf("marker=%d usage=%d unrelated=%d", markers, usage, audit)
	}
	audits, err := db.AdminAuditEntries(ctx, "admin-marker")
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].Operator == "" || audits[0].Result != "recovered" {
		t.Fatalf("audit=%+v", audits)
	}
}

func TestEgressAdminCLIRejectsActiveDelivery(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = db.CreateAccount(ctx, store.Account{ID: "active-account", Email: "active-r6@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateRepository(ctx, store.Repository{ID: "active-repo", AccountID: "active-account", Name: "active", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.BeginEgressDelivery(ctx, "active-marker", "active-repo", "now"); err != nil {
		t.Fatal(err)
	}
	var listed []store.EgressDelivery
	if err = captureJSONOutput(t, func() error {
		return adminCLI(config.Config{DataDir: dir}, []string{"egress", "list-uncertain"})
	}, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("active marker listed as uncertain: %+v", listed)
	}
	db.Close()
	err = adminCLI(config.Config{DataDir: dir}, []string{"egress", "recover", "active-marker", "--version", "1", "--usage-at", "2026-09-04T00:00:00Z", "--bytes", "1"})
	if err == nil || err.Error() != "egress delivery is still active" {
		t.Fatalf("active recovery error=%v", err)
	}
}

func TestEgressAdminCLIRecoversLiveSettlementFailure(t *testing.T) {
	s, h := testServer(t)
	defer h.Close()
	repo := "33333333333333333333333333333333"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repo, AccountID: "a", Name: "live-recovery", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repo)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Packages.gz"), []byte("metadata"), 0644); err != nil {
		t.Fatal(err)
	}
	var deliveryID string
	s.completeEgressDelivery = func(_ context.Context, id, _ string, _ string, _ int64, _ string) error {
		deliveryID = id
		return errors.New("injected settlement failure")
	}
	response, err := http.Get(h.URL + "/r/" + repo + "/Packages.gz")
	if err != nil {
		t.Fatal(err)
	}
	if body, readErr := io.ReadAll(response.Body); readErr != nil || response.StatusCode != http.StatusOK || string(body) != "metadata" {
		response.Body.Close()
		t.Fatalf("failed settlement delivery status=%d err=%v", response.StatusCode, readErr)
	}
	response.Body.Close()
	if deliveryID == "" {
		t.Fatal("settlement marker was not captured")
	}
	var marker store.EgressDelivery
	if err = captureJSONOutput(t, func() error {
		return adminCLI(config.Config{DataDir: s.cfg.DataDir}, []string{"egress", "inspect", deliveryID})
	}, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.State != "uncertain" {
		t.Fatalf("live settlement marker=%+v", marker)
	}
	s.completeEgressDelivery = nil
	if err = adminCLI(config.Config{DataDir: s.cfg.DataDir}, []string{"egress", "recover", deliveryID, "--version", fmt.Sprint(marker.Version), "--usage-at", marker.CreatedAt, "--bytes", "7"}); err != nil {
		t.Fatal(err)
	}
	response, err = http.Get(h.URL + "/r/" + repo + "/Packages.gz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("delivery did not resume without restart: %d", response.StatusCode)
	}
	uncertain, err := s.db.RepositoryEgressUncertain(context.Background(), repo)
	if err != nil || uncertain {
		t.Fatalf("live uncertainty after recovery=%v err=%v", uncertain, err)
	}
}

func captureJSONOutput(t *testing.T, fn func() error, out any) error {
	t.Helper()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	os.Stdout = writer
	callErr := fn()
	writer.Close()
	os.Stdout = old
	body, readErr := io.ReadAll(reader)
	reader.Close()
	if callErr != nil {
		return callErr
	}
	if readErr != nil {
		return readErr
	}
	return json.Unmarshal(body, out)
}

func TestCleanupRetriesLiveWithBackoffAndRestoresInodePressure(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.CreateAccount(ctx, store.Account{ID: "cleanup-r6", Email: "cleanup-r6@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateRepository(ctx, store.Repository{ID: "cleanup-r6-repo", AccountID: "cleanup-r6", Name: "cleanup", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(dir, "incoming", "cleanup")
	if err = os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(incoming, "payload"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateJob(ctx, store.Job{ID: "cleanup-r6-job", RepositoryID: "cleanup-r6-repo", State: "running", IncomingPath: incoming, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(ctx, "cleanup-r6-job", "failed", "injected terminal failure"); err != nil {
		t.Fatal(err)
	}
	var now = time.Now().UTC().Add(time.Second)
	var removeAttempts int
	s := &server{cfg: config.Config{DataDir: dir, MinFreeInodes: 10}, db: db, cleanupNow: func() time.Time { return now }, freeInodes: func() (int64, error) {
		if _, statErr := os.Stat(incoming); statErr == nil {
			return 11, nil
		}
		return 12, nil
	}, removeAll: func(path string) error {
		if path == incoming {
			removeAttempts++
			if removeAttempts < 3 {
				return errors.New("injected remove failure")
			}
		}
		return os.RemoveAll(path)
	}}
	if err = s.retryPendingCleanup(now); err != nil {
		t.Fatal(err)
	}
	job, err := db.Job(ctx, "cleanup-r6-job")
	if err != nil {
		t.Fatal(err)
	}
	if job.CleanupState != "pending" || job.CleanupError == "" || job.CleanupState == "complete" {
		t.Fatalf("first cleanup state=%+v", job)
	}
	if err = s.checkInodeHeadroomFor(2); err == nil {
		t.Fatal("failed cleanup released inode pressure")
	}
	firstNext := job.CleanupNextRetry
	next, err := time.Parse(time.RFC3339Nano, firstNext)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.retryPendingCleanup(next.Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	job, _ = db.Job(ctx, "cleanup-r6-job")
	if job.CleanupAttempts != 1 || job.CleanupNextRetry != firstNext {
		t.Fatalf("retry ran before backoff: %+v", job)
	}
	if err = s.retryPendingCleanup(next); err != nil {
		t.Fatal(err)
	}
	job, _ = db.Job(ctx, "cleanup-r6-job")
	secondNext, err := time.Parse(time.RFC3339Nano, job.CleanupNextRetry)
	if err != nil || job.CleanupAttempts != 2 || !secondNext.After(next) {
		t.Fatalf("second cleanup state=%+v err=%v", job, err)
	}
	if err = s.retryPendingCleanup(secondNext); err != nil {
		t.Fatal(err)
	}
	job, _ = db.Job(ctx, "cleanup-r6-job")
	if job.CleanupState != "complete" || job.CleanupAttempts != 0 {
		t.Fatalf("successful cleanup state=%+v", job)
	}
	if _, err = os.Stat(incoming); !os.IsNotExist(err) {
		t.Fatalf("incoming survived cleanup: %v", err)
	}
	if err = s.checkInodeHeadroomFor(2); err != nil {
		t.Fatalf("successful cleanup did not restore inode headroom: %v", err)
	}
}

func TestCleanupPendingResumesAfterRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = db.CreateAccount(ctx, store.Account{ID: "restart-cleanup", Email: "restart-cleanup@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateRepository(ctx, store.Repository{ID: "restart-cleanup-repo", AccountID: "restart-cleanup", Name: "cleanup", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(dir, "incoming", "restart")
	if err = os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateJob(ctx, store.Job{ID: "restart-cleanup-job", RepositoryID: "restart-cleanup-repo", State: "running", IncomingPath: incoming, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(ctx, "restart-cleanup-job", "failed", "failure"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &server{cfg: config.Config{DataDir: dir}, db: db}
	if err = s.reconcileStagingState(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(incoming); !os.IsNotExist(err) {
		t.Fatalf("restart cleanup left incoming: %v", err)
	}
}

func TestCopyDebsSameContentDigestCollisionIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(src, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("same package payload")
	if err := os.WriteFile(filepath.Join(src, "tool.deb"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "tool.deb"), []byte("v1 package"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyDebs(src, dst, true); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	derived := filepath.Join(dst, "tool-"+hex.EncodeToString(digest[:])[:16]+".deb")
	if err := copyDebs(src, dst, true); err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if got, err := os.ReadFile(derived); err != nil || string(got) != string(payload) {
		t.Fatalf("derived package=%q err=%v", got, err)
	}
	if err := os.WriteFile(derived, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyDebs(src, dst, true); err == nil || !strings.Contains(err.Error(), "digest-derived package collision") {
		t.Fatalf("corrupt collision error=%v", err)
	}
}

func TestCopyDebsSameBasenameHistorySurvivesRetry(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "work")
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	for i, payload := range []string{"v1", "v2", "v3"} {
		source := filepath.Join(dir, fmt.Sprintf("source-%d", i))
		if err := os.MkdirAll(source, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "tool.deb"), []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		if err := copyDebs(source, destination, i != 0); err != nil {
			t.Fatalf("copy version %d: %v", i+1, err)
		}
	}
	if err := copyDebs(filepath.Join(dir, "source-1"), destination, true); err != nil {
		t.Fatalf("retry v2: %v", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("same-basename history entries=%v", entries)
	}
}

func TestInodeAdmissionBoundaryAndConcurrentUploads(t *testing.T) {
	s := &server{cfg: config.Config{MinFreeInodes: 10}, freeInodes: func() (int64, error) { return 11, nil }}
	if err := s.checkInodeHeadroomFor(1); err != nil {
		t.Fatalf("exact inode boundary rejected: %v", err)
	}
	s.freeInodes = func() (int64, error) { return 10, nil }
	if err := s.checkInodeHeadroomFor(1); err == nil {
		t.Fatal("inode boundary below reserve was accepted")
	}
	for _, tc := range []struct {
		name string
		free int64
		want int
	}{
		{name: "exact reserve", free: 12, want: http.StatusAccepted},
		{name: "below reserve", free: 11, want: http.StatusInsufficientStorage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, h := testServer(t)
			s.cfg.MinFreeInodes = 10
			s.freeInodes = func() (int64, error) { return tc.free, nil }
			repo := "11111111111111111111111111111111"
			if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repo, AccountID: "a", Name: tc.name, CreatedAt: "now"}); err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest(http.MethodPost, h.URL+"/v1/repositories/"+repo+"/publishes?filename=tool.deb", strings.NewReader("x"))
			req.Header.Set("Authorization", "Bearer tph_s_test")
			req.Header.Set("Content-Type", "application/octet-stream")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", res.StatusCode, tc.want)
			}
			if tc.want != http.StatusAccepted {
				if entries, readErr := os.ReadDir(filepath.Join(s.cfg.DataDir, "incoming")); readErr == nil && len(entries) != 0 {
					t.Fatalf("failed admission created incoming entries=%v", entries)
				}
			}
			h.Close()
		})
	}

	s, h := testServer(t)
	s.cfg.MinFreeInodes = 10
	s.freeInodes = func() (int64, error) {
		// Floor + incoming root + one raw upload directory and file.
		free := int64(13)
		root := filepath.Join(s.cfg.DataDir, "incoming")
		_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
			if err == nil && entry != nil {
				free--
			}
			return nil
		})
		return free, nil
	}
	repo := "22222222222222222222222222222222"
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repo, AccountID: "a", Name: "concurrent", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, h.URL+"/v1/repositories/"+repo+"/publishes?filename=tool.deb", strings.NewReader("x"))
			req.Header.Set("Authorization", "Bearer tph_s_test")
			req.Header.Set("Content-Type", "application/octet-stream")
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				if res.StatusCode == http.StatusAccepted {
					accepted.Add(1)
				}
				res.Body.Close()
			}
		}()
	}
	wg.Wait()
	h.Close()
	if accepted.Load() != 1 {
		t.Fatalf("concurrent inode admissions accepted=%d", accepted.Load())
	}
}

func TestProviderCheckoutProcessDeathRecoversExactPurchase(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	checkoutRequests := 0
	var checkoutKeys []string
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/v1/customers" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"cus_process_r6"}`)
			return
		}
		if r.URL.Path == "/v1/checkout/sessions" {
			checkoutRequests++
			checkoutKeys = append(checkoutKeys, r.Header.Get("Idempotency-Key"))
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"cs_process_r6","url":"https://checkout.test/process-r6"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer providerServer.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProviderCheckoutProcessDeathHelper$")
	cmd.Env = append(os.Environ(), "TPARUN_R6_PROVIDER_HELPER=1", "TPARUN_R6_DATA_DIR="+dir, "TPARUN_R6_PROVIDER_URL="+providerServer.URL)
	crashErr := cmd.Run()
	if exitErr, ok := crashErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 42 {
		t.Fatalf("provider crash helper error=%v", crashErr)
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, err := NewStripeBillingProvider(db, "sk_test_example", BillingEnvironmentTest)
	if err != nil {
		t.Fatal(err)
	}
	provider.BaseURL = providerServer.URL
	offer := tpaNanoOffer()
	_, err = provider.CreateCheckout(context.Background(), payments.Operation{OperationID: "process-purchase", IdempotencyKey: "tpahub:purchase:process-purchase:stripe:test:checkout:v2", CustomerID: commerce.CustomerID("process-account"), ProductID: commerce.ProductID(offer.ProductID), Description: offer.Description, CustomerDescription: "TPA.run account process-account", CustomerMetadata: map[string]string{"tpahub_account_id": "process-account"}, Currency: "EUR", AmountMinor: 500, TaxClass: payments.TaxClass(TaxSaaSPersonal), Metadata: map[string]string{"tpahub_purchase_id": "process-purchase"}, SuccessURL: "https://tpa.run/", CancelURL: "https://tpa.run/", Environment: payments.EnvironmentTest})
	if err != nil {
		t.Fatalf("provider idempotent retry=%v", err)
	}
	mu.Lock()
	if checkoutRequests != 2 || len(checkoutKeys) != 2 || checkoutKeys[0] == "" || checkoutKeys[0] != checkoutKeys[1] {
		mu.Unlock()
		t.Fatalf("checkout requests=%d keys=%v", checkoutRequests, checkoutKeys)
	}
	mu.Unlock()
	// The provider event deliberately occurs years after the locally persisted
	// checkout attempt. Recovery must preserve the original 2026..2027 terms.
	eventBody := fmt.Sprintf(`{"id":"evt_process_r6","created":1893456000,"type":"checkout.session.completed","data":{"object":{"id":"cs_process_r6","payment_status":"paid","amount_subtotal":500,"amount_total":595,"currency":"eur","metadata":{"tpahub_purchase_id":"process-purchase"}}}}`)
	hub := &server{cfg: config.Config{DataDir: dir, BillingPurchasesEnabled: true, BillingProvider: "stripe", BillingEnvironment: "test", BillingRateCard: "production-nano-v1", StripeTestSecret: "sk_test_example", StripeWebhookSecret: "whsec_example"}, db: db, purchaseProvider: provider, paymentWebhook: provider}
	webhook := func() *httptest.ResponseRecorder {
		now := time.Now().UTC()
		req := httptest.NewRequest(http.MethodPost, "/v1/billing/providers/stripe/webhook", strings.NewReader(eventBody))
		req.Header.Set("Stripe-Signature", signedStripeEvent(eventBody, "whsec_example", now))
		recorder := httptest.NewRecorder()
		hub.stripeWebhook(recorder, req)
		return recorder
	}
	if response := webhook(); response.Code != http.StatusOK {
		t.Fatalf("webhook recovery status=%d body=%s", response.Code, response.Body.String())
	}
	if response := webhook(); response.Code != http.StatusOK {
		t.Fatalf("replayed webhook status=%d body=%s", response.Code, response.Body.String())
	}
	purchase, err := db.Purchase(context.Background(), "process-purchase")
	wantFrom, wantUntil := "2026-09-04T12:00:00Z", "2027-09-04T12:00:00Z"
	if err != nil || purchase.Status != "paid" || purchase.ProviderReference != "cs_process_r6" || purchase.EntitlementPeriodStart != wantFrom || purchase.EntitlementPeriodEnd != wantUntil {
		t.Fatalf("recovered purchase=%+v err=%v", purchase, err)
	}
	if count, err := db.PurchaseGrantInstanceCount(context.Background(), "process-account"); err != nil || count == 0 {
		t.Fatalf("grant instances=%d err=%v", count, err)
	}
	grant, err := db.GetGrant(context.Background(), "process-account", tpahubproduct.ProductID, CapabilityRepositoryPublish, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || grant.ValidFrom.Format(time.RFC3339Nano) != wantFrom || grant.ValidUntil == nil || grant.ValidUntil.Format(time.RFC3339Nano) != wantUntil {
		t.Fatalf("recovered grant validity=%+v err=%v", grant, err)
	}
}

func TestProviderCheckoutProcessDeathHelper(t *testing.T) {
	if os.Getenv("TPARUN_R6_PROVIDER_HELPER") != "1" {
		return
	}
	dir := os.Getenv("TPARUN_R6_DATA_DIR")
	providerURL := os.Getenv("TPARUN_R6_PROVIDER_URL")
	db, err := store.Open(dir)
	if err != nil {
		os.Exit(41)
	}
	ctx := context.Background()
	if err = db.CreateAccount(ctx, store.Account{ID: "process-account", Email: "process-r6@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "now"}); err != nil {
		os.Exit(42)
	}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	offer := tpaNanoOffer()
	purchase := store.BillingPurchase{ID: "process-purchase", AccountID: "process-account", ProductID: string(offer.ProductID), PlanCode: offer.ID, OfferVersion: offer.Version, RateCardID: "production-nano-v1", RateCardMode: "production", TaxClassification: string(TaxSaaSPersonal), Currency: "EUR", AmountMinor: 500, CheckoutRequestVersion: "v2", BillingEnvironment: "test", Provider: "stripe", Status: "pending", EntitlementPeriodStart: now.Format(time.RFC3339Nano), EntitlementPeriodEnd: now.AddDate(1, 0, 0).Format(time.RFC3339Nano), CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)}
	if _, err = db.CreatePendingPurchase(ctx, purchase, purchase.CreatedAt, offer.Grants...); err != nil {
		os.Exit(43)
	}
	provider, err := NewStripeBillingProvider(db, "sk_test_example", BillingEnvironmentTest)
	if err != nil {
		os.Exit(44)
	}
	provider.BaseURL = providerURL
	_, err = provider.CreateCheckout(ctx, payments.Operation{OperationID: purchase.ID, IdempotencyKey: "tpahub:purchase:" + purchase.ID + ":stripe:test:checkout:v2", CustomerID: commerce.CustomerID(purchase.AccountID), ProductID: commerce.ProductID(purchase.ProductID), Description: offer.Description, CustomerDescription: "TPA.run account " + purchase.AccountID, CustomerMetadata: map[string]string{"tpahub_account_id": purchase.AccountID}, Currency: purchase.Currency, AmountMinor: purchase.AmountMinor, TaxClass: payments.TaxClass(purchase.TaxClassification), Metadata: map[string]string{"tpahub_purchase_id": purchase.ID}, SuccessURL: "https://tpa.run/", CancelURL: "https://tpa.run/", Environment: payments.EnvironmentTest})
	if err != nil {
		os.Exit(45)
	}
	os.Exit(42)
}
