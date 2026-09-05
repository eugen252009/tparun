package main

import (
	"net/http/httptest"
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

func TestOperatorStaticAssetAndHeaders(t *testing.T) {
	s, _ := testServer(t)
	r := httptest.NewRequest("GET", "/static/ops-auth.js", nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("asset status=%d content-type=%q", w.Code, w.Header().Get("Content-Type"))
	}
	expected, err := websiteFiles.ReadFile("static/ops-auth.js")
	if err != nil {
		t.Fatal(err)
	}
	if string(w.Body.Bytes()) != string(expected) {
		t.Fatal("served auth asset differs from embedded asset")
	}
	for _, path := range []string{"/ops", "/ops/login", "/ops/recover"} {
		r = httptest.NewRequest("GET", path, nil)
		w = httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s missing security headers", path)
		}
	}
}
