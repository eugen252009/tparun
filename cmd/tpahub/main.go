package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpa"
	appversion "github.com/eugen252009/tpahub/internal/version"
	"golang.org/x/sys/unix"
)

var idRE = regexp.MustCompile(`^[a-f0-9]{32}$`)

func newID() string { b := make([]byte, 16); _, _ = rand.Read(b); return fmt.Sprintf("%x", b) }

type publicationRunner interface {
	Check(context.Context) error
	Publish(context.Context, tpa.PublishRequest) error
}

type server struct {
	cfg              config.Config
	db               *store.Store
	runner           publicationRunner
	ctx              context.Context
	startedAt        time.Time
	now              func() time.Time
	purchaseProvider PurchaseProvider
	paymentWebhook   payments.WebhookParser
	grants           commerce.GrantService
	purchaseMu       sync.Mutex
	stagingMu        sync.Mutex
	egressLookup     func(string, time.Time) (int64, int64, error)
	egressMu         sync.RWMutex
	egressDegraded   map[string]bool
	publicationFault func(string) error
	diskFree         func() (int64, error)
	reservations     map[string]capacityReservation
}

type capacityReservation struct {
	AccountID, RepositoryID, JobID, Kind string
	Bytes                                int64
}

func main() {
	cfg := config.Load()
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		if err := cli(cfg, os.Args[1:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}
func run(cfg config.Config) error {
	if e := ValidateBillingConfiguration(cfg); e != nil {
		return e
	}
	db, e := store.Open(cfg.DataDir)
	if e != nil {
		return e
	}
	defer db.Close()
	s := &server{cfg: cfg, db: db, runner: tpa.Runner{Bin: cfg.TPABin, GPGHome: cfg.GPGHome, Fingerprint: cfg.GPGFingerprint}, startedAt: time.Now().UTC()}
	if cfg.BillingPurchasesEnabled {
		paymentProvider, providerErr := NewStripeBillingProvider(db, cfg.StripeTestSecret, BillingEnvironment(cfg.BillingEnvironment))
		if providerErr != nil {
			return providerErr
		}
		s.purchaseProvider, s.paymentWebhook = paymentProvider, paymentProvider
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e = s.runner.Check(ctx); e != nil {
		return e
	}
	if e = s.reconcilePublicationState(); e != nil {
		return e
	}
	if e = s.reconcileStagingState(); e != nil {
		return e
	}
	s.ctx = ctx
	go s.worker()
	srv := &http.Server{Addr: cfg.Listen, Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Shutdown(context.Background()) }()
	log.Printf("TPA Hub listening on http://%s", cfg.Listen)
	e = srv.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func (s *server) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/install.sh", s.install)
	m.HandleFunc("/static/", s.static)
	m.HandleFunc("/register", s.registerPage)
	m.HandleFunc("/login", s.loginPage)
	m.HandleFunc("/account", s.accountPage)
	m.HandleFunc("/account/billing/nano/purchase", s.accountNanoPurchase)
	m.HandleFunc("/logout", s.browserLogout)
	m.HandleFunc("/docs", s.web)
	m.HandleFunc("/how-to", s.web)
	m.HandleFunc("/pricing", s.web)
	m.HandleFunc("/datenschutz", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://lupricht.net/privacy/", http.StatusPermanentRedirect)
	})
	m.HandleFunc("/", s.web)
	m.HandleFunc("/tpahub.asc", s.publicKey)
	m.HandleFunc("/version", s.version)
	m.HandleFunc("/v1/accounts", s.accounts)
	m.HandleFunc("/v1/login", s.login)
	m.HandleFunc("/v1/logout", s.logout)
	m.HandleFunc("/v1/billing/purchases/nano", s.authHandler(s.createNanoPurchase))
	m.HandleFunc("/v1/billing/providers/stripe/webhook", s.stripeWebhook)
	m.HandleFunc("/v1/repositories", s.authHandler(s.repositories))
	m.HandleFunc("/v1/repositories/", s.repositoryRoute)
	m.HandleFunc("/v1/publish-jobs/", s.authHandler(s.job))
	m.HandleFunc("/r/", s.files)
	return s.browserSession(m)
}
func (s *server) install(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		bad(w, 405, "method not allowed")
		return
	}
	data, e := os.ReadFile("install.sh")
	if e != nil {
		data, e = os.ReadFile("../../install.sh")
	}
	if e != nil {
		bad(w, 404, "installer not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}
func (s *server) publicKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		bad(w, 405, "method not allowed")
		return
	}
	data, e := os.ReadFile("tpahub.asc")
	if e != nil {
		data, e = os.ReadFile("../../tpahub.asc")
	}
	if e != nil {
		bad(w, 404, "public key not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}
func (s *server) version(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		bad(w, 405, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"service": appversion.Service, "version": appversion.Current, "revision": "unknown", "started_at": s.startedAt.Format(time.RFC3339)})
}
func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func bad(w http.ResponseWriter, status int, msg string) { http.Error(w, msg, status) }
func (s *server) repositories(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		repos, e := s.db.Repositories(r.Context(), accountID(r))
		if e != nil {
			bad(w, 500, "repository lookup failed")
			return
		}
		jsonOut(w, repos)
		return
	}
	if r.Method != "POST" || r.URL.Path != "/v1/repositories" {
		bad(w, 405, "method not allowed")
		return
	}
	var in struct {
		Name       string `json:"name"`
		Visibility string `json:"visibility"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil || strings.TrimSpace(in.Name) == "" {
		bad(w, 400, "name is required")
		return
	}
	if _, err := s.grantService().GetGrant(r.Context(), commerce.CustomerID(accountID(r)), commerce.ProductTPAHub, CapabilityRepositoryCreate, s.currentTime()); err != nil {
		bad(w, http.StatusForbidden, "repository creation grant unavailable")
		return
	}
	id := newID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if e := os.MkdirAll(filepath.Join(s.cfg.DataDir, "repos", id), 0755); e != nil {
		bad(w, 500, e.Error())
		return
	}
	visibility := in.Visibility
	if visibility != "public" {
		visibility = "private"
	}
	if e := s.db.CreateRepository(r.Context(), store.Repository{ID: id, AccountID: accountID(r), Name: strings.TrimSpace(in.Name), Visibility: visibility, CreatedAt: now}); e != nil {
		bad(w, 500, e.Error())
		return
	}
	log.Printf("repository created id=%s", id)
	jsonOut(w, map[string]any{"id": id, "name": strings.TrimSpace(in.Name), "visibility": visibility, "created_at": now})
}
func (s *server) repositoryRoute(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/signing-key") {
		s.repository(w, r)
		return
	}
	if r.Method == "GET" && r.Header.Get("Authorization") == "" {
		s.repository(w, r)
		return
	}
	s.authHandler(s.repository)(w, r)
}
func (s *server) repository(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/v1/repositories/")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 1 || !idRE.MatchString(parts[0]) {
		bad(w, 404, "repository not found")
		return
	}
	id := parts[0]
	if len(parts) == 1 && r.Method == "GET" {
		repo, e := s.db.Repository(r.Context(), id)
		if e == nil && repo.Visibility == "private" && accountID(r) != "" {
			repo, e = s.db.OwnedRepository(r.Context(), accountID(r), id)
		}
		if e != nil || (repo.Visibility == "private" && accountID(r) == "") {
			bad(w, 404, "repository not found")
			return
		}
		jsonOut(w, repo)
		return
	}
	if len(parts) == 2 && parts[1] == "signing-key" {
		s.key(w, r, id)
		return
	}
	if len(parts) >= 2 && parts[1] == "reader-credentials" {
		s.credentials(w, r, id, parts[2:])
		return
	}
	if len(parts) == 2 && parts[1] == "publishes" && r.Method == "POST" {
		repo, e := s.db.OwnedRepository(r.Context(), accountID(r), id)
		if e != nil || repo.ID != id {
			bad(w, 404, "repository not found")
			return
		}
		s.upload(w, r, id)
		return
	}
	bad(w, 404, "not found")
}
func (s *server) upload(w http.ResponseWriter, r *http.Request, id string) {
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	if err := s.checkDiskHeadroom(); err != nil {
		bad(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	if _, err := s.grantService().GetGrant(r.Context(), commerce.CustomerID(accountID(r)), commerce.ProductTPAHub, CapabilityRepositoryPublish, s.currentTime()); err != nil {
		bad(w, http.StatusForbidden, "repository publishing grant unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") && r.ContentLength > 0 {
		if err := s.checkDiskHeadroomFor(r.ContentLength); err != nil {
			bad(w, http.StatusInsufficientStorage, err.Error())
			return
		}
		if err := s.checkStagingCapacity(r.Context(), accountID(r), r.ContentLength, false); err != nil {
			bad(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
	}
	jobID := newID()
	reserveBytes := r.ContentLength
	if reserveBytes <= 0 {
		// Multipart requests with a known Content-Length use that bound too; an
		// unknown-length body is conservatively bounded by MaxUpload.
		reserveBytes = s.cfg.MaxUpload
	}
	if err := s.reserveCapacity(r.Context(), accountID(r), id, jobID, "incoming", reserveBytes); err != nil {
		bad(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			s.releaseCapacity(jobID)
		}
	}()
	dir := filepath.Join(s.cfg.DataDir, "incoming", jobID)
	if e := os.MkdirAll(dir, 0700); e != nil {
		bad(w, 500, e.Error())
		return
	}
	defer func() {
		if r.Context().Err() != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	var total int64
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/") {
		if e := r.ParseMultipartForm(s.cfg.MaxUpload); e != nil {
			_ = os.RemoveAll(dir)
			bad(w, 400, "invalid upload")
			return
		}
		for _, hs := range r.MultipartForm.File {
			for _, h := range hs {
				if !strings.HasSuffix(strings.ToLower(h.Filename), ".deb") {
					_ = os.RemoveAll(dir)
					bad(w, 400, "only .deb files are accepted")
					return
				}
				name := filepath.Base(h.Filename)
				f, e := h.Open()
				if e != nil {
					bad(w, 400, "cannot read upload")
					return
				}
				dst, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if e == nil {
					n, ce := io.Copy(dst, io.LimitReader(f, s.cfg.MaxUpload-total+1))
					dst.Close()
					f.Close()
					total += n
					e = ce
					if total > s.cfg.MaxUpload {
						e = fmt.Errorf("upload too large")
					}
				} else {
					f.Close()
				}
				if e != nil {
					_ = os.RemoveAll(dir)
					bad(w, 413, e.Error())
					return
				}
			}
		}
	} else {
		if !strings.HasSuffix(strings.ToLower(r.URL.Query().Get("filename")), ".deb") {
			_ = os.RemoveAll(dir)
			bad(w, 400, "filename query ending in .deb is required")
			return
		}
		dst, e := os.OpenFile(filepath.Join(dir, filepath.Base(r.URL.Query().Get("filename"))), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e == nil {
			total, e = io.Copy(dst, io.LimitReader(r.Body, s.cfg.MaxUpload+1))
			dst.Close()
		}
		if e != nil || total > s.cfg.MaxUpload {
			_ = os.RemoveAll(dir)
			bad(w, 413, "upload too large")
			return
		}
	}
	if total == 0 {
		_ = os.RemoveAll(dir)
		bad(w, 400, "at least one .deb is required")
		return
	}
	if err := s.checkDiskHeadroom(); err != nil {
		_ = os.RemoveAll(dir)
		bad(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	// The reservation transitions to actual filesystem bytes once the upload is complete.
	if err := s.resizeCapacity(jobID, 0); err != nil {
		_ = os.RemoveAll(dir)
		bad(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if e := s.db.CreateJob(r.Context(), store.Job{ID: jobID, RepositoryID: id, State: "queued", IncomingPath: dir, CreatedAt: now}); e != nil {
		_ = os.RemoveAll(dir)
		bad(w, 500, e.Error())
		return
	}
	committed = true
	log.Printf("publish job queued id=%s repository=%s bytes=%d", jobID, id, total)
	w.WriteHeader(http.StatusAccepted)
	jsonOut(w, map[string]any{"id": jobID, "state": "queued"})
}
func (s *server) job(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		bad(w, 405, "method not allowed")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/publish-jobs/"), "/")
	j, e := s.db.Job(r.Context(), id)
	if e == nil {
		repo, _ := s.db.Repository(r.Context(), j.RepositoryID)
		if repo.AccountID != accountID(r) {
			e = store.ErrNotFound
		}
	}
	if e != nil {
		bad(w, 404, "job not found")
		return
	}
	jsonOut(w, j)
}
func (s *server) files(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/r/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || !idRE.MatchString(parts[0]) {
		bad(w, 404, "not found")
		return
	}
	rel := filepath.Clean("/" + parts[1])
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || rel == ".." {
		bad(w, 404, "not found")
		return
	}
	repo, e := s.db.Repository(r.Context(), parts[0])
	if e != nil {
		bad(w, 404, "not found")
		return
	}
	if repo.Visibility == "private" {
		if _, grantErr := s.grantService().GetGrant(r.Context(), commerce.CustomerID(repo.AccountID), commerce.ProductTPAHub, CapabilityRepositoryReadPrivate, s.currentTime()); grantErr != nil {
			bad(w, http.StatusForbidden, "private repository grant unavailable")
			return
		}
		_, pw, ok := r.BasicAuth()
		allowed, err := s.db.ReaderRepository(r.Context(), repo.ID, store.HashToken(pw))
		if !ok || err != nil || !allowed {
			w.Header().Set("WWW-Authenticate", `Basic realm="TPA Hub"`)
			bad(w, 401, "repository credentials required")
			return
		}
	}
	root := s.activeRepositoryRoot(filepath.Join(s.cfg.DataDir, "repos", parts[0]))
	path := filepath.Join(root, rel)
	info, e := os.Stat(path)
	if e != nil || !info.Mode().IsRegular() {
		bad(w, 404, "not found")
		return
	}
	startedAt := s.currentTime().UTC()
	period := startedAt.Format("2006-01-02")
	limited := false
	if used, limit, err := s.accountEgress(repo.AccountID, startedAt); err != nil {
		limited = grantDenied(err)
		log.Printf("repository egress enforcement lookup: repository=%s: %v", repo.ID, err)
		if !limited {
			bad(w, http.StatusServiceUnavailable, "egress authority unavailable")
			return
		}
	} else {
		limited = egressLimited(used, limit)
	}
	counted := &repositoryCountingWriter{ResponseWriter: w}
	var output http.ResponseWriter = counted
	if limited && strings.HasSuffix(strings.ToLower(path), ".deb") {
		w.Header().Set("X-TPAHub-Egress-Limited", "true")
		output = &rateLimitedResponseWriter{ResponseWriter: counted, rate: NanoOverageBytesPerSecond}
	}
	defer func() { s.recordRepositoryEgress(repo.ID, period, counted.bytes) }()
	http.ServeFile(output, r, path)
}
func (s *server) key(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != "GET" {
		bad(w, 405, "method not allowed")
		return
	}
	if _, e := s.db.Repository(r.Context(), id); e != nil {
		bad(w, 404, "repository not found")
		return
	}
	if s.cfg.GPGFingerprint == "" {
		bad(w, 404, "repository signing key unavailable")
		return
	}
	args := []string{"--export", s.cfg.GPGFingerprint}
	cmd := exec.CommandContext(r.Context(), "gpg", args...)
	if s.cfg.GPGHome != "" {
		cmd.Env = append(os.Environ(), "GNUPGHOME="+s.cfg.GPGHome)
	}
	out, e := cmd.Output()
	if e != nil {
		bad(w, 500, "signing key unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/pgp-keys")
	w.Write(out)
}
func (s *server) worker() {
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		j, e := s.db.Claim(s.ctx)
		if e != nil {
			if errors.Is(e, store.ErrNotFound) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			log.Printf("worker claim: %v", e)
			time.Sleep(time.Second)
			continue
		}
		s.process(j)
	}
}
func (s *server) process(j store.Job) {
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	defer s.releaseCapacity(j.ID)
	log.Printf("publish job started id=%s", j.ID)
	if e := s.ensurePublishAccountActive(j.RepositoryID); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.checkDiskHeadroom(); e != nil {
		s.fail(j, e)
		return
	}
	work := filepath.Join(s.cfg.DataDir, "work", j.ID)
	candidate := work + "-published"
	repo, e := s.db.Repository(context.Background(), j.RepositoryID)
	if e != nil {
		s.fail(j, e)
		return
	}
	incomingBytes, e := fileTreeSize(j.IncomingPath)
	if e != nil {
		s.fail(j, fmt.Errorf("incoming capacity accounting: %w", e))
		return
	}
	defer os.RemoveAll(work)
	defer os.RemoveAll(candidate)
	art := filepath.Join(s.cfg.DataDir, "artifacts", j.RepositoryID)
	baseline := s.activeRepositoryRoot(filepath.Join(s.cfg.DataDir, "repos", j.RepositoryID))
	if _, statErr := os.Stat(baseline); statErr != nil {
		baseline = art
	}
	// Reserve work, candidate, and immutable-generation expansion before copying.
	if e = s.reserveCapacity(context.Background(), repo.AccountID, j.RepositoryID, j.ID, "work", incomingBytes); e != nil {
		s.fail(j, e)
		return
	}
	if e := os.MkdirAll(work, 0700); e != nil {
		s.fail(j, e)
		return
	}
	if e := copyDebs(baseline, work, false); e != nil {
		s.fail(j, e)
		return
	}
	if e := copyDebs(j.IncomingPath, work, true); e != nil {
		s.fail(j, e)
		return
	}
	// Work reservation is converted to actual bytes before candidate creation;
	// subsequent output is bounded by the publication capacity checks.
	if e := s.resizeCapacity(j.ID, 0); e != nil {
		s.fail(j, e)
		return
	}
	repositoryRoot := filepath.Join(s.cfg.DataDir, "repos", j.RepositoryID)
	generations := filepath.Join(repositoryRoot, "generations")
	if e := os.MkdirAll(generations, 0755); e != nil {
		s.fail(j, e)
		return
	}
	out := filepath.Join(generations, j.ID)
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.PublishTimeout)
	defer cancel()
	workBytes, e := fileTreeSize(work)
	if e != nil {
		s.fail(j, fmt.Errorf("work capacity accounting: %w", e))
		return
	}
	// TPA output is variable-sized; reserve a conservative two-work-tree bound
	// before the runner can create candidate files.
	candidateBound := workBytes*2 + 1<<20
	if e = s.reserveCapacity(context.Background(), "", j.RepositoryID, j.ID, "candidate", candidateBound); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.runner.Publish(ctx, tpa.PublishRequest{Input: work, Output: candidate}); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.publicationCheckpoint("candidate-built"); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.checkDiskHeadroom(); e != nil {
		s.fail(j, e)
		return
	}
	repository, repositoryErr := s.db.Repository(context.Background(), j.RepositoryID)
	if repositoryErr != nil {
		s.fail(j, repositoryErr)
		return
	}
	if e := s.checkStagingCapacity(context.Background(), repository.AccountID, 0, true); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.checkStorageCapacity(j.RepositoryID, candidate); e != nil {
		s.fail(j, e)
		return
	}
	if e := verifyPublicationCandidate(candidate); e != nil {
		s.fail(j, e)
		return
	}
	candidateBytes, e := fileTreeSize(candidate)
	if e != nil || candidateBytes > candidateBound {
		if e == nil {
			e = fmt.Errorf("candidate exceeded reserved capacity")
		}
		s.fail(j, e)
		return
	}
	if e := s.publicationCheckpoint("candidate-verified"); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.ensurePublishAccountActive(j.RepositoryID); e != nil {
		s.fail(j, e)
		return
	}
	// The verified candidate is the artifact that becomes the immutable
	// generation. Never rebuild a second, independently unverified tree.
	if e := os.Rename(candidate, out); e != nil {
		s.fail(j, e)
		return
	}
	// Same-filesystem rename changes ownership from candidate to generation;
	// it does not allocate another copy. Clear the candidate reservation rather
	// than counting the same physical bytes twice.
	if e := s.resizeCapacity(j.ID, 0); e != nil {
		s.fail(j, e)
		return
	}
	pointer := filepath.Join(repositoryRoot, "current")
	temporaryPointer := pointer + "." + j.ID
	// The temporary pointer is a fixed-size directory entry (no file data is
	// duplicated); it is classified as negligible metadata and is removed on
	// every failure path.
	if e := os.Symlink(filepath.Join("generations", j.ID), temporaryPointer); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.publicationCheckpoint("before-live-swap"); e != nil {
		_ = os.Remove(temporaryPointer)
		s.fail(j, e)
		return
	}
	// This is intentionally the last external/DB work before activation.
	if e := s.ensurePublishAuthorized(j.RepositoryID); e != nil {
		_ = os.Remove(temporaryPointer)
		s.fail(j, e)
		return
	}
	if e := atomicReplaceSymlink(temporaryPointer, pointer); e != nil {
		_ = os.Remove(temporaryPointer)
		s.fail(j, e)
		return
	}
	if e := s.publicationCheckpoint("after-live-swap"); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.db.SetActiveGeneration(context.Background(), j.RepositoryID, j.ID, s.currentTime().UTC().Format(time.RFC3339Nano)); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.publicationCheckpoint("after-durable-record"); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.observeAccountStorage(context.Background(), j.RepositoryID, s.currentTime()); e != nil {
		log.Printf("storage high-water update: repository=%s: %v", j.RepositoryID, e)
	}
	if e := s.publicationCheckpoint("before-job-success"); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.finishPublishedJob(j); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.collectOldGenerations(j.RepositoryID); e != nil {
		log.Printf("generation cleanup: repository=%s: %v", j.RepositoryID, e)
	}
	_ = os.RemoveAll(j.IncomingPath)
	log.Printf("publish job succeeded id=%s", j.ID)
}
func (s *server) collectOldGenerations(repositoryID string) error {
	if s.cfg.MaxGenerations <= 0 {
		return nil
	}
	busy, err := s.db.HasNonterminalJobs(context.Background(), repositoryID)
	if err != nil || busy {
		return err
	}
	active, err := s.db.ActiveGeneration(context.Background(), repositoryID)
	if err != nil {
		return nil
	}
	root := filepath.Join(s.cfg.DataDir, "repos", repositoryID, "generations")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	type generationInfo struct {
		name string
		mod  time.Time
	}
	var candidates []generationInfo
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == active {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		candidates = append(candidates, generationInfo{entry.Name(), info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod.After(candidates[j].mod) })
	keep := int(s.cfg.MaxGenerations) - 1
	if keep < 0 {
		keep = 0
	}
	for i := keep; i < len(candidates); i++ {
		if err = os.RemoveAll(filepath.Join(root, candidates[i].name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) publicationCheckpoint(stage string) error {
	if s.publicationFault == nil {
		return nil
	}
	return s.publicationFault(stage)
}

func atomicReplaceSymlink(next, current string) error {
	if _, err := os.Lstat(current); os.IsNotExist(err) {
		return os.Rename(next, current)
	} else if err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, next, unix.AT_FDCWD, current, unix.RENAME_EXCHANGE); err != nil {
		return err
	}
	return os.Remove(next)
}

func verifyPublicationCandidate(candidate string) error {
	info, err := os.Stat(candidate)
	if err != nil {
		return fmt.Errorf("publication candidate missing: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("publication candidate is not a directory")
	}
	entries, err := os.ReadDir(candidate)
	if err != nil {
		return fmt.Errorf("publication candidate unreadable: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("publication candidate is empty")
	}
	return nil
}

func (s *server) reconcileStagingState() error {
	paths, err := s.db.TerminalJobIncomingPaths(context.Background())
	if err != nil {
		return fmt.Errorf("scan terminal staging state: %w", err)
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove terminal incoming workspace: %w", err)
		}
	}
	work := filepath.Join(s.cfg.DataDir, "work")
	entries, err := os.ReadDir(work)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scan staging state: %w", err)
	}
	for _, entry := range entries {
		if err = os.RemoveAll(filepath.Join(work, entry.Name())); err != nil {
			return fmt.Errorf("remove abandoned staging workspace: %w", err)
		}
	}
	return nil
}

func (s *server) reconcilePublicationState() error {
	root := filepath.Join(s.cfg.DataDir, "repos")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scan repository publication state: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		repositoryRoot := filepath.Join(root, entry.Name())
		pointer := filepath.Join(repositoryRoot, "current")
		generation, generationErr := s.db.ActiveGeneration(context.Background(), entry.Name())
		if generationErr != nil {
			if errors.Is(generationErr, store.ErrNotFound) {
				if err = os.Remove(pointer); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove uncommitted publication pointer: %w", err)
				}
				continue
			}
			return fmt.Errorf("read durable publication state: %w", generationErr)
		}
		target, err := filepath.EvalSymlinks(pointer)
		if err != nil && !os.IsNotExist(err) {
			// A dangling pointer is reconstructed from durable state below.
			target = ""
		}
		if target != "" && filepath.Base(target) == generation {
			continue
		}
		generationPath := filepath.Join(repositoryRoot, "generations", generation)
		if _, err = os.Stat(generationPath); err != nil {
			return fmt.Errorf("durable publication generation missing: %w", err)
		}
		temporary := pointer + ".reconcile"
		_ = os.Remove(pointer)
		if err = os.Symlink(filepath.Join("generations", generation), temporary); err != nil {
			return fmt.Errorf("stage publication pointer recovery: %w", err)
		}
		if err = atomicReplaceSymlink(temporary, pointer); err != nil {
			_ = os.Remove(temporary)
			return fmt.Errorf("restore durable publication pointer: %w", err)
		}
	}
	return nil
}

func (s *server) activeRepositoryRoot(root string) string {
	if _, err := os.Stat(filepath.Join(root, "current")); err == nil {
		return filepath.Join(root, "current")
	}
	return root
}

func (s *server) finishPublishedJob(j store.Job) error {
	generation, err := s.db.ActiveGeneration(context.Background(), j.RepositoryID)
	if err != nil {
		return fmt.Errorf("active publication generation lookup: %w", err)
	}
	if generation != j.ID {
		return fmt.Errorf("publication generation mismatch: job=%s active=%s", j.ID, generation)
	}
	if err = s.db.Finish(context.Background(), j.ID, "succeeded", ""); err != nil {
		return fmt.Errorf("finish publication job: %w", err)
	}
	return nil
}

func (s *server) ensurePublishAuthorized(repositoryID string) error {
	if err := s.ensurePublishAccountActive(repositoryID); err != nil {
		return err
	}
	repository, err := s.db.Repository(context.Background(), repositoryID)
	if err != nil {
		return fmt.Errorf("publication repository lookup: %w", err)
	}
	if _, err = s.grantService().GetGrant(context.Background(), commerce.CustomerID(repository.AccountID), commerce.ProductTPAHub, CapabilityRepositoryPublish, s.currentTime()); err != nil {
		return fmt.Errorf("publication grant unavailable: %w", err)
	}
	return nil
}

func (s *server) ensurePublishAccountActive(repositoryID string) error {
	repository, err := s.db.Repository(context.Background(), repositoryID)
	if err != nil {
		return fmt.Errorf("publication repository lookup: %w", err)
	}
	account, err := s.db.AccountByID(context.Background(), repository.AccountID)
	if err != nil {
		return fmt.Errorf("publication account lookup: %w", err)
	}
	if account.Status != store.AccountStatusActive {
		return fmt.Errorf("publication account is not active")
	}
	return nil
}

// reserveCapacity is called while stagingMu is held. Reservations are process-local;
// restart reconciliation deliberately treats durable filesystem state as actual bytes.
func (s *server) reserveCapacity(ctx context.Context, accountID, repositoryID, jobID, kind string, bytes int64) error {
	if bytes < 0 {
		return fmt.Errorf("negative capacity reservation")
	}
	if s.reservations == nil {
		s.reservations = make(map[string]capacityReservation)
	}
	oldBytes := int64(0)
	if old, ok := s.reservations[jobID]; ok {
		oldBytes = old.Bytes
	}
	if accountID != "" {
		if err := s.checkStagingCapacity(ctx, accountID, bytes, false); err != nil {
			return err
		}
	}
	var reserved int64
	for _, r := range s.reservations {
		reserved += r.Bytes
	}
	var actual int64
	var err error
	for _, root := range []string{filepath.Join(s.cfg.DataDir, "incoming"), filepath.Join(s.cfg.DataDir, "work")} {
		tree, sizeErr := fileTreeSize(root)
		if sizeErr != nil {
			return fmt.Errorf("capacity accounting: %w", sizeErr)
		}
		actual += tree
	}
	if s.cfg.MaxStagingBytes > 0 && actual+reserved+bytes > s.cfg.MaxStagingBytes {
		return fmt.Errorf("global staging reservation exceeded")
	}
	if err = s.checkDiskHeadroomFor(bytes); err != nil {
		return err
	}
	s.reservations[jobID] = capacityReservation{AccountID: accountID, RepositoryID: repositoryID, JobID: jobID, Kind: kind, Bytes: oldBytes + bytes}
	return nil
}
func (s *server) resizeCapacity(jobID string, bytes int64) error {
	if s.reservations == nil {
		return fmt.Errorf("missing capacity reservation")
	}
	r, ok := s.reservations[jobID]
	if !ok {
		return fmt.Errorf("missing capacity reservation")
	}
	if bytes < 0 {
		return fmt.Errorf("negative capacity reservation")
	}
	if bytes > r.Bytes {
		if err := s.checkDiskHeadroomFor(bytes - r.Bytes); err != nil {
			return err
		}
	}
	if bytes == 0 {
		delete(s.reservations, jobID)
		return nil
	}
	r.Bytes = bytes
	s.reservations[jobID] = r
	return nil
}
func (s *server) releaseCapacity(jobID string) {
	if s.reservations != nil {
		delete(s.reservations, jobID)
	}
}

func (s *server) checkDiskHeadroom() error { return s.checkDiskHeadroomFor(0) }

func (s *server) checkDiskHeadroomFor(additional int64) error {
	if additional < 0 {
		return fmt.Errorf("invalid disk reservation")
	}
	if s.cfg.MinDiskFreeBytes <= 0 {
		return nil
	}
	var free int64
	if s.diskFree != nil {
		var err error
		free, err = s.diskFree()
		if err != nil {
			return fmt.Errorf("disk headroom unavailable: %w", err)
		}
	} else {
		var stat unix.Statfs_t
		if err := unix.Statfs(s.cfg.DataDir, &stat); err != nil {
			return fmt.Errorf("disk headroom unavailable: %w", err)
		}
		free = int64(stat.Bavail) * int64(stat.Bsize)
	}
	if free-additional < s.cfg.MinDiskFreeBytes {
		return fmt.Errorf("minimum disk headroom unavailable")
	}
	return nil
}

func (s *server) checkStagingCapacity(ctx context.Context, accountID string, incoming int64, alreadyWritten bool) error {
	grant, err := s.grantService().GetGrant(ctx, commerce.CustomerID(accountID), commerce.ProductTPAHub, CapabilityRepositoryPublish, s.currentTime())
	if err != nil {
		return fmt.Errorf("staging grant unavailable: %w", err)
	}
	if grant.Limits.StorageBytes <= 0 {
		return fmt.Errorf("staging grant has no capacity")
	}
	usage, err := s.accountUsage(ctx, accountID)
	if err != nil {
		return fmt.Errorf("staging usage unavailable: %w", err)
	}
	var staged, reservedGlobal, reservedAccount int64
	for _, reservation := range s.reservations {
		reservedGlobal += reservation.Bytes
		if reservation.AccountID == accountID {
			reservedAccount += reservation.Bytes
		}
	}
	for _, root := range []string{filepath.Join(s.cfg.DataDir, "incoming"), filepath.Join(s.cfg.DataDir, "work")} {
		treeSize, sizeErr := fileTreeSize(root)
		if sizeErr != nil {
			return fmt.Errorf("global staging usage unavailable: %w", sizeErr)
		}
		staged += treeSize
	}
	if !alreadyWritten && s.cfg.MaxStagingBytes > 0 && staged+reservedGlobal+incoming > s.cfg.MaxStagingBytes {
		return fmt.Errorf("global staging storage limit exceeded")
	}
	accountStaged := int64(0)
	paths, pathErr := s.db.AccountStagingPaths(ctx, accountID)
	if pathErr != nil {
		return fmt.Errorf("account staging usage unavailable: %w", pathErr)
	}
	for _, path := range paths {
		treeSize, sizeErr := fileTreeSize(path)
		if sizeErr != nil {
			return fmt.Errorf("account staging usage unavailable: %w", sizeErr)
		}
		accountStaged += treeSize
		jobID := filepath.Base(path)
		for _, workPath := range []string{filepath.Join(s.cfg.DataDir, "work", jobID)} {
			treeSize, sizeErr = fileTreeSize(workPath)
			if sizeErr != nil {
				return fmt.Errorf("account staging usage unavailable: %w", sizeErr)
			}
			accountStaged += treeSize
		}
	}
	if usage.StoredBytes+accountStaged+reservedAccount+incoming > grant.Limits.StorageBytes {
		return fmt.Errorf("staging storage limit exceeded")
	}
	return nil
}

func (s *server) checkStorageCapacity(repositoryID, candidate string) error {
	repository, err := s.db.Repository(context.Background(), repositoryID)
	if err != nil {
		return fmt.Errorf("repository lookup for storage capacity: %w", err)
	}
	account, err := s.accountUsage(context.Background(), repository.AccountID)
	if err != nil {
		return fmt.Errorf("storage usage unavailable: %w", err)
	}
	currentRepo, err := s.repositoryUsage(repositoryID)
	if err != nil {
		return fmt.Errorf("repository storage usage unavailable: %w", err)
	}
	prospective, err := publishedUsage(candidate)
	if err != nil {
		return fmt.Errorf("candidate storage usage unavailable: %w", err)
	}
	grant, err := s.grantService().GetGrant(context.Background(), commerce.CustomerID(repository.AccountID), commerce.ProductTPAHub, CapabilityRepositoryPublish, s.currentTime())
	if err != nil {
		return fmt.Errorf("storage grant unavailable: %w", err)
	}
	capacity := grant.Limits.StorageBytes
	if capacity <= 0 {
		return fmt.Errorf("storage grant has no capacity")
	}
	resulting := account.StoredBytes - currentRepo.StoredBytes + prospective.StoredBytes
	if resulting > capacity && resulting > account.StoredBytes {
		return StorageCapacityError{CurrentBytes: account.StoredBytes, ResultingBytes: resulting, CapacityBytes: capacity}
	}
	return nil
}
func fileTreeSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) && path == root {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func copyDebs(src, dst string, replace bool) error {
	entries, e := os.ReadDir(src)
	if e != nil {
		if os.IsNotExist(e) {
			return nil
		}
		return e
	}
	for _, x := range entries {
		if x.IsDir() || !strings.HasSuffix(strings.ToLower(x.Name()), ".deb") {
			continue
		}
		in := filepath.Join(src, x.Name())
		out := filepath.Join(dst, x.Name())
		a, e := os.Open(in)
		if e != nil {
			return e
		}
		if replace {
			_ = os.Remove(out)
		}
		b, e := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e == nil {
			_, e = io.Copy(b, a)
			b.Close()
		}
		a.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *server) fail(j store.Job, e error) {
	msg := e.Error()
	_ = os.RemoveAll(j.IncomingPath)
	_ = s.db.Finish(context.Background(), j.ID, "failed", msg)
	log.Printf("publish job failed id=%s error=%s", j.ID, msg)
}
