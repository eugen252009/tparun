package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOperatorOriginPolicyMatrix(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.OperatorOrigin = "https://operator.example:8443"

	cases := []struct {
		name   string
		origin string
		valid  bool
	}{
		{name: "exact", origin: s.cfg.OperatorOrigin, valid: true},
		{name: "foreign-host", origin: "https://evil.example:8443"},
		{name: "foreign-scheme", origin: "http://operator.example:8443"},
		{name: "foreign-port", origin: "https://operator.example:9443"},
		{name: "missing", origin: ""},
		{name: "null", origin: "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/ops/bootstrap", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := s.exactOperatorOrigin(r); got != tc.valid {
				t.Fatalf("exactOperatorOrigin(%q) = %v, want %v", tc.origin, got, tc.valid)
			}
		})
	}
}

func TestOperatorPOSTRoutesRejectNonCanonicalOrigins(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.OperatorOrigin = "https://operator.example:8443"
	session, err := s.newOperatorSession()
	if err != nil {
		t.Fatal(err)
	}

	// Keep this table synchronized with routes(): these are all current
	// state-changing operator POST endpoints. The valid session ensures that
	// /ops/bootstrap reaches its Origin gate instead of redirecting first.
	paths := []string{
		"/ops/bootstrap",
		"/ops/auth/login/begin",
		"/ops/auth/login/finish",
		"/ops/auth/register/begin",
		"/ops/auth/register/finish",
		"/ops/logout",
		"/ops/recover",
	}
	origins := []string{"https://evil.example:8443", "http://operator.example:8443", "https://operator.example:9443", "", "null"}
	for _, path := range paths {
		for _, origin := range origins {
			t.Run(path+"/"+origin, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodPost, path, nil)
				if origin != "" {
					r.Header.Set("Origin", origin)
				}
				if path == "/ops/bootstrap" {
					r.AddCookie(&http.Cookie{Name: operatorSessionCookie, Value: session})
				}
				w := httptest.NewRecorder()
				s.routes().ServeHTTP(w, r)
				if w.Code != http.StatusForbidden {
					t.Fatalf("path=%s origin=%q status=%d, want %d", path, origin, w.Code, http.StatusForbidden)
				}
			})
		}
	}
}
