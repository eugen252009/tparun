package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthLimiterIsBoundedAndExpires(t *testing.T) {
	l := newAuthLimiter(2, 2, time.Minute)
	now := time.Unix(100, 0)
	if !l.allow("one:1", now) || !l.allow("two:2", now) || !l.allow("three:3", now) {
		t.Fatal("new keys should be admitted with bounded eviction")
	}
	if l.size() > 2 {
		t.Fatalf("limiter size=%d", l.size())
	}
	if !l.allow("one:1", now.Add(time.Minute)) || l.size() > 1 {
		t.Fatalf("expired entries were not evicted: size=%d", l.size())
	}
}

func TestCustomerAuthenticationRoutesShareBoundedLimiter(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		body  string
		type_ string
	}{
		{"api registration", "/v1/accounts", `{"email":"limit-api-register@example.invalid","password":"long-enough-password"}`, "application/json"},
		{"api login", "/v1/login", `{"email":"missing@example.invalid","password":"long-enough-password"}`, "application/json"},
		{"web registration", "/register", "email=limit-web-register%40example.invalid&password=long-enough-password&password_confirmation=long-enough-password", "application/x-www-form-urlencoded"},
		{"web login", "/login", "email=missing%40example.invalid&password=long-enough-password", "application/x-www-form-urlencoded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, _ := testServer(t)
			now := time.Unix(100, 0)
			s.now = func() time.Time { return now }
			s.customerAuthRate = newAuthLimiter(4, 1, time.Minute)
			request := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
				r.RemoteAddr = "198.51.100.10:1234"
				r.Header.Set("Content-Type", test.type_)
				if strings.HasPrefix(test.path, "/") && !strings.HasPrefix(test.path, "/v1/") {
					r.Header.Set("Origin", "http://example.com")
					r.Host = "example.com"
				}
				w := httptest.NewRecorder()
				s.routes().ServeHTTP(w, r)
				return w
			}
			_ = request()
			if got := request().Code; got != http.StatusTooManyRequests {
				t.Fatalf("second %s attempt status=%d, want 429", test.name, got)
			}
			now = now.Add(time.Minute)
			if got := request().Code; got == http.StatusTooManyRequests {
				t.Fatalf("expired %s limiter remained blocked", test.name)
			}
		})
	}
}

func TestCustomerAuthenticationRoutesCannotBypassSharedLimiter(t *testing.T) {
	s, _ := testServer(t)
	s.customerAuthRate = newAuthLimiter(4, 2, time.Minute)
	request := func(path, body, contentType string) int {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.RemoteAddr = "198.51.100.20:1234"
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		return w.Code
	}
	if got := request("/v1/login", `{"email":"missing@example.invalid","password":"long-enough-password"}`, "application/json"); got != http.StatusUnauthorized {
		t.Fatalf("first API login status=%d, want 401", got)
	}
	if got := request("/v1/login", `{"email":"missing@example.invalid","password":"long-enough-password"}`, "application/json"); got != http.StatusUnauthorized {
		t.Fatalf("second API login status=%d, want 401", got)
	}
	if got := request("/v1/accounts", `{"email":"blocked@example.invalid","password":"long-enough-password"}`, "application/json"); got != http.StatusTooManyRequests {
		t.Fatalf("API registration bypassed login limiter status=%d", got)
	}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"email": {"missing@example.invalid"}, "password": {"long-enough-password"}}.Encode()))
	r.RemoteAddr = "198.51.100.20:4321"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://example.com")
	r.Host = "example.com"
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("web login bypassed API limiter status=%d", w.Code)
	}
}

func TestOperatorStaticAssetAndHeaders(t *testing.T) {
	s, _ := testServer(t)
	for _, asset := range []string{"ops-auth.js", "ops.js"} {
		r := httptest.NewRequest("GET", "/static/"+asset, nil)
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Fatalf("%s status=%d content-type=%q", asset, w.Code, w.Header().Get("Content-Type"))
		}
		expected, err := websiteFiles.ReadFile("static/" + asset)
		if err != nil {
			t.Fatal(err)
		}
		if string(w.Body.Bytes()) != string(expected) {
			t.Fatalf("served %s differs from embedded asset", asset)
		}
	}
	for _, path := range []string{"/ops", "/ops/login", "/ops/recover"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s missing security headers", path)
		}
	}
}
