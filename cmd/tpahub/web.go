package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"github.com/eugen252009/tpahub/internal/tpahubproduct"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/commerce"
	"github.com/eugen252009/tpahub/internal/store"
	"golang.org/x/crypto/bcrypt"
)

//go:embed install.sh templates/*.html static/site.css static/ops-auth.js static/ops.js
var websiteFiles embed.FS
var websiteTemplates = template.Must(template.ParseFS(websiteFiles, "templates/*.html"))

type pageData struct {
	Title, Email                   string
	LoggedIn                       bool
	Error                          string
	CSRF                           string
	PackageCount                   int64
	HasPackageCount                bool
	StorageUsed                    string
	StorageBytes                   int64
	HasStorageUsage                bool
	StorageCapacity                string
	EgressUsed                     string
	EgressCapacity                 string
	EgressStatus                   string
	HasEgressUsage                 bool
	EgressMeasured, EgressEnforced bool
	EgressPeriod                   string
	EgressRequests                 uint64
	ShowBillingPreviewLink         bool
	BillingMode                    string
	BillingPeriod                  string
	StorageHighWater               string
	HasBillingPreview              bool
	ShowSandboxPurchase            bool
	SandboxPurchaseStatus          string
}

func (s *server) browserSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accountID(r) == "" {
			if c, e := r.Cookie("tpahub_session"); e == nil {
				if id, e := s.db.AccountByToken(r.Context(), store.HashToken(c.Value)); e == nil {
					r = r.WithContext(context.WithValue(r.Context(), accountKey, id))
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (s *server) web(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		bad(w, 405, "method not allowed")
		return
	}
	name := strings.Trim(r.URL.Path, "/")
	if name == "" {
		name = "home"
	}
	if name == "how-to" {
		name = "docs"
	}
	if name != "home" && name != "docs" && name != "pricing" && name != "nano" && name != "register" && name != "login" && name != "account" {
		notFoundPage(w)
		return
	}
	id := accountID(r)
	email := ""
	var packageCount int64
	var countErr error
	if name == "home" {
		stats, err := s.globalPublishedUsage()
		packageCount, countErr = stats.PackageCount, err
		if countErr != nil {
			log.Printf("package count: %v", countErr)
		}
	}
	var accountUsage AccountUsage
	var usageErr error
	var storageLimit int64
	var egress deliveryUsageView
	var egressErr, storageGrantErr error
	var billingSnapshot BillingUsageSnapshot
	var billingPreview BillingPreview
	var billingErr error
	billingRequested := r.URL.Query().Get("billing") == "preview" && s.billingEnabled()
	var billingMode BillingMode
	showSandboxPurchase := false
	if name == "account" && id != "" {
		showSandboxPurchase = s.nanoPurchaseAvailable()

		accountUsage, usageErr = s.accountUsage(r.Context(), id)
		if usageErr != nil {
			log.Printf("account storage usage: %v", usageErr)
		}
		storageGrant, grantErr := s.grantService().GetGrant(r.Context(), commerce.CustomerID(id), tpahubproduct.ProductID, CapabilityRepositoryPublish, s.currentTime())
		storageGrantErr = grantErr
		if grantErr == nil {
			storageLimit = storageGrant.Limits.StorageBytes
		}
		egress, egressErr = s.readDeliveryUsage(id, s.currentTime())
		if egressErr != nil {
			log.Printf("account egress usage: %v", egressErr)
		}
		if billingRequested {
			billingSnapshot, billingErr = s.billingSnapshot(r.Context(), id, s.currentTime())
			if billingErr == nil {
				if plan, ok := planCatalog[billingSnapshot.PlanCode]; ok {
					billingMode = plan.BillingMode
				}
				billingPreview, billingErr = s.billingProvider().Preview(r.Context(), billingSnapshot)
			}
			if billingErr != nil {
				log.Printf("billing preview: %v", billingErr)
			}
		}
	}
	if id != "" {
		if a, e := s.accountByID(r, id); e == nil {
			email = a.Email
		}
	}
	if name == "account" && id == "" {
		http.Redirect(w, r, "/login", 303)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = websiteTemplates.ExecuteTemplate(w, name+".html", pageData{Title: "TPA Hub", Email: email, LoggedIn: id != "", PackageCount: packageCount, HasPackageCount: countErr == nil, StorageUsed: formatBytes(accountUsage.StoredBytes), StorageBytes: accountUsage.StoredBytes, HasStorageUsage: usageErr == nil && storageGrantErr == nil, StorageCapacity: formatBytes(storageLimit), EgressUsed: formatBytes(egress.Bytes), EgressCapacity: formatBytes(egress.Limit), EgressStatus: egress.Status, HasEgressUsage: egressErr == nil, EgressMeasured: egress.Measured, EgressEnforced: egress.Enforced, EgressPeriod: egress.Period, EgressRequests: egress.Requests, ShowBillingPreviewLink: s.billingEnabled(), BillingMode: string(billingMode), BillingPeriod: string(billingSnapshot.Period), StorageHighWater: formatBytes(billingPreview.StorageHighWaterBytes), HasBillingPreview: billingRequested && billingErr == nil, ShowSandboxPurchase: showSandboxPurchase})
}
func (s *server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		bad(w, 405, "method not allowed")
		return
	}
	asset := strings.TrimPrefix(r.URL.Path, "/")
	switch asset {
	case "static/site.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case "static/ops-auth.js", "static/ops.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	default:
		notFoundPage(w)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	b, _ := websiteFiles.ReadFile(asset)
	w.Write(b)
}
func notFoundPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(404)
	_ = websiteTemplates.ExecuteTemplate(w, "not-found.html", nil)
}
func (s *server) webAuth(w http.ResponseWriter, r *http.Request) bool {
	if !sameOrigin(r) {
		bad(w, 403, "invalid request origin")
		return false
	}
	id := accountID(r)
	if id == "" {
		http.Redirect(w, r, "/login", 303)
		return false
	}
	return true
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return strings.HasSuffix(origin, "//"+r.Host) || origin == r.Host
}
func webToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "tph_s_" + hex.EncodeToString(b)
}
func (s *server) createAccount(r *http.Request, email, password string) (store.Account, error) {
	if !emailRE.MatchString(email) || len(password) < 10 {
		return store.Account{}, fmt.Errorf("invalid account")
	}
	h, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return store.Account{}, e
	}
	a := store.Account{ID: newID(), Email: email, PasswordHash: string(h), Status: store.AccountStatusActive, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	grants, e := s.initialTPAGrants(a.CreatedAt)
	if e != nil {
		return store.Account{}, e
	}
	if e = s.db.CreateAccount(r.Context(), a, grants...); e != nil {
		return store.Account{}, e
	}
	return a, nil
}
func (s *server) accountByID(r *http.Request, id string) (store.Account, error) {
	return s.db.AccountByID(r.Context(), id)
}
func (s *server) registerPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		s.web(w, r)
		return
	}
	if !s.cfg.RegistrationEnabled {
		s.webError(w, r, "Registration is currently unavailable.")
		return
	}
	if !s.customerAuthAllowed(r) {
		http.Error(w, "too many authentication attempts", http.StatusTooManyRequests)
		return
	}
	if !sameOrigin(r) {
		bad(w, 403, "invalid request origin")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if e := r.ParseForm(); e != nil {
		s.webError(w, r, "Invalid form.")
		return
	}
	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	pw := r.FormValue("password")
	if pw != r.FormValue("password_confirmation") || len(pw) < 10 {
		s.webError(w, r, "Use the same password twice; passwords must be at least 10 characters.")
		return
	}
	a, e := s.createAccount(r, email, pw)
	if e != nil {
		s.webError(w, r, "That email address is already registered or invalid.")
		return
	}
	tok, e := s.issue(r.Context(), a.ID)
	if e != nil {
		bad(w, 500, "session creation failed")
		return
	}
	setBrowserCookie(w, tok)
	http.Redirect(w, r, "/account", 303)
}
func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		s.web(w, r)
		return
	}
	if !sameOrigin(r) {
		bad(w, 403, "invalid request origin")
		return
	}
	if !s.customerAuthAllowed(r) {
		http.Error(w, "too many authentication attempts", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	_ = r.ParseForm()
	a, e := s.db.ActiveAccountByEmail(r.Context(), strings.ToLower(strings.TrimSpace(r.FormValue("email"))))
	if e != nil || bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(r.FormValue("password"))) != nil {
		s.webError(w, r, "Invalid email or password.")
		return
	}
	tok, e := s.issue(r.Context(), a.ID)
	if e != nil {
		bad(w, 500, "session creation failed")
		return
	}
	setBrowserCookie(w, tok)
	http.Redirect(w, r, "/account", 303)
}
func (s *server) accountPage(w http.ResponseWriter, r *http.Request) {
	if accountID(r) == "" {
		http.Redirect(w, r, "/login", 303)
		return
	}
	s.web(w, r)
}
func (s *server) browserLogout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("tpahub_session"); e == nil {
		_ = s.db.RevokeSession(r.Context(), store.HashToken(c.Value))
	}
	clearBrowserCookie(w)
	http.Redirect(w, r, "/", 303)
}
func (s *server) webError(w http.ResponseWriter, r *http.Request, msg string) {
	name := strings.Trim(r.URL.Path, "/")
	if name == "" {
		name = "home"
	}
	if name == "how-to" {
		name = "docs"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = websiteTemplates.ExecuteTemplate(w, name+".html", pageData{Title: "TPA Hub", Error: msg})
}
func setBrowserCookie(w http.ResponseWriter, t string) {
	http.SetCookie(w, &http.Cookie{Name: "tpahub_session", Value: t, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600})
}
func clearBrowserCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "tpahub_session", Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}
