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
	"strings"
	"syscall"
	"time"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/store"
	"github.com/eugen252009/tpahub/internal/tpa"
	appversion "github.com/eugen252009/tpahub/internal/version"
)

var idRE = regexp.MustCompile(`^[a-f0-9]{32}$`)

func newID() string { b := make([]byte, 16); _, _ = rand.Read(b); return fmt.Sprintf("%x", b) }

type server struct {
	cfg       config.Config
	db        *store.Store
	runner    tpa.Runner
	ctx       context.Context
	startedAt time.Time
	now       func() time.Time
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
	db, e := store.Open(cfg.DataDir)
	if e != nil {
		return e
	}
	defer db.Close()
	s := &server{cfg: cfg, db: db, runner: tpa.Runner{Bin: cfg.TPABin, GPGHome: cfg.GPGHome, Fingerprint: cfg.GPGFingerprint}, startedAt: time.Now().UTC()}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e = s.runner.Check(ctx); e != nil {
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
	m.HandleFunc("/logout", s.browserLogout)
	m.HandleFunc("/docs", s.web)
	m.HandleFunc("/how-to", s.web)
	m.HandleFunc("/pricing", s.web)
	m.HandleFunc("/", s.web)
	m.HandleFunc("/tpahub.asc", s.publicKey)
	m.HandleFunc("/version", s.version)
	m.HandleFunc("/v1/accounts", s.accounts)
	m.HandleFunc("/v1/login", s.login)
	m.HandleFunc("/v1/logout", s.logout)
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
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	jobID := newID()
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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if e := s.db.CreateJob(r.Context(), store.Job{ID: jobID, RepositoryID: id, State: "queued", IncomingPath: dir, CreatedAt: now}); e != nil {
		_ = os.RemoveAll(dir)
		bad(w, 500, e.Error())
		return
	}
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
		_, pw, ok := r.BasicAuth()
		allowed, err := s.db.ReaderRepository(r.Context(), repo.ID, store.HashToken(pw))
		if !ok || err != nil || !allowed {
			w.Header().Set("WWW-Authenticate", `Basic realm="TPA Hub"`)
			bad(w, 401, "repository credentials required")
			return
		}
	}
	root := filepath.Join(s.cfg.DataDir, "repos", parts[0])
	path := filepath.Join(root, rel)
	info, e := os.Stat(path)
	if e != nil || !info.Mode().IsRegular() {
		bad(w, 404, "not found")
		return
	}
	counted := &repositoryCountingWriter{ResponseWriter: w}
	defer func() { s.recordRepositoryEgress(repo.ID, counted.bytes) }()
	http.ServeFile(counted, r, path)
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
	log.Printf("publish job started id=%s", j.ID)
	work := filepath.Join(s.cfg.DataDir, "work", j.ID)
	candidate := work + "-published"
	defer os.RemoveAll(work)
	defer os.RemoveAll(candidate)
	if e := os.MkdirAll(work, 0700); e != nil {
		s.fail(j, e)
		return
	}
	art := filepath.Join(s.cfg.DataDir, "artifacts", j.RepositoryID)
	if e := copyDebs(art, work, false); e != nil {
		s.fail(j, e)
		return
	}
	if e := copyDebs(j.IncomingPath, work, true); e != nil {
		s.fail(j, e)
		return
	}
	out := filepath.Join(s.cfg.DataDir, "repos", j.RepositoryID)
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.PublishTimeout)
	defer cancel()
	if e := s.runner.Publish(ctx, tpa.PublishRequest{Input: work, Output: candidate}); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.checkStorageCapacity(j.RepositoryID, candidate); e != nil {
		s.fail(j, e)
		return
	}
	if e := s.runner.Publish(ctx, tpa.PublishRequest{Input: work, Output: out}); e != nil {
		s.fail(j, e)
		return
	}
	e := os.RemoveAll(art)
	if e == nil {
		e = os.MkdirAll(filepath.Dir(art), 0755)
		if e == nil {
			e = os.Rename(work, art)
		}
	}
	if e != nil {
		s.fail(j, e)
		return
	}
	_ = s.db.Finish(context.Background(), j.ID, "succeeded", "")
	_ = os.RemoveAll(j.IncomingPath)
	log.Printf("publish job succeeded id=%s", j.ID)
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
	resulting := account.StoredBytes - currentRepo.StoredBytes + prospective.StoredBytes
	if resulting > nanoPlan.StorageBytes && resulting > account.StoredBytes {
		return StorageCapacityError{CurrentBytes: account.StoredBytes, ResultingBytes: resulting, CapacityBytes: nanoPlan.StorageBytes}
	}
	return nil
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
	_ = s.db.Finish(context.Background(), j.ID, "failed", msg)
	log.Printf("publish job failed id=%s error=%s", j.ID, msg)
}
