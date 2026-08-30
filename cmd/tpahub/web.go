package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
	"golang.org/x/crypto/bcrypt"
)

//go:embed templates/*.html static/site.css
var websiteFiles embed.FS
var websiteTemplates = template.Must(template.ParseFS(websiteFiles, "templates/*.html"))

type pageData struct {
	Title, Email string
	LoggedIn     bool
	Error        string
	CSRF         string
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
	if name != "home" && name != "docs" && name != "pricing" && name != "register" && name != "login" && name != "account" {
		notFoundPage(w)
		return
	}
	id := accountID(r)
	email := ""
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
	_ = websiteTemplates.ExecuteTemplate(w, name+".html", pageData{Title: "TPA Hub", Email: email, LoggedIn: id != ""})
}
func (s *server) static(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/static/site.css" {
		notFoundPage(w)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	b, _ := websiteFiles.ReadFile("static/site.css")
	w.Write(b)
}
func notFoundPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(404)
	w.Write([]byte("<!doctype html><title>Not found — TPA Hub</title><h1>Not found</h1><p><a href=\"/\">Return to TPA Hub</a></p>"))
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
	a := store.Account{ID: newID(), Email: email, PasswordHash: string(h), Status: "active", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if e = s.db.CreateAccount(r.Context(), a); e != nil {
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
	if !sameOrigin(r) {
		bad(w, 403, "invalid request origin")
		return
	}
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
	_ = r.ParseForm()
	a, e := s.db.AccountByEmail(r.Context(), strings.ToLower(strings.TrimSpace(r.FormValue("email"))))
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
