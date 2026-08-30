package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestWebsitePagesAndProtocolBoundaries(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/", "/docs", "/how-to", "/pricing", "/register", "/login"} {
		r, e := http.Get(h.URL + path)
		if e != nil || r.StatusCode != 200 {
			t.Fatalf("%s: %v %d", path, e, r.StatusCode)
		}
		r.Body.Close()
	}
	r, e := http.Get(h.URL + "/account")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 200 && r.StatusCode != 303 {
		t.Fatalf("account status %d", r.StatusCode)
	}
	r, e = http.Get(h.URL + "/r/invalid/InRelease")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode == 200 || strings.Contains(r.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("repository boundary status=%d type=%s", r.StatusCode, r.Header.Get("Content-Type"))
	}
}
