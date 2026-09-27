package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eugen252009/tpahub/internal/billing"
	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/payments"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpa"
	"github.com/eugen252009/tpahub/internal/usage"
	appversion "github.com/eugen252009/tpahub/internal/version"
	"golang.org/x/sys/unix"
)

var (
	idRE              = regexp.MustCompile(`^[a-f0-9]{32}$`)
	aptArchitectureRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

const (
	maxUploadFiles     = 64
	maxDiagnosticBytes = 64 << 10
)

type boundedDiagnostic struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *boundedDiagnostic) Len() int { return b.buf.Len() }

func (b *boundedDiagnostic) Write(p []byte) (int, error) {
	original := len(p)
	remaining := maxDiagnosticBytes - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return original, nil
	}
	_, err := b.buf.Write(p)
	return original, err
}

func (b *boundedDiagnostic) String() string {
	message := b.buf.String()
	if b.truncated {
		return message + "\n[diagnostic output truncated]"
	}
	return message
}

func newID() string { b := make([]byte, 16); _, _ = rand.Read(b); return fmt.Sprintf("%x", b) }

type publicationRunner interface {
	Check(context.Context) error
	Publish(context.Context, tpa.PublishRequest) error
}
type boundedPublicationRunner interface {
	PublishBounded(context.Context, tpa.PublishRequest, int64) error
}
type boundedPublicationRunnerWithInodes interface {
	PublishBoundedWithInodes(context.Context, tpa.PublishRequest, int64, int64) error
}

type server struct {
	commercial              billing.Service
	commercialResolved      bool
	deliveryOnce            sync.Once
	delivery                *deliveryBehavior
	deliveryErr             error
	usageConsumers          []*usage.Mailbox // configured before routes; empty is valid
	cfg                     config.Config
	db                      *store.Store
	runner                  publicationRunner
	ctx                     context.Context
	startedAt               time.Time
	now                     func() time.Time
	purchaseProvider        PurchaseProvider
	paymentWebhook          payments.WebhookParser
	grants                  commerce.GrantService
	customerAuthRate        *authLimiter
	sshChallengeMu          sync.Mutex
	sshChallenges           map[string]sshLoginChallenge
	purchaseMu              sync.Mutex
	stagingMu               sync.Mutex
	egressLookup            func(string, time.Time) (int64, int64, error)
	egressMu                sync.RWMutex
	egressDegraded          map[string]bool
	publicationFault        func(string) error
	diskFree                func() (int64, error)
	freeInodes              func() (int64, error)
	lifecycle               *serverLifecycle
	capacityObserve         func(stage, path string)
	publicationHashObserve  func(bytes int64)
	reservations            map[string]capacityReservation
	verifyCandidate         func(candidate, work string) error
	operatorAuth            *operatorAuthState
	operatorInitMu          sync.Mutex
	pointerSwap             func(next, current string) error
	durablePublicationFault func() error
	cleanupWake             chan struct{}
	cleanupNow              func() time.Time
	removeAll               func(string) error
	beginEgressDelivery     func(context.Context, string, string, string) error
	reserveEgressDelivery   func(context.Context, string, string, string, string, int64, int64, string) (bool, error)
	completeEgressDelivery  func(context.Context, string, string, string, int64, string) error
}

type capacityReservation struct {
	AccountID, RepositoryID, JobID, Kind string
	Bytes, Inodes                        int64
}

func serverRequested(args []string) bool {
	return len(args) > 0 && args[0] == "serve"
}

func main() {
	cfg := config.Load()
	args := os.Args[1:]
	if !serverRequested(args) {
		if err := cli(cfg, args); err != nil {
			log.Fatal(err)
		}
		return
	}
	var err error
	cfg, err = config.LoadServer()
	if err != nil {
		log.Fatal(err)
	}
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}
func run(cfg config.Config) error {
	if _, err := initialGrantSource(cfg); err != nil {
		return err
	}
	if _, err := config.ResolveEgressEnforcement(cfg.DeliveryMode, cfg.EgressEnforcement); err != nil {
		return err
	}
	if e := ValidateBillingConfiguration(cfg); e != nil {
		return e
	}
	lock, e := lockDataDirectory(cfg.DataDir)
	if e != nil {
		return e
	}
	safeToClose := true
	defer func() {
		if !safeToClose {
			return
		} // forced process exit retains ownership until OS exit
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	}()
	db, e := store.Open(cfg.DataDir)
	if e != nil {
		return e
	}
	defer func() {
		if safeToClose {
			db.Close()
		}
	}()
	if e = os.Chmod(filepath.Join(cfg.DataDir, "tpahub.db"), 0600); e != nil {
		return fmt.Errorf("secure database permissions: %w", e)
	}
	s := &server{cfg: cfg, db: db, runner: tpa.Runner{Bin: cfg.TPABin, GPGHome: cfg.GPGHome, Fingerprint: cfg.GPGFingerprint}, startedAt: time.Now().UTC(), customerAuthRate: newAuthLimiter(4096, 10, time.Minute), cleanupWake: make(chan struct{}, 1)}
	if _, e = s.operatorState(); e != nil {
		return fmt.Errorf("invalid operator WebAuthn configuration: %w", e)
	}
	if err := s.composeCommercial(); err != nil {
		return err
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
	srv := newHTTPServer(s)
	listener, e := net.Listen("tcp", cfg.Listen)
	if e != nil {
		return e
	}
	log.Printf("TPA.run listening on http://%s", listener.Addr())
	safeToClose, e = s.serveAndDrain(ctx, srv, listener, cfg.ShutdownTimeout, 5*time.Second)
	return e
}

func newHTTPServer(s *server) *http.Server {
	s.lifecycle = &serverLifecycle{}
	return &http.Server{Addr: s.cfg.Listen, Handler: s.lifecycle.wrap(s.routes()), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: s.cfg.UploadReadTimeout}
}

func lockDataDirectory(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".tpahub.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open data directory lock: %w", err)
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("another TPA.run process already owns data directory %s: %w", dir, err)
	}
	return lock, nil
}

func (s *server) routes() http.Handler {
	if err := s.initDelivery(); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bad(w, http.StatusServiceUnavailable, "delivery configuration unavailable")
		})
	}
	m := http.NewServeMux()
	m.HandleFunc("/tparun.sources", s.sources)
	m.HandleFunc("/tparun-bootstrap.deb", s.bootstrapAlias)
	m.HandleFunc("/static/", s.static)
	m.HandleFunc("/register", s.registerPage)
	m.HandleFunc("/login", s.loginPage)
	m.HandleFunc("/account", s.accountPage)
	m.HandleFunc("/ops", s.ops)
	m.HandleFunc("/ops/bootstrap", s.bootstrapRoute)
	if tparunEnv("TPARUN_QUALIFICATION_HOOK") == "1" {
		m.HandleFunc("/__qualification/candidate-ready", s.qualificationCandidateReady)
		m.HandleFunc("/__qualification/candidate-mutate", s.qualificationCandidateMutation)
	}
	m.HandleFunc("/ops/login", s.operatorLoginPage)
	m.HandleFunc("/ops/auth/login/begin", s.operatorLoginBegin)
	m.HandleFunc("/ops/auth/login/finish", s.operatorLoginFinish)
	m.HandleFunc("/ops/auth/register/begin", s.operatorRegisterBegin)
	m.HandleFunc("/ops/auth/register/finish", s.operatorRegisterFinish)
	m.HandleFunc("/ops/logout", s.operatorLogout)
	m.HandleFunc("/ops/recover", s.operatorRecoveryPage)
	m.HandleFunc("/ops/recover/register", s.operatorRegisterPage)
	m.HandleFunc("/ops/passkeys/add", s.operatorRegisterPage)
	m.HandleFunc("/account/billing/nano/purchase", s.accountNanoPurchase)
	m.HandleFunc("/account/ssh-credentials", s.accountSSHCredentialsPage)
	m.HandleFunc("/account/ssh-credentials/", s.accountSSHCredentialPage)
	m.HandleFunc("/logout", s.browserLogout)
	m.HandleFunc("/docs", s.web)
	m.HandleFunc("/how-to", s.web)
	m.HandleFunc("/pricing", s.web)
	m.HandleFunc("/datenschutz", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://lupricht.net/privacy/", http.StatusPermanentRedirect)
	})
	m.HandleFunc("/", s.web)
	m.HandleFunc("/version", s.version)
	m.HandleFunc("/ready", s.ready)
	m.HandleFunc("/v1/accounts", s.accounts)
	m.HandleFunc("/v1/login", s.login)
	m.HandleFunc("/v1/login/ssh/challenge", s.sshChallenge)
	m.HandleFunc("/v1/login/ssh", s.sshLogin)
	m.HandleFunc("/v1/logout", s.logout)
	m.HandleFunc("/v1/account", s.authHandler(s.accountSummary))
	m.HandleFunc("/v1/account/ssh-credentials", s.authHandler(s.sshCredentials))
	m.HandleFunc("/v1/account/ssh-credentials/", s.authHandler(s.sshCredentialRoute))
	m.HandleFunc("/v1/billing/purchases/nano", s.authHandler(s.createNanoPurchase))
	s.registerHostedRoutes(m)
	m.HandleFunc("/v1/repositories", s.authHandler(s.repositories))
	m.HandleFunc("/v1/public/repositories", s.publicRepositories)
	m.HandleFunc("/v1/repositories/", s.repositoryRoute)
	m.HandleFunc("/v1/publish-jobs/", s.authHandler(s.job))
	m.HandleFunc("/v1/publication-sessions/", s.authHandler(s.publicationSessionRoute))
	m.HandleFunc("/r/", s.files)
	return s.browserSession(m)
}
func (s *server) sources(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		bad(w, 405, "method not allowed")
		return
	}
	data, err := websiteFiles.ReadFile("tparun.sources")
	if err != nil {
		bad(w, 500, "sources file unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}
func (s *server) version(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		bad(w, 405, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"service": appversion.Service, "version": appversion.Current, "revision": appversion.BuildRevision, "schema": strconv.Itoa(store.CurrentSchemaVersion()), "started_at": s.startedAt.Format(time.RFC3339)})
}
func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
func bad(w http.ResponseWriter, status int, msg string) { http.Error(w, msg, status) }
func (s *server) publicRepositories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	repositories, err := s.db.PublicRepositories(r.Context())
	if err != nil {
		bad(w, http.StatusInternalServerError, "public repository lookup failed")
		return
	}
	// Do not expose account ownership or administrative fields through the
	// anonymous discovery surface.
	out := make([]repositoryView, 0, len(repositories))
	for _, repository := range repositories {
		view, err := s.repositoryView(r.Context(), repository)
		if err != nil {
			bad(w, http.StatusInternalServerError, "repository owner lookup failed")
			return
		}
		out = append(out, view)
	}
	jsonOut(w, out)
}

func (s *server) repositories(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		if !allowsCredential(r, store.CredentialList) {
			bad(w, http.StatusForbidden, "SSH credential lacks list capability")
			return
		}
		repos, e := s.db.Repositories(r.Context(), accountID(r))
		if e != nil {
			bad(w, 500, "repository lookup failed")
			return
		}
		out := make([]repositoryView, 0, len(repos))
		for _, repository := range repos {
			view, err := s.repositoryView(r.Context(), repository)
			if err != nil {
				bad(w, http.StatusInternalServerError, "repository owner lookup failed")
				return
			}
			out = append(out, view)
		}
		jsonOut(w, out)
		return
	}
	if !allowsCredential(r, store.CredentialWrite) {
		bad(w, http.StatusForbidden, "SSH credential lacks write capability")
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
	in.Name = strings.TrimSpace(in.Name)
	if err := validateRepositoryName(in.Name); err != nil {
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.repositoryPolicy().AuthorizeCreation(r.Context(), accountID(r)); err != nil {
		bad(w, http.StatusForbidden, err.Error())
		return
	}
	id := newID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	allocationBlock, e := filesystemAllocationBlock(s.cfg.DataDir)
	if e != nil {
		bad(w, http.StatusInsufficientStorage, "repository allocation unavailable")
		return
	}
	reservationID := "repository-create:" + id
	repositoryReservation, overflow := addCapacity(allocationBlock, allocationBlock)
	if overflow {
		bad(w, http.StatusInsufficientStorage, "repository allocation overflow")
		return
	}
	if e = s.reserveCapacityWithResources(r.Context(), accountID(r), id, reservationID, "repository", repositoryReservation, 2); e != nil {
		bad(w, http.StatusInsufficientStorage, e.Error())
		return
	}
	defer s.releaseCapacity(reservationID)
	repositoryRoot := filepath.Join(s.cfg.DataDir, "repos", id)
	if e = mkdirAllDurable(repositoryRoot, 0755); e != nil {
		bad(w, 500, e.Error())
		return
	}
	visibility := in.Visibility
	if visibility != "public" {
		visibility = "private"
	}
	if e = s.db.CreateRepository(r.Context(), store.Repository{ID: id, AccountID: accountID(r), Name: strings.TrimSpace(in.Name), Visibility: visibility, CreatedAt: now}); e != nil {
		if cleanupErr := s.removePath(repositoryRoot); cleanupErr != nil {
			s.signalCleanupRetry()
			bad(w, 500, fmt.Sprintf("%v; repository root cleanup failed: %v", e, cleanupErr))
			return
		}
		if cleanupSyncErr := syncDirectory(filepath.Dir(repositoryRoot)); cleanupSyncErr != nil {
			s.signalCleanupRetry()
			bad(w, 500, fmt.Sprintf("%v; repository root cleanup sync failed: %v", e, cleanupSyncErr))
			return
		}
		bad(w, 500, e.Error())
		return
	}
	log.Printf("repository created id=%s", id)
	view, viewErr := s.repositoryView(r.Context(), store.Repository{ID: id, AccountID: accountID(r), Name: in.Name, Visibility: visibility, CreatedAt: now})
	if viewErr != nil {
		bad(w, http.StatusInternalServerError, "repository owner lookup failed")
		return
	}
	jsonOut(w, view)
}
func (s *server) repositoryRoute(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/signing-key") {
		if r.Header.Get("Authorization") != "" {
			s.authHandler(s.repository)(w, r)
		} else {
			s.repository(w, r)
		}
		return
	}
	if r.Method == "GET" && r.Header.Get("Authorization") == "" {
		s.repository(w, r)
		return
	}
	s.authHandler(s.repository)(w, r)
}
func (s *server) repository(w http.ResponseWriter, r *http.Request) {
	if accountID(r) != "" {
		if r.Method == http.MethodGet && !allowsCredential(r, store.CredentialRead) {
			bad(w, http.StatusForbidden, "SSH credential lacks read capability")
			return
		}
		if r.Method != http.MethodGet && !allowsCredential(r, store.CredentialWrite) {
			bad(w, http.StatusForbidden, "SSH credential lacks write capability")
			return
		}
	}
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
		view, viewErr := s.repositoryView(r.Context(), repo)
		if viewErr != nil {
			bad(w, http.StatusInternalServerError, "repository owner lookup failed")
			return
		}
		jsonOut(w, view)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPatch {
		var in struct {
			Visibility string `json:"visibility"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in) != nil {
			bad(w, http.StatusBadRequest, "invalid visibility request")
			return
		}
		if in.Visibility != "private" && in.Visibility != "public" {
			bad(w, http.StatusBadRequest, "visibility must be private or public")
			return
		}
		if _, err := s.repositoryPolicy().AuthorizeMutation(r.Context(), accountID(r), id); err != nil {
			s.repositoryAuthorizationError(w, err)
			return
		}
		if err := s.db.SetRepositoryVisibility(r.Context(), id, in.Visibility); err != nil {
			bad(w, http.StatusInternalServerError, "repository visibility update failed")
			return
		}
		repo, err := s.db.Repository(r.Context(), id)
		if err != nil {
			bad(w, http.StatusInternalServerError, "repository lookup failed")
			return
		}
		view, viewErr := s.repositoryView(r.Context(), repo)
		if viewErr != nil {
			bad(w, http.StatusInternalServerError, "repository owner lookup failed")
			return
		}
		jsonOut(w, view)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if _, err := s.repositoryPolicy().AuthorizeMutation(r.Context(), accountID(r), id); err != nil {
			s.repositoryAuthorizationError(w, err)
			return
		}
		s.stagingMu.Lock()
		defer s.stagingMu.Unlock()
		if err := s.deleteRepository(r.Context(), id); err != nil {
			s.repositoryDeletionError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 2 && parts[1] == "signing-key" {
		s.key(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "publication-plan" && r.Method == http.MethodPost {
		s.handlePublicationPlan(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "publication-sessions" {
		s.createPublicationSession(w, r, id)
		return
	}
	if len(parts) >= 2 && parts[1] == "reader-credentials" {
		s.credentials(w, r, id, parts[2:])
		return
	}
	if len(parts) == 2 && parts[1] == "publishes" && r.Method == "POST" {
		// Keep the domain decision precise, but cloak private repositories from
		// foreign customers at the HTTP boundary. The direct lookup here is
		// not authorization; it only selects the existing visibility contract.
		repo, lookupErr := s.db.Repository(r.Context(), id)
		if lookupErr != nil {
			bad(w, http.StatusNotFound, "repository not found")
			return
		}
		if _, authErr := s.repositoryPolicy().AuthorizeMutation(r.Context(), accountID(r), id); authErr != nil {
			if repo.Visibility == "private" {
				bad(w, http.StatusNotFound, "repository not found")
			} else {
				bad(w, http.StatusForbidden, authErr.Error())
			}
			return
		}
		s.upload(w, r, id)
		return
	}
	bad(w, 404, "not found")
}
func (s *server) repositoryAuthorizationError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) || strings.Contains(err.Error(), "repository not found") {
		bad(w, http.StatusNotFound, "repository not found")
		return
	}
	if errors.Is(err, ErrRepositoryOwnerMismatch) {
		bad(w, http.StatusForbidden, "repository management is owner-only")
		return
	}
	bad(w, http.StatusForbidden, "repository management is not authorized")
}

func (s *server) repositoryDeletionError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrRepositoryBusy) {
		bad(w, http.StatusConflict, "repository has an active publication lifecycle")
		return
	}
	if strings.Contains(err.Error(), "not found") {
		bad(w, http.StatusNotFound, "repository not found")
		return
	}
	if strings.Contains(err.Error(), "storage cleanup") || strings.Contains(err.Error(), "storage sync") {
		bad(w, http.StatusInternalServerError, err.Error())
		return
	}
	bad(w, http.StatusConflict, err.Error())
}

func (s *server) deleteRepository(ctx context.Context, id string) error {
	if !idRE.MatchString(id) {
		return store.ErrNotFound
	}
	if promotion, found, promotionErr := s.readBootstrapPromotion(); promotionErr != nil {
		return promotionErr
	} else if found && promotion.RepositoryID == id {
		return fmt.Errorf("repository is the promoted bootstrap target; promote another artifact or clear the promotion first")
	}
	dataRoot := filepath.Clean(s.cfg.DataDir)
	reposRoot := filepath.Join(dataRoot, "repos")
	repoRoot := filepath.Join(reposRoot, id)
	if filepath.Dir(repoRoot) != reposRoot || filepath.Base(repoRoot) != id {
		return fmt.Errorf("repository storage path is unsafe")
	}
	if err := s.db.DeleteRepository(ctx, id); err != nil {
		return err
	}
	for _, path := range []string{repoRoot, filepath.Join(dataRoot, "artifacts", id)} {
		if err := s.removePath(path); err != nil {
			return fmt.Errorf("repository storage cleanup failed")
		}
	}
	if err := syncDirectory(reposRoot); err != nil {
		return fmt.Errorf("repository storage sync failed")
	}
	return nil
}

func (s *server) upload(w http.ResponseWriter, r *http.Request, id string) {
	s.stagingMu.Lock()
	locked := true
	committed := false
	jobID := ""
	dir := ""
	defer func() {
		if !locked {
			s.stagingMu.Lock()
		}
		if !committed {
			if jobID != "" {
				s.releaseCapacity(jobID)
			}
			if dir != "" {
				_ = os.RemoveAll(dir)
			}
		}
		s.stagingMu.Unlock()
	}()
	if err := s.checkDiskHeadroom(); err != nil {
		bad(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	if _, _, err := s.repositoryPolicy().AuthorizePublication(r.Context(), accountID(r), id); err != nil {
		bad(w, http.StatusForbidden, err.Error())
		return
	}
	sessionID := r.URL.Query().Get("session")
	var sessionBatch int64
	if sessionID != "" || r.URL.Query().Get("batch") != "" {
		var parseErr error
		sessionBatch, parseErr = strconv.ParseInt(r.URL.Query().Get("batch"), 10, 64)
		if sessionID == "" || parseErr != nil || sessionBatch < 0 {
			bad(w, http.StatusBadRequest, "invalid publication session batch")
			return
		}
		session, sessionErr := s.db.PublicationSession(r.Context(), sessionID)
		if sessionErr != nil || session.State != "open" || session.RepositoryID != id || session.OwnerAccountID != accountID(r) {
			bad(w, http.StatusNotFound, "publication session not found")
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") && r.ContentLength > 0 {
		if err := s.checkDiskHeadroomFor(r.ContentLength); err != nil {
			bad(w, http.StatusInsufficientStorage, err.Error())
			return
		}
	}
	for _, name := range []string{"incoming", "work"} {
		if err := os.MkdirAll(filepath.Join(s.cfg.DataDir, name), 0700); err != nil {
			bad(w, http.StatusInsufficientStorage, "staging directory unavailable")
			return
		}
	}
	jobID = newID()
	reserveBytes := r.ContentLength
	if reserveBytes <= 0 {
		// Multipart requests with a known Content-Length use that bound too; an
		// unknown-length body is conservatively bounded by MaxUpload.
		reserveBytes = s.cfg.MaxUpload
	}
	allocationBlock, err := filesystemAllocationBlock(s.cfg.DataDir)
	if err != nil || reserveBytes > math.MaxInt64-(allocationBlock-1) {
		bad(w, http.StatusInsufficientStorage, "upload allocation unavailable")
		return
	}
	physicalReserve := ((reserveBytes + allocationBlock - 1) / allocationBlock) * allocationBlock
	physicalReserve, overflow := addCapacity(physicalReserve, allocationBlock)
	if overflow {
		bad(w, http.StatusInsufficientStorage, "upload allocation overflow")
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		physicalReserve, overflow = addCapacity(physicalReserve, maxUploadFiles*allocationBlock)
		if overflow {
			bad(w, http.StatusInsufficientStorage, "upload allocation overflow")
			return
		}
	}
	inodeReserve := int64(2)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		inodeReserve = maxUploadFiles + 1
	}
	if err := s.reserveCapacityWithResources(r.Context(), accountID(r), id, jobID, "incoming", physicalReserve, inodeReserve); err != nil {
		bad(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	dir = filepath.Join(s.cfg.DataDir, "incoming", jobID)
	if e := os.MkdirAll(dir, 0700); e != nil {
		bad(w, 500, e.Error())
		return
	}
	// The reservation protects capacity while an untrusted client streams its
	// body, so other tenants need not wait on the global allocator lock.
	s.stagingMu.Unlock()
	locked = false
	var total int64
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/") {
		// Stream directly to the reserved incoming tree. ParseMultipartForm
		// would create an additional, unowned disk spool above its RAM limit.
		reader, e := r.MultipartReader()
		if e != nil {
			bad(w, 400, "invalid upload")
			return
		}
		fileCount := 0
		for {
			part, e := reader.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				bad(w, 400, "invalid upload")
				return
			}
			if part.FileName() == "" {
				part.Close()
				continue
			}
			fileCount++
			if fileCount > maxUploadFiles {
				bad(w, 400, "too many upload files")
				return
			}
			name := filepath.Base(part.FileName())
			if !strings.HasSuffix(strings.ToLower(name), ".deb") {
				bad(w, 400, "only .deb files are accepted")
				return
			}
			n, e := s.copyUploadFile(filepath.Join(dir, name), part, s.cfg.MaxUpload-total+1)
			total += n
			if e != nil || total > s.cfg.MaxUpload {
				bad(w, 413, "upload copy failed or too large")
				return
			}
			part.Close()
		}
	} else {
		if !strings.HasSuffix(strings.ToLower(r.URL.Query().Get("filename")), ".deb") {
			bad(w, 400, "filename query ending in .deb is required")
			return
		}
		var e error
		total, e = s.copyUploadFile(filepath.Join(dir, filepath.Base(r.URL.Query().Get("filename"))), r.Body, s.cfg.MaxUpload+1)
		if e != nil || total > s.cfg.MaxUpload {
			bad(w, 413, "upload copy failed or too large")
			return
		}
	}
	s.stagingMu.Lock()
	locked = true
	if total == 0 {
		_ = os.RemoveAll(dir)
		bad(w, 400, "at least one .deb is required")
		return
	}
	// Uploads consume infrastructure capacity only. Customer entitlement is
	// checked against the complete resulting published payload by the worker.
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
	now := s.currentTime().UTC().Format(time.RFC3339Nano)
	artifacts, identityErr := uploadedPublicationArtifactsObserved(r.Context(), dir, s.publicationHashObserve)
	if sessionID != "" {
		if identityErr != nil || len(artifacts) == 0 {
			bad(w, http.StatusBadRequest, "publication session batch contains an invalid package")
			return
		}
		replayed, batchErr := s.acceptPublicationSessionBatch(r.Context(), sessionID, accountID(r), id, sessionBatch, dir, artifacts, now)
		if batchErr != nil {
			status := http.StatusConflict
			if strings.Contains(batchErr.Error(), "does not match") {
				status = http.StatusBadRequest
			}
			bad(w, status, batchErr.Error())
			return
		}
		committed = true
		log.Printf("publication batch accepted session=%s batch=%d files=%d bytes=%d replay=%t", sessionID, sessionBatch, len(artifacts), total, replayed)
		w.WriteHeader(http.StatusAccepted)
		jsonOut(w, map[string]any{"session_id": sessionID, "batch": sessionBatch, "state": "accepted", "replayed": replayed})
		return
	}
	if identityErr == nil && len(artifacts) > 0 {
		if e := s.db.CreateJobWithArtifacts(r.Context(), store.Job{ID: jobID, RepositoryID: id, PublisherAccountID: accountID(r), State: "queued", IncomingPath: dir, CreatedAt: now}, artifacts); e != nil {
			_ = os.RemoveAll(dir)
			bad(w, 500, e.Error())
			return
		}
	} else if e := s.db.CreateJob(r.Context(), store.Job{ID: jobID, RepositoryID: id, PublisherAccountID: accountID(r), State: "queued", IncomingPath: dir, CreatedAt: now}); e != nil {
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
	artifacts, artifactErr := s.db.PublicationArtifacts(r.Context(), j.ID)
	if artifactErr != nil {
		bad(w, http.StatusInternalServerError, "publication status unavailable")
		return
	}
	jsonOut(w, struct {
		store.Job
		Artifacts []store.PublicationArtifact `json:"artifacts"`
	}{Job: j, Artifacts: artifacts})
}
func (s *server) files(w http.ResponseWriter, r *http.Request) {
	if token, ok := bearer(r); ok {
		principal, err := s.db.SessionPrincipalByToken(r.Context(), store.HashToken(token))
		if err != nil {
			bad(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if principal.CredentialID != "" && !principal.CredentialFlags.Has(store.CredentialRead) {
			bad(w, http.StatusForbidden, "SSH credential lacks read capability")
			return
		}
	}
	rest := strings.TrimPrefix(r.URL.Path, "/r/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		bad(w, 404, "not found")
		return
	}
	repositoryID := parts[0]
	relParts := parts[1:]
	var repo store.Repository
	var e error
	if idRE.MatchString(repositoryID) {
		repo, e = s.db.Repository(r.Context(), repositoryID)
	} else if len(parts) >= 3 {
		repo, e = s.resolveRepositoryPath(r.Context(), parts[0], parts[1])
		relParts = parts[2:]
	} else {
		e = store.ErrNotFound
	}
	if e != nil {
		bad(w, 404, "not found")
		return
	}
	rel := filepath.Clean("/" + strings.Join(relParts, "/"))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || rel == ".." {
		bad(w, 404, "not found")
		return
	}
	if repo.ID == "" {
		bad(w, 404, "not found")
		return
	}
	if repo.Visibility == "private" {
		if _, grantErr := s.grantService().GetGrant(r.Context(), commerce.CustomerID(repo.AccountID), tpahubproduct.ProductID, CapabilityRepositoryReadPrivate, s.currentTime()); grantErr != nil {
			bad(w, http.StatusForbidden, "private repository grant unavailable")
			return
		}
		_, pw, ok := r.BasicAuth()
		allowed, err := s.db.ReaderRepository(r.Context(), repo.ID, store.HashToken(pw))
		if !ok || err != nil || !allowed {
			w.Header().Set("WWW-Authenticate", `Basic realm="TPA.run"`)
			bad(w, 401, "repository credentials required")
			return
		}
	}
	root, rootErr := s.publishedRepositoryRoot(r.Context(), repo.ID)
	if rootErr != nil {
		if errors.Is(rootErr, store.ErrNotFound) {
			bad(w, http.StatusNotFound, "not found")
		} else {
			bad(w, http.StatusServiceUnavailable, "publication authority unavailable")
		}
		return
	}
	// Pin the authorized root and reject symlink traversal outside it. The old
	// os.Stat/ServeFile pair followed escaping symlinks in repository content.
	files, e := os.OpenRoot(root)
	if e != nil {
		bad(w, 404, "not found")
		return
	}
	defer files.Close()
	info, e := files.Stat(rel)
	if e != nil || !info.Mode().IsRegular() {
		bad(w, 404, "not found")
		return
	}
	s.serveRepositoryFile(w, r, repo, files.FS(), rel, info.Size())
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
		if s.lifecycle != nil && s.lifecycle.draining.Load() {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		j, e := s.db.Claim(s.ctx)
		if e != nil {
			delay := 100 * time.Millisecond
			if !errors.Is(e, store.ErrNotFound) {
				if s.ctx.Err() != nil {
					return
				}
				log.Printf("worker claim: %v", e)
				delay = time.Second
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(delay):
			}
			continue
		}
		s.process(j)
	}
}
func (s *server) process(j store.Job) {
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	defer func() {
		if cleanupErr := s.cleanupPublishJob(j); cleanupErr != nil {
			if stateErr := s.db.SetCleanupRetry(context.Background(), j.ID, cleanupErr, s.cleanupRetryAt(s.cleanupCurrentTime(), 1)); stateErr != nil {
				log.Printf("publish cleanup state: job=%s cleanup=%v state=%v", j.ID, cleanupErr, stateErr)
			}
			s.signalCleanupRetry()
			return
		}
		if stateErr := s.db.SetCleanupResult(context.Background(), j.ID, nil); stateErr != nil {
			log.Printf("publish cleanup state: job=%s: %v", j.ID, stateErr)
			return
		}
		s.releaseCapacity(j.ID)
	}()
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
	defer func() {
		_ = os.RemoveAll(work)
		_ = os.RemoveAll(candidate)
	}()
	art := filepath.Join(s.cfg.DataDir, "artifacts", j.RepositoryID)
	baseline := s.activeRepositoryRoot(filepath.Join(s.cfg.DataDir, "repos", j.RepositoryID))
	if _, statErr := os.Stat(baseline); statErr != nil {
		baseline = art
	}
	baselineBytes, e := copiedDebAllocation(baseline, s.cfg.DataDir)
	if e != nil {
		s.fail(j, fmt.Errorf("baseline capacity accounting: %w", e))
		return
	}
	incomingAllocation, e := copiedDebAllocation(j.IncomingPath, s.cfg.DataDir)
	if e != nil {
		s.fail(j, fmt.Errorf("incoming allocation accounting: %w", e))
		return
	}
	baselineInodes, e := copiedDebInodes(baseline)
	if e != nil {
		s.fail(j, fmt.Errorf("baseline inode accounting: %w", e))
		return
	}
	incomingInodes, e := copiedDebInodes(j.IncomingPath)
	if e != nil {
		s.fail(j, fmt.Errorf("incoming inode accounting: %w", e))
		return
	}
	allocationBlock, e := filesystemAllocationBlock(s.cfg.DataDir)
	if e != nil {
		s.fail(j, fmt.Errorf("work metadata accounting: %w", e))
		return
	}
	// The work tree physically duplicates the complete active baseline and the
	// incoming payload. Reserve that full allocation before creating it.
	workBound, overflow := addCapacity(baselineBytes, incomingAllocation, allocationBlock)
	if overflow {
		s.fail(j, fmt.Errorf("work capacity overflow"))
		return
	}
	workInodes, overflow := addCapacity(baselineInodes, incomingInodes, 1)
	if overflow {
		s.fail(j, fmt.Errorf("work inode accounting overflow"))
		return
	}
	if e = s.reserveCapacityWithResources(context.Background(), repo.AccountID, j.RepositoryID, j.ID, "work", workBound, workInodes); e != nil {
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
	if e := s.removeReplacedWorkPackages(s.ctx, work, j.ID); e != nil {
		s.fail(j, fmt.Errorf("prepare package replacements: %w", e))
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
	if e := mkdirAllDurable(generations, 0755); e != nil {
		s.fail(j, e)
		return
	}
	out := filepath.Join(generations, j.ID)
	generationCommitted := false
	defer func() {
		if !generationCommitted {
			_ = os.RemoveAll(out)
		}
	}()
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.PublishTimeout)
	defer cancel()
	workBytes, e := fileTreeSize(work)
	if e != nil {
		s.fail(j, fmt.Errorf("work capacity accounting: %w", e))
		return
	}
	packageAllocationBound, e := copiedDebAllocation(work, s.cfg.DataDir)
	if e != nil {
		s.fail(j, fmt.Errorf("candidate package allocation accounting: %w", e))
		return
	}
	// TPA output is variable-sized. Account both for its logical metadata and
	// for block-rounded payload files: many small packages can occupy far more
	// tmpfs space than their summed logical bytes.
	if workBytes > (math.MaxInt64-(1<<20))/2 || packageAllocationBound > math.MaxInt64-(1<<20) {
		s.fail(j, fmt.Errorf("candidate capacity overflow"))
		return
	}
	rawCandidateBound := workBytes * 2
	if packageAllocationBound > rawCandidateBound {
		rawCandidateBound = packageAllocationBound
	}
	rawCandidateBound += 1 << 20
	if rawCandidateBound > math.MaxInt64-(allocationBlock-1) {
		s.fail(j, fmt.Errorf("candidate capacity overflow"))
		return
	}
	candidateBound := ((rawCandidateBound + allocationBlock - 1) / allocationBlock) * allocationBlock
	candidateInodes := s.cfg.MaxCandidateInodes
	if candidateInodes <= 0 {
		candidateInodes = 64
	}
	if candidateInodes < 64 {
		candidateInodes = 64
	}
	candidateReservation, overflow := addCapacity(candidateBound, candidateInodes*allocationBlock)
	if overflow {
		s.fail(j, fmt.Errorf("candidate capacity overflow"))
		return
	}
	if e = s.reserveCapacityWithResources(context.Background(), repo.AccountID, j.RepositoryID, j.ID, "candidate", candidateReservation, candidateInodes+1); e != nil {
		s.fail(j, e)
		return
	}
	request := tpa.PublishRequest{Input: work, Output: candidate}
	bounded, ok := s.runner.(boundedPublicationRunner)
	if !ok {
		s.fail(j, fmt.Errorf("publication runner does not enforce an output bound"))
		return
	}
	if boundedWithInodes, supportsInodes := s.runner.(boundedPublicationRunnerWithInodes); supportsInodes {
		e = boundedWithInodes.PublishBoundedWithInodes(ctx, request, candidateBound, candidateInodes+1)
	} else {
		e = bounded.PublishBounded(ctx, request, candidateBound)
	}
	if e != nil {
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
	if e := s.checkStorageCapacity(j.RepositoryID, candidate); e != nil {
		s.fail(j, e)
		return
	}
	verify := s.verifyCandidate
	if verify == nil {
		verify = func(candidate, work string) error { return verifyPublicationCandidate(candidate, work, s.cfg) }
	}
	if e := verify(candidate, work); e != nil {
		s.fail(j, e)
		return
	}
	candidateBytes, e := fileTreeSize(candidate)
	candidateAllocated, allocationErr := fileTreeAllocatedSize(candidate)
	if e != nil || allocationErr != nil || candidateBytes > candidateBound || candidateAllocated > candidateReservation {
		if e == nil {
			e = allocationErr
		}
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
	verifiedArtifacts, e := s.verifyPublicationArtifactIdentity(context.Background(), candidate, j.ID)
	if e != nil {
		s.fail(j, fmt.Errorf("published artifact identity verification failed: %w", e))
		return
	}
	if len(verifiedArtifacts) > 0 {
		if e = s.db.RecordPublicationVerification(context.Background(), j.ID, j.ID, "", verifiedArtifacts); e != nil {
			s.fail(j, fmt.Errorf("record publication verification: %w", e))
			return
		}
	}
	if e := syncTree(candidate); e != nil {
		s.fail(j, fmt.Errorf("sync verified candidate: %w", e))
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
	if e := syncDirectory(generations); e != nil {
		s.fail(j, fmt.Errorf("sync publication generation: %w", e))
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
	publisherAccount := j.PublisherAccountID
	if publisherAccount == "" {
		publisherAccount = repo.AccountID
	}
	_, _, e = s.repositoryPolicy().AuthorizePublication(context.Background(), publisherAccount, j.RepositoryID)
	if e != nil {
		_ = os.Remove(temporaryPointer)
		s.fail(j, e)
		return
	}
	_, previousErr := os.Lstat(pointer)
	hadPrevious := previousErr == nil
	if previousErr != nil && !os.IsNotExist(previousErr) {
		_ = os.Remove(temporaryPointer)
		s.fail(j, previousErr)
		return
	}
	swap := s.pointerSwap
	if swap == nil {
		swap = swapSymlinkPreservingPrevious
	}
	if e := swap(temporaryPointer, pointer); e != nil {
		_ = os.Remove(temporaryPointer)
		s.fail(j, e)
		return
	}
	rollback := func(cause error) {
		if rollbackErr := rollbackLiveSymlink(pointer, temporaryPointer, hadPrevious); rollbackErr != nil {
			panic(fmt.Sprintf("publication authority rollback failed: cause=%v rollback=%v", cause, rollbackErr))
		}
		if syncErr := syncDirectory(repositoryRoot); syncErr != nil {
			panic(fmt.Sprintf("publication authority rollback sync failed: cause=%v sync=%v", cause, syncErr))
		}
		s.fail(j, cause)
	}
	if e := syncDirectory(repositoryRoot); e != nil {
		rollback(fmt.Errorf("sync live publication pointer: %w", e))
		return
	}
	if e := s.publicationCheckpoint("after-live-swap"); e != nil {
		rollback(e)
		return
	}
	commitTime := s.currentTime().UTC()
	if s.durablePublicationFault != nil {
		e = s.durablePublicationFault()
	} else {
		e = s.db.CommitAuthorizedPublication(context.Background(), store.AuthorizedPublication{RepositoryID: j.RepositoryID, JobID: j.ID, OwnerAccountID: repo.AccountID}, commitTime.Format(time.RFC3339Nano), commitTime.Format(time.RFC3339Nano))
	}
	if e != nil {
		rollback(e)
		return
	}
	generationCommitted = true
	_ = os.Remove(temporaryPointer)
	if e := s.publicationCheckpoint("after-durable-record"); e != nil {
		log.Printf("publication post-commit checkpoint: repository=%s job=%s: %v", j.RepositoryID, j.ID, e)
		return
	}
	if e := s.observeAccountStorage(context.Background(), j.RepositoryID, s.currentTime()); e != nil {
		log.Printf("storage high-water update: repository=%s: %v", j.RepositoryID, e)
	}
	if e := s.publicationCheckpoint("before-job-success"); e != nil {
		log.Printf("publication post-commit checkpoint: repository=%s job=%s: %v", j.RepositoryID, j.ID, e)
		return
	}
	if e := s.collectOldGenerations(j.RepositoryID); e != nil {
		log.Printf("generation cleanup: repository=%s: %v", j.RepositoryID, e)
		s.signalCleanupRetry()
	}
	_ = os.RemoveAll(j.IncomingPath)
	log.Printf("publish job succeeded id=%s", j.ID)
}
func (s *server) collectOldGenerations(repositoryID string) error {
	err := s.collectOldGenerationsOnce(repositoryID)
	if err != nil {
		now := s.cleanupCurrentTime()
		if retryErr := s.db.SetGenerationCleanupRetry(context.Background(), repositoryID, err, now.Add(cleanupRetryPoll), now); retryErr != nil {
			return fmt.Errorf("%v; persist generation cleanup retry: %w", err, retryErr)
		}
		return err
	}
	return s.db.ClearGenerationCleanup(context.Background(), repositoryID)
}

func (s *server) collectOldGenerationsOnce(repositoryID string) error {
	if s.cfg.MaxGenerations <= 0 {
		return nil
	}
	busy, err := s.db.HasNonterminalJobs(context.Background(), repositoryID)
	if err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("generation cleanup deferred while publication is active")
	}
	active, err := s.db.ActiveGeneration(context.Background(), repositoryID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read active generation for cleanup: %w", err)
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
		if err = s.publicationCheckpoint("cleanup-old-generation"); err != nil {
			return err
		}
		if err = os.RemoveAll(filepath.Join(root, candidates[i].name)); err != nil {
			return err
		}
		if err = syncDirectory(root); err != nil {
			return fmt.Errorf("sync generation cleanup: %w", err)
		}
	}
	if err = syncDirectory(root); err != nil {
		return fmt.Errorf("sync generation directory: %w", err)
	}
	return nil
}

func (s *server) publicationCheckpoint(stage string) error {
	if s.publicationFault == nil {
		return nil
	}
	return s.publicationFault(stage)
}

func swapSymlinkPreservingPrevious(next, current string) error {
	if _, err := os.Lstat(current); os.IsNotExist(err) {
		return os.Rename(next, current)
	} else if err != nil {
		return err
	}
	return unix.Renameat2(unix.AT_FDCWD, next, unix.AT_FDCWD, current, unix.RENAME_EXCHANGE)
}

func rollbackLiveSymlink(current, previous string, hadPrevious bool) error {
	if !hadPrevious {
		return os.Remove(current)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, previous, unix.AT_FDCWD, current, unix.RENAME_EXCHANGE); err != nil {
		return err
	}
	return os.Remove(previous)
}

func syncTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot sync non-regular publication entry: %s", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err = syncDirectory(directories[i]); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func mkdirAllDurable(path string, mode os.FileMode) error {
	path = filepath.Clean(path)
	var missing []string
	for current := path; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("no existing ancestor for %s", path)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := syncDirectory(filepath.Dir(missing[i])); err != nil {
			return fmt.Errorf("sync parent for %s: %w", missing[i], err)
		}
	}
	return nil
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

func verifyPublicationCandidate(candidate, work string, cfg config.Config) error {
	if cfg.GPGFingerprint == "" {
		return fmt.Errorf("publication signing fingerprint is not configured")
	}
	verificationTimeout := cfg.PublishTimeout
	if verificationTimeout <= 0 {
		verificationTimeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), verificationTimeout)
	defer cancel()
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("publication candidate is not a directory")
	}
	indexedPayloads := map[string]candidatePackage{}
	candidatePayloads := map[string]bool{}
	err = filepath.WalkDir(candidate, func(path string, entry os.DirEntry, err error) error {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if err != nil {
			return err
		}
		if path == candidate || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("publication candidate contains symlink: %s", path)
		}
		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("publication candidate contains non-regular file: %s", path)
		}
		relative, relativeErr := filepath.Rel(candidate, path)
		if relativeErr != nil {
			return relativeErr
		}
		relative = filepath.ToSlash(relative)
		if strings.HasPrefix(relative, "pool/") {
			candidatePayloads[relative] = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("publication candidate unreadable: %w", err)
	}
	distDir := filepath.Join(candidate, "dists", "stable")
	releasePath := filepath.Join(distDir, "Release")
	inReleasePath := filepath.Join(distDir, "InRelease")
	releaseBytes, err := os.ReadFile(releasePath)
	if err != nil {
		return fmt.Errorf("publication candidate missing Release: %w", err)
	}
	checksums, releaseFields, err := parseCandidateRelease(releaseBytes)
	if err != nil {
		return err
	}
	if releaseFields["Suite"] != "stable" || releaseFields["Codename"] != "stable" || !strings.Contains(" "+releaseFields["Components"]+" ", " main ") {
		return fmt.Errorf("publication candidate Release identity is invalid")
	}
	releaseArchitectures := map[string]bool{}
	for _, architecture := range strings.Fields(releaseFields["Architectures"]) {
		if !aptArchitectureRE.MatchString(architecture) || releaseArchitectures[architecture] {
			return fmt.Errorf("publication candidate Release architectures are invalid")
		}
		releaseArchitectures[architecture] = true
	}
	if len(releaseArchitectures) == 0 {
		return fmt.Errorf("publication candidate Release has no architectures")
	}
	if err = verifyCandidateSignature(ctx, inReleasePath, releaseBytes, cfg); err != nil {
		return err
	}
	for relative, expected := range checksums {
		path, pathErr := candidateRelativePath(distDir, relative)
		if pathErr != nil {
			return fmt.Errorf("invalid Release path %q: %w", relative, pathErr)
		}
		if fileErr := verifyCandidateFile(ctx, path, expected.size, expected.sha256); fileErr != nil {
			return fmt.Errorf("Release entry %s: %w", relative, fileErr)
		}
	}
	componentDir := filepath.Join(distDir, "main")
	binaryDirs, err := os.ReadDir(componentDir)
	if err != nil {
		return fmt.Errorf("publication candidate missing main metadata: %w", err)
	}
	binaryCount := 0
	indexedArchitectures := map[string]bool{}
	for _, binaryDir := range binaryDirs {
		if !binaryDir.IsDir() || !strings.HasPrefix(binaryDir.Name(), "binary-") || len(binaryDir.Name()) == len("binary-") {
			continue
		}
		architecture := strings.TrimPrefix(binaryDir.Name(), "binary-")
		if !aptArchitectureRE.MatchString(architecture) || !releaseArchitectures[architecture] {
			return fmt.Errorf("publication candidate has undeclared architecture index: %s", architecture)
		}
		indexedArchitectures[architecture] = true
		binaryCount++
		relativePackages := filepath.ToSlash(filepath.Join("main", binaryDir.Name(), "Packages"))
		relativeGzip := relativePackages + ".gz"
		if _, ok := checksums[relativePackages]; !ok {
			return fmt.Errorf("Release missing checksum for %s", relativePackages)
		}
		if _, ok := checksums[relativeGzip]; !ok {
			return fmt.Errorf("Release missing checksum for %s", relativeGzip)
		}
		packagesPath := filepath.Join(componentDir, binaryDir.Name(), "Packages")
		entries, indexErr := verifyPackageIndex(ctx, candidate, packagesPath, architecture)
		if indexErr != nil {
			return indexErr
		}
		compressed, gzipErr := os.Open(packagesPath + ".gz")
		if gzipErr != nil {
			return gzipErr
		}
		gz, gzipErr := gzip.NewReader(compressed)
		if gzipErr != nil {
			compressed.Close()
			return fmt.Errorf("invalid Packages.gz: %w", gzipErr)
		}
		expectedPackages := checksums[relativePackages]
		uncompressedSize, uncompressedDigest, gzipErr := digestReader(ctx, gz, expectedPackages.size)
		closeErr := gz.Close()
		compressed.Close()
		if gzipErr != nil || closeErr != nil || uncompressedSize != expectedPackages.size || uncompressedDigest != expectedPackages.sha256 {
			return fmt.Errorf("Packages.gz does not match Packages")
		}
		for name, checksum := range entries {
			if existing, duplicate := indexedPayloads[name]; duplicate {
				if checksum != existing || !strings.HasSuffix(checksum.identity, "\x00all") {
					return fmt.Errorf("package payload indexed more than once: %s", name)
				}
				continue
			}
			indexedPayloads[name] = checksum
		}
	}
	if binaryCount == 0 {
		return fmt.Errorf("publication candidate has no binary package metadata")
	}
	for architecture := range releaseArchitectures {
		if !indexedArchitectures[architecture] {
			return fmt.Errorf("publication candidate Release architecture has no index: %s", architecture)
		}
	}
	for path := range candidatePayloads {
		if _, indexed := indexedPayloads[path]; !indexed {
			return fmt.Errorf("candidate pool payload is not indexed: %s", path)
		}
	}
	if len(candidatePayloads) != len(indexedPayloads) {
		return fmt.Errorf("candidate index/pool payload set mismatch")
	}
	err = filepath.WalkDir(work, func(path string, entry os.DirEntry, walkErr error) error {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".deb") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("work package is a symlink: %s", path)
		}
		identity, identityErr := debIdentity(ctx, path)
		if identityErr != nil {
			return fmt.Errorf("read work package identity %s: %w", entry.Name(), identityErr)
		}
		var candidateName string
		var expected candidatePackage
		for name, candidate := range indexedPayloads {
			if candidate.identity == identity {
				if candidateName != "" {
					return fmt.Errorf("ambiguous candidate package identity: %s", identity)
				}
				candidateName, expected = name, candidate
			}
		}
		if candidateName == "" {
			return fmt.Errorf("package missing from candidate index: %s", entry.Name())
		}
		if fileErr := verifyCandidateFile(ctx, path, expected.size, expected.sha256); fileErr != nil {
			return fmt.Errorf("candidate package differs from input %s: %w", entry.Name(), fileErr)
		}
		delete(indexedPayloads, candidateName)
		return nil
	})
	if err != nil {
		return err
	}
	if len(indexedPayloads) != 0 {
		return fmt.Errorf("candidate contains package payload absent from input")
	}
	return nil
}

type candidateChecksum struct {
	sha256 string
	size   int64
}

type candidatePackage struct {
	candidateChecksum
	identity string
}

func parseCandidateRelease(data []byte) (map[string]candidateChecksum, map[string]string, error) {
	checksums := map[string]candidateChecksum{}
	metadata := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	inSHA := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "SHA256:" {
			inSHA = true
			continue
		}
		if inSHA && strings.HasPrefix(line, " ") {
			fields := strings.Fields(line)
			if len(fields) != 3 || len(fields[0]) != sha256.Size*2 {
				return nil, nil, fmt.Errorf("malformed Release SHA256 entry")
			}
			if _, decodeErr := hex.DecodeString(fields[0]); decodeErr != nil {
				return nil, nil, fmt.Errorf("malformed Release SHA256 digest")
			}
			size, parseErr := strconv.ParseInt(fields[1], 10, 64)
			if parseErr != nil || size < 0 {
				return nil, nil, fmt.Errorf("malformed Release size")
			}
			if _, duplicate := checksums[fields[2]]; duplicate {
				return nil, nil, fmt.Errorf("duplicate Release path: %s", fields[2])
			}
			checksums[fields[2]] = candidateChecksum{sha256: strings.ToLower(fields[0]), size: size}
			continue
		}
		inSHA = false
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			if _, duplicate := metadata[parts[0]]; duplicate {
				return nil, nil, fmt.Errorf("duplicate Release field: %s", parts[0])
			}
			metadata[parts[0]] = strings.TrimSpace(parts[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	if len(checksums) == 0 {
		return nil, nil, fmt.Errorf("Release has no SHA256 entries")
	}
	return checksums, metadata, nil
}

func verifyCandidateSignature(ctx context.Context, inRelease string, release []byte, cfg config.Config) error {
	command := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "gpg", args...)
		cmd.Env = os.Environ()
		if cfg.GPGHome != "" {
			cmd.Env = append(cmd.Env, "GNUPGHOME="+cfg.GPGHome)
		}
		return cmd
	}
	status, err := command("--batch", "--status-fd=1", "--verify", inRelease).CombinedOutput()
	if err != nil {
		return fmt.Errorf("publication candidate signature invalid: %w", err)
	}
	want := strings.ToUpper(cfg.GPGFingerprint)
	valid := false
	for _, line := range strings.Split(string(status), "\n") {
		marker := "[GNUPG:] VALIDSIG "
		position := strings.Index(line, marker)
		if position < 0 {
			continue
		}
		fields := strings.Fields(line[position+len(marker):])
		if len(fields) > 0 && (strings.ToUpper(fields[0]) == want || (len(fields) > 9 && strings.ToUpper(fields[len(fields)-1]) == want)) {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("publication candidate signature key mismatch")
	}
	plain, err := command("--batch", "--decrypt", inRelease).Output()
	if err != nil || !bytes.Equal(plain, release) {
		return fmt.Errorf("publication candidate signed payload differs from Release")
	}
	return nil
}

func candidateRelativePath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || strings.Contains(relative, "\\") {
		return "", fmt.Errorf("path is not a relative slash path")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != relative {
		return "", fmt.Errorf("path escapes repository")
	}
	return filepath.Join(root, clean), nil
}

func verifyCandidateFile(ctx context.Context, path string, size int64, digest string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return fmt.Errorf("file type or size mismatch")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	actualSize, actualDigest, err := digestReader(ctx, file, size)
	if err != nil {
		return err
	}
	if actualSize != size || actualDigest != digest {
		return fmt.Errorf("SHA256 mismatch")
	}
	return nil
}

func digestReader(ctx context.Context, reader io.Reader, maximum int64) (int64, string, error) {
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		n, err := reader.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > maximum {
				return 0, "", fmt.Errorf("content exceeds declared size")
			}
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(err, io.EOF) {
			return total, hex.EncodeToString(hash.Sum(nil)), nil
		}
		if err != nil {
			return 0, "", err
		}
	}
}

func verifyPackageIndex(ctx context.Context, candidate, packagesPath, indexArchitecture string) (map[string]candidatePackage, error) {
	f, err := os.Open(packagesPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	indexed := map[string]candidatePackage{}
	fields := map[string]string{}
	verifyStanza := func() error {
		if len(fields) == 0 {
			return nil
		}
		name := fields["Filename"]
		if fields["Package"] == "" || fields["Version"] == "" || fields["Architecture"] == "" || name == "" || fields["Size"] == "" || fields["SHA256"] == "" {
			return fmt.Errorf("incomplete Packages stanza")
		}
		if fields["Architecture"] != indexArchitecture && fields["Architecture"] != "all" {
			return fmt.Errorf("package architecture %s is invalid for binary-%s", fields["Architecture"], indexArchitecture)
		}
		if !strings.HasPrefix(name, "pool/") {
			return fmt.Errorf("package Filename is outside pool")
		}
		sz, e := strconv.ParseInt(fields["Size"], 10, 64)
		if e != nil || sz < 0 {
			return fmt.Errorf("invalid package size")
		}
		path, e := candidateRelativePath(candidate, name)
		if e != nil {
			return fmt.Errorf("invalid package Filename: %w", e)
		}
		digest := strings.ToLower(fields["SHA256"])
		if len(digest) != sha256.Size*2 {
			return fmt.Errorf("invalid package SHA256")
		}
		if e = verifyCandidateFile(ctx, path, sz, digest); e != nil {
			return fmt.Errorf("package payload mismatch: %w", e)
		}
		if _, duplicate := indexed[name]; duplicate {
			return fmt.Errorf("duplicate package payload: %s", name)
		}
		identity, identityErr := debIdentity(ctx, path)
		if identityErr != nil {
			return fmt.Errorf("read candidate package identity: %w", identityErr)
		}
		declaredIdentity := strings.Join([]string{fields["Package"], fields["Version"], fields["Architecture"]}, "\x00")
		if identity != declaredIdentity {
			return fmt.Errorf("Packages identity does not match package control metadata")
		}
		indexed[name] = candidatePackage{candidateChecksum: candidateChecksum{size: sz, sha256: digest}, identity: identity}
		return nil
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		line := scanner.Text()
		if line == "" {
			if err = verifyStanza(); err != nil {
				return nil, err
			}
			fields = map[string]string{}
			continue
		}
		if strings.HasPrefix(line, " ") {
			continue
		}
		kv := strings.SplitN(line, ":", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("truncated Packages index")
		}
		if _, duplicate := fields[kv[0]]; duplicate {
			return nil, fmt.Errorf("duplicate Packages field: %s", kv[0])
		}
		fields[kv[0]] = strings.TrimSpace(kv[1])
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	if err = verifyStanza(); err != nil {
		return nil, err
	}
	if len(indexed) == 0 {
		return nil, fmt.Errorf("empty Packages index")
	}
	return indexed, nil
}

func debIdentity(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "dpkg-deb", "--show", "--showformat=${Package}\n${Version}\n${Architecture}\n", path)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var output bytes.Buffer
	var diagnostic boundedDiagnostic
	cmd.Stdout = &output
	cmd.Stderr = &diagnostic
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("dpkg-deb: %w: %s", err, strings.TrimSpace(diagnostic.String()))
	}
	fields := strings.Fields(output.String())
	if len(fields) != 3 || fields[0] == "" || fields[1] == "" || fields[2] == "" {
		return "", fmt.Errorf("invalid .deb control identity")
	}
	return strings.Join(fields, "\x00"), nil
}

func (s *server) reconcileStagingState() error {
	openSessions, err := s.db.OpenPublicationSessions(context.Background())
	if err != nil {
		return fmt.Errorf("load incomplete publication sessions: %w", err)
	}
	for _, session := range openSessions {
		if filepath.Clean(session.IncomingPath) != filepath.Join(s.cfg.DataDir, "incoming", session.ID) {
			return fmt.Errorf("incomplete publication session has an invalid path: %s", session.ID)
		}
		if err = s.removePath(session.IncomingPath); err != nil {
			return fmt.Errorf("remove incomplete publication session %s: %w", session.ID, err)
		}
		if err = s.db.DeleteOpenPublicationSession(context.Background(), session.ID); err != nil {
			return fmt.Errorf("discard incomplete publication session %s: %w", session.ID, err)
		}
	}
	pending, err := s.db.PendingCleanupJobs(context.Background())
	if err != nil {
		return fmt.Errorf("load pending publication cleanup: %w", err)
	}
	for _, job := range pending {
		if err = s.cleanupPublishJob(store.Job{ID: job.ID, IncomingPath: job.IncomingPath}); err != nil {
			if retryErr := s.db.SetCleanupRetry(context.Background(), job.ID, err, s.cleanupRetryAt(s.cleanupCurrentTime(), job.Attempts+1)); retryErr != nil {
				return fmt.Errorf("record cleanup retry for %s: %w", job.ID, retryErr)
			}
			log.Printf("startup publication cleanup deferred: job=%s: %v", job.ID, err)
			continue
		}
		if err = s.db.SetCleanupResult(context.Background(), job.ID, nil); err != nil {
			return fmt.Errorf("record publication cleanup %s: %w", job.ID, err)
		}
	}
	return s.reconcileOrphanedStagingState()
}

func (s *server) reconcileOrphanedStagingState() error {
	if s.capacityObserve != nil {
		s.capacityObserve("before-cleanup", s.cfg.DataDir)
	}
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	paths, err := s.db.TerminalJobIncomingPaths(context.Background())
	if err != nil {
		return fmt.Errorf("scan terminal staging state: %w", err)
	}
	for _, path := range paths {
		if err := s.publicationCheckpoint("cleanup-terminal-incoming"); err != nil {
			return fmt.Errorf("cleanup terminal incoming: %w", err)
		}
		if err := s.removePath(path); err != nil {
			log.Printf("deferred terminal incoming cleanup: path=%s: %v", path, err)
		}
	}
	activePaths, err := s.db.ActiveStagingPaths(context.Background())
	if err != nil {
		return fmt.Errorf("scan active incoming state: %w", err)
	}
	active := make(map[string]struct{}, len(activePaths))
	for _, path := range activePaths {
		active[filepath.Clean(path)] = struct{}{}
	}
	// Upload copying releases stagingMu, but retains its incoming reservation.
	// Under this lock ownership transfers atomically from reservation to job.
	// Neither representation may be mistaken for an orphan by live cleanup.
	for _, reservation := range s.reservations {
		if reservation.Kind == "incoming" {
			active[filepath.Clean(filepath.Join(s.cfg.DataDir, "incoming", reservation.JobID))] = struct{}{}
		}
	}
	incoming := filepath.Join(s.cfg.DataDir, "incoming")
	incomingEntries, err := os.ReadDir(incoming)
	if os.IsNotExist(err) {
		incomingEntries = nil
	} else if err != nil {
		return fmt.Errorf("scan incoming state: %w", err)
	}
	for _, entry := range incomingEntries {
		path := filepath.Join(incoming, entry.Name())
		if _, ok := active[filepath.Clean(path)]; ok {
			continue
		}
		if err = s.publicationCheckpoint("cleanup-orphaned-incoming"); err != nil {
			return fmt.Errorf("cleanup orphaned incoming: %w", err)
		}
		if err = s.removePath(path); err != nil {
			log.Printf("deferred orphaned incoming cleanup: path=%s: %v", path, err)
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
		if err = s.publicationCheckpoint("cleanup-abandoned-work"); err != nil {
			return fmt.Errorf("cleanup abandoned work: %w", err)
		}
		if err = s.removePath(filepath.Join(work, entry.Name())); err != nil {
			log.Printf("deferred abandoned staging cleanup: path=%s: %v", filepath.Join(work, entry.Name()), err)
		}
	}
	return nil
}

func (s *server) cleanupPublishJob(j store.Job) error {
	paths := []string{j.IncomingPath, filepath.Join(s.cfg.DataDir, "work", j.ID), filepath.Join(s.cfg.DataDir, "work", j.ID+"-published")}
	for _, path := range paths {
		if path == "" {
			continue
		}
		if err := s.removePath(path); err != nil {
			return err
		}
	}
	return nil
}

const cleanupRetryPoll = 100 * time.Millisecond

func (s *server) removePath(path string) error {
	if s.removeAll != nil {
		return s.removeAll(path)
	}
	return os.RemoveAll(path)
}

func (s *server) cleanupCurrentTime() time.Time {
	if s.cleanupNow != nil {
		return s.cleanupNow().UTC()
	}
	return s.currentTime().UTC()
}

func (s *server) cleanupRetryAt(now time.Time, attempt int64) time.Time {
	if attempt < 1 {
		attempt = 1
	}
	delay := 100 * time.Millisecond
	for i := int64(1); i < attempt && delay < time.Minute; i++ {
		delay *= 2
		if delay >= time.Minute {
			delay = time.Minute
		}
	}
	return now.Add(delay)
}

func (s *server) signalCleanupRetry() {
	if s.cleanupWake == nil {
		return
	}
	select {
	case s.cleanupWake <- struct{}{}:
	default:
	}
}

func (s *server) cleanupRetryLoop() {
	ticker := time.NewTicker(cleanupRetryPoll)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.retryPendingCleanup(s.cleanupCurrentTime()); err != nil {
				log.Printf("publish cleanup retry: %v", err)
			}
			if err := s.reconcileOrphanedStagingState(); err != nil {
				log.Printf("orphaned staging cleanup: %v", err)
			}
			if err := s.retryPendingGenerationCleanup(s.cleanupCurrentTime()); err != nil {
				log.Printf("generation cleanup retry: %v", err)
			}
			if err := s.reconcileOrphanedRepositoryRoots(); err != nil {
				log.Printf("orphaned repository cleanup: %v", err)
			}
		case <-s.cleanupWake:
			if err := s.retryPendingCleanup(s.cleanupCurrentTime()); err != nil {
				log.Printf("publish cleanup retry: %v", err)
			}
			if err := s.reconcileOrphanedStagingState(); err != nil {
				log.Printf("orphaned staging cleanup: %v", err)
			}
			if err := s.retryPendingGenerationCleanup(s.cleanupCurrentTime()); err != nil {
				log.Printf("generation cleanup retry: %v", err)
			}
			if err := s.reconcileOrphanedRepositoryRoots(); err != nil {
				log.Printf("orphaned repository cleanup: %v", err)
			}
		}
	}
}

func (s *server) retryPendingGenerationCleanup(now time.Time) error {
	tasks, err := s.db.PendingGenerationCleanup(context.Background())
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if next, parseErr := time.Parse(time.RFC3339Nano, task.NextRetry); parseErr == nil && next.After(now) {
			continue
		}
		s.stagingMu.Lock()
		err = s.collectOldGenerations(task.RepositoryID)
		s.stagingMu.Unlock()
		if err != nil {
			continue
		}
	}
	return nil
}

func (s *server) retryPendingCleanup(now time.Time) error {
	pending, err := s.db.PendingCleanupJobs(context.Background())
	if err != nil {
		return err
	}
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	for _, job := range pending {
		if job.NextRetry != "" {
			next, parseErr := time.Parse(time.RFC3339Nano, job.NextRetry)
			if parseErr == nil && next.After(now) {
				continue
			}
		}
		cleanupErr := s.cleanupPublishJob(store.Job{ID: job.ID, IncomingPath: job.IncomingPath})
		if cleanupErr != nil {
			if err = s.db.SetCleanupRetry(context.Background(), job.ID, cleanupErr, s.cleanupRetryAt(now, job.Attempts+1)); err != nil {
				return err
			}
			continue
		}
		if err = s.db.SetCleanupResult(context.Background(), job.ID, nil); err != nil {
			return err
		}
		s.releaseCapacity(job.ID)
	}
	return nil
}

func (s *server) reconcilePublicationState() error {
	root := filepath.Join(s.cfg.DataDir, "repos")
	repositories, err := s.db.AllRepositories(context.Background())
	if err != nil {
		return fmt.Errorf("load durable repositories: %w", err)
	}
	if len(repositories) > 0 {
		if err = mkdirAllDurable(root, 0755); err != nil {
			return fmt.Errorf("restore repository root: %w", err)
		}
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scan repository publication state: %w", err)
	}
	ids := make([]string, 0, len(repositories))
	seen := make(map[string]bool, len(repositories))
	for _, repository := range repositories {
		ids = append(ids, repository.ID)
		seen[repository.ID] = true
		if err = mkdirAllDurable(filepath.Join(root, repository.ID), 0755); err != nil {
			return fmt.Errorf("restore durable repository root %s: %w", repository.ID, err)
		}
	}
	for _, entry := range entries {
		if entry.IsDir() && !seen[entry.Name()] {
			if err = s.removePath(filepath.Join(root, entry.Name())); err != nil {
				return fmt.Errorf("remove repository root without durable row: %w", err)
			}
			if err = syncDirectory(root); err != nil {
				return fmt.Errorf("sync orphaned repository cleanup: %w", err)
			}
		}
	}
	for _, repositoryID := range ids {
		repositoryRoot := filepath.Join(root, repositoryID)
		pointer := filepath.Join(repositoryRoot, "current")
		// Temporary current.<job> and current.reconcile pointers are never
		// authoritative; durable repository_publications is the only recovery source.
		repositoryEntries, readErr := os.ReadDir(repositoryRoot)
		if readErr != nil {
			return fmt.Errorf("scan temporary publication pointers: %w", readErr)
		}
		for _, candidate := range repositoryEntries {
			if !strings.HasPrefix(candidate.Name(), "current.") {
				continue
			}
			if err = s.publicationCheckpoint("cleanup-temporary-pointer"); err != nil {
				return fmt.Errorf("cleanup temporary pointer: %w", err)
			}
			if err = os.Remove(filepath.Join(repositoryRoot, candidate.Name())); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove stale publication pointer: %w", err)
			}
		}
		generation, generationErr := s.db.ActiveGeneration(context.Background(), repositoryID)
		if generationErr != nil {
			if errors.Is(generationErr, store.ErrNotFound) {
				if err = os.Remove(pointer); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove uncommitted publication pointer: %w", err)
				}
				if err = s.removeFailedGenerations(repositoryRoot, ""); err != nil {
					return err
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
			if err = s.removeFailedGenerations(repositoryRoot, generation); err != nil {
				return err
			}
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
		if err = syncDirectory(repositoryRoot); err != nil {
			return fmt.Errorf("sync restored publication pointer: %w", err)
		}
		if err = s.removeFailedGenerations(repositoryRoot, generation); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) reconcileOrphanedRepositoryRoots() error {
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	repositories, err := s.db.AllRepositories(context.Background())
	if err != nil {
		return err
	}
	durable := make(map[string]bool, len(repositories))
	for _, repository := range repositories {
		durable[repository.ID] = true
	}
	for _, rootName := range []string{"repos", "artifacts"} {
		root := filepath.Join(s.cfg.DataDir, rootName)
		entries, readErr := os.ReadDir(root)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		for _, entry := range entries {
			if !entry.IsDir() || durable[entry.Name()] {
				continue
			}
			if err = s.removePath(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
			if err = syncDirectory(root); err != nil {
				return err
			}
		}
		if err = syncDirectory(root); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) removeFailedGenerations(repositoryRoot, active string) error {
	root := filepath.Join(repositoryRoot, "generations")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scan publication generations: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == active {
			continue
		}
		job, jobErr := s.db.Job(context.Background(), entry.Name())
		if errors.Is(jobErr, store.ErrNotFound) || jobErr == nil && job.State != "failed" {
			continue
		}
		if jobErr != nil {
			return fmt.Errorf("inspect publication generation job: %w", jobErr)
		}
		if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("remove uncommitted publication generation: %w", err)
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

func (s *server) publishedRepositoryRoot(ctx context.Context, repositoryID string) (string, error) {
	root := filepath.Join(s.cfg.DataDir, "repos", repositoryID)
	generation, err := s.db.ActiveGeneration(ctx, repositoryID)
	if err == nil {
		path := filepath.Join(root, "generations", generation)
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			if statErr == nil {
				statErr = fmt.Errorf("active generation is not a directory")
			}
			return "", statErr
		}
		return path, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if _, pointerErr := os.Lstat(filepath.Join(root, "current")); pointerErr == nil {
		return "", store.ErrNotFound
	} else if !os.IsNotExist(pointerErr) {
		return "", pointerErr
	}
	// Pre-generation repositories stored payloads directly in the repository root.
	return root, nil
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

func (s *server) ensurePublishAccountActive(repositoryID string) error {
	repository, err := s.repositoryPolicy().Resolve(context.Background(), repositoryID)
	if err != nil {
		return err
	}
	_, err = s.repositoryPolicy().AuthorizeMutation(context.Background(), repository.AccountID, repositoryID)
	return err
}

// reserveCapacity is kept as a compatibility helper for narrow unit tests.
func (s *server) reserveCapacity(ctx context.Context, accountID, repositoryID, jobID, kind string, bytes int64) error {
	return s.reserveCapacityWithResources(ctx, accountID, repositoryID, jobID, kind, bytes, 1)
}

// reserveCapacityWithResources is called while stagingMu is held. Reservations
// are process-local; restart reconciliation treats durable filesystem state as
// actual bytes and the statfs result as actual inode usage.
func (s *server) reserveCapacityWithResources(ctx context.Context, accountID, repositoryID, jobID, kind string, bytes, inodes int64) error {
	// Infrastructure reservations never consume customer payload entitlement.
	if bytes < 0 {
		return fmt.Errorf("negative capacity reservation")
	}
	if inodes < 0 {
		return fmt.Errorf("negative inode reservation")
	}
	if s.reservations == nil {
		s.reservations = make(map[string]capacityReservation)
	}
	oldBytes, oldInodes := int64(0), int64(0)
	if old, ok := s.reservations[jobID]; ok {
		if old.AccountID != accountID || old.RepositoryID != repositoryID || old.Kind != kind {
			return fmt.Errorf("physical reservation identity conflict")
		}
		oldBytes = old.Bytes
		oldInodes = old.Inodes
	}
	snapshot, err := s.physicalCapacity(ctx, accountID)
	if err != nil {
		return err
	}
	prospective, overflow := addCapacity(snapshot.committed, snapshot.reserved, bytes)
	if overflow || s.cfg.MaxStagingBytes > 0 && prospective > s.cfg.MaxStagingBytes {
		return fmt.Errorf("temporary publication capacity unavailable: global staging reservation exceeded")
	}
	additional, overflow := addCapacity(snapshot.remaining, bytes)
	if overflow {
		return fmt.Errorf("capacity reservation overflow")
	}
	if err = s.checkDiskHeadroomFor(additional); err != nil {
		return err
	}
	additionalInodes, overflow := addCapacity(snapshot.remainingInodes, inodes)
	if overflow {
		return fmt.Errorf("inode reservation overflow")
	}
	if err = s.checkInodeHeadroomFor(additionalInodes); err != nil {
		return err
	}
	if accountID != "" && s.cfg.MaxAccountStagingBytes > 0 {
		prospectiveAccount, accountOverflow := addCapacity(snapshot.accountCommitted, snapshot.accountReserved, bytes)
		if accountOverflow || prospectiveAccount > s.cfg.MaxAccountStagingBytes {
			return fmt.Errorf("temporary publication capacity unavailable: account staging reservation exceeded")
		}
	}
	newBytes, overflow := addCapacity(oldBytes, bytes)
	if overflow {
		return fmt.Errorf("capacity reservation overflow")
	}
	newInodes, overflow := addCapacity(oldInodes, inodes)
	if overflow {
		return fmt.Errorf("inode reservation overflow")
	}
	s.reservations[jobID] = capacityReservation{AccountID: accountID, RepositoryID: repositoryID, JobID: jobID, Kind: kind, Bytes: newBytes, Inodes: newInodes}
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
		return s.reserveCapacityWithResources(context.Background(), r.AccountID, r.RepositoryID, jobID, r.Kind, bytes-r.Bytes, 0)
	}
	snapshot, err := s.physicalCapacity(context.Background(), r.AccountID)
	if err != nil {
		return err
	}
	if bytes == 0 {
		delete(s.reservations, jobID)
		return nil
	}
	if bytes < snapshot.used[jobID].bytes {
		return fmt.Errorf("physical reservation below materialized allocation")
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
	if s.cfg.MinDiskFreeBytes < 0 {
		return fmt.Errorf("invalid disk headroom floor")
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
		if stat.Bsize <= 0 || stat.Bavail > uint64(math.MaxInt64)/uint64(stat.Bsize) {
			return fmt.Errorf("disk headroom unavailable: allocation overflow")
		}
		free = int64(stat.Bavail) * int64(stat.Bsize)
	}
	if free < s.cfg.MinDiskFreeBytes || additional > free-s.cfg.MinDiskFreeBytes {
		return fmt.Errorf("minimum disk headroom unavailable")
	}
	return nil
}

func inodeHeadroomFromStat(stat unix.Statfs_t) (free int64, known bool, err error) {
	// Some filesystems, including Btrfs, report no fixed inode pool through
	// statfs. Files==0 is the portable signal that Ffree is not an exhaustion
	// metric; zero free inodes is meaningful only when a total is reported.
	if stat.Files == 0 {
		return 0, false, nil
	}
	if stat.Ffree > math.MaxInt64 {
		return 0, false, fmt.Errorf("inode count overflow")
	}
	return int64(stat.Ffree), true, nil
}

func checkInodeHeadroomValues(free int64, known bool, minimum, additional int64) error {
	if additional < 0 {
		return fmt.Errorf("invalid inode reservation")
	}
	if minimum <= 0 || !known {
		return nil
	}
	if free < minimum || additional > free-minimum {
		return fmt.Errorf("minimum inode headroom unavailable")
	}
	return nil
}

func (s *server) checkInodeHeadroomFor(additional int64) error {
	if additional < 0 {
		return fmt.Errorf("invalid inode reservation")
	}
	if s.cfg.MinFreeInodes <= 0 {
		return nil
	}
	var free int64
	known := true
	if s.freeInodes != nil {
		var err error
		free, err = s.freeInodes()
		if err != nil {
			return fmt.Errorf("inode headroom unavailable: %w", err)
		}
	} else {
		var stat unix.Statfs_t
		if err := unix.Statfs(s.cfg.DataDir, &stat); err != nil {
			return fmt.Errorf("inode headroom unavailable: %w", err)
		}
		var err error
		free, known, err = inodeHeadroomFromStat(stat)
		if err != nil {
			return fmt.Errorf("inode headroom unavailable: %w", err)
		}
	}
	return checkInodeHeadroomValues(free, known, s.cfg.MinFreeInodes, additional)
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
	grant, err := s.grantService().GetGrant(context.Background(), commerce.CustomerID(repository.AccountID), tpahubproduct.ProductID, CapabilityRepositoryPublish, s.currentTime())
	if err != nil {
		return fmt.Errorf("storage grant unavailable: %w", err)
	}
	capacity := grant.Limits.StorageBytes
	if capacity <= 0 {
		return fmt.Errorf("storage grant has no capacity")
	}
	if currentRepo.StoredBytes > account.StoredBytes {
		return fmt.Errorf("repository storage usage exceeds account usage")
	}
	resulting, overflow := addCapacity(account.StoredBytes-currentRepo.StoredBytes, prospective.StoredBytes)
	if overflow {
		return fmt.Errorf("resulting storage usage overflow")
	}
	if resulting > capacity && resulting > account.StoredBytes {
		return StorageCapacityError{CurrentBytes: account.StoredBytes, ResultingBytes: resulting, CapacityBytes: capacity}
	}
	return s.checkActiveRepositoryCapacity(repository.AccountID, repositoryID, candidate, capacity)
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

func fileTreeAllocatedSize(root string) (int64, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	block, err := filesystemAllocationBlock(root)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) && path == root {
				return nil
			}
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		allocated, err := allocatedFileBytes(info, block)
		if err != nil {
			return err
		}
		if allocated > math.MaxInt64-total {
			return fmt.Errorf("filesystem allocation overflow")
		}
		total += allocated
		return nil
	})
	return total, err
}

func filesystemAllocationBlock(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, fmt.Errorf("filesystem block size unavailable")
	}
	return int64(stat.Bsize), nil
}

func copiedDebAllocation(root, destinationFilesystem string) (int64, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	root = resolved
	block, err := filesystemAllocationBlock(destinationFilesystem)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) && path == root {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".deb") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("package input is a symlink: %s", path)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() || info.Size() > math.MaxInt64-(block-1) {
			return fmt.Errorf("package input is not a bounded regular file: %s", path)
		}
		allocated := ((info.Size() + block - 1) / block) * block
		// Conservatively reserve one additional block per destination directory
		// entry so many tiny packages cannot bypass physical work accounting.
		if allocated > math.MaxInt64-block {
			return fmt.Errorf("copy allocation overflow")
		}
		allocated += block
		if allocated > math.MaxInt64-total {
			return fmt.Errorf("copy allocation overflow")
		}
		total += allocated
		return nil
	})
	return total, err
}

func copiedDebInodes(root string) (int64, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var count int64
	err = filepath.WalkDir(resolved, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".deb") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("package input is a symlink: %s", path)
		}
		if _, err := entry.Info(); err != nil {
			return err
		}
		if count == math.MaxInt64 {
			return fmt.Errorf("inode accounting overflow")
		}
		count++
		return nil
	})
	return count, err
}

func addCapacity(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || value > math.MaxInt64-total {
			return 0, true
		}
		total += value
	}
	return total, false
}

func copyDebs(src, dst string, replace bool) error {
	resolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	src = resolved
	return filepath.WalkDir(src, func(in string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) && in == src {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".deb") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("package input is a symlink: %s", in)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("package input is not a regular file: %s", in)
		}
		out := filepath.Join(dst, entry.Name())
		a, e := os.Open(in)
		if e != nil {
			return e
		}
		if replace {
			if _, statErr := os.Stat(out); statErr == nil {
				equal, compareErr := filesEqual(in, out)
				if compareErr != nil {
					a.Close()
					return compareErr
				}
				if equal {
					a.Close()
					return nil
				}
				digest, digestErr := fileDigest(a, info.Size())
				if digestErr != nil {
					a.Close()
					return digestErr
				}
				if _, seekErr := a.Seek(0, io.SeekStart); seekErr != nil {
					a.Close()
					return seekErr
				}
				stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
				out = filepath.Join(dst, stem+"-"+digest[:16]+filepath.Ext(entry.Name()))
				if _, derivedErr := os.Stat(out); derivedErr == nil {
					equal, compareErr = filesEqual(in, out)
					if compareErr != nil {
						a.Close()
						return compareErr
					}
					if equal {
						a.Close()
						return nil
					}
					a.Close()
					return fmt.Errorf("digest-derived package collision: %s", out)
				} else if !os.IsNotExist(derivedErr) {
					a.Close()
					return derivedErr
				}
			}
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
		return nil
	})
}

func fileDigest(file *os.File, size int64) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func filesEqual(left, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false, err
	}
	if leftInfo.Size() != rightInfo.Size() {
		return false, nil
	}
	leftFile, err := os.Open(left)
	if err != nil {
		return false, err
	}
	defer leftFile.Close()
	rightFile, err := os.Open(right)
	if err != nil {
		return false, err
	}
	defer rightFile.Close()
	leftDigest, err := fileDigest(leftFile, leftInfo.Size())
	if err != nil {
		return false, err
	}
	rightDigest, err := fileDigest(rightFile, rightInfo.Size())
	return leftDigest == rightDigest, err
}

func (s *server) fail(j store.Job, e error) {
	msg := e.Error()
	_ = s.db.Finish(context.Background(), j.ID, "failed", msg)
	log.Printf("publish job failed id=%s error=%s", j.ID, msg)
}
