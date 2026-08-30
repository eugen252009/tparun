package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWebsitePagesAndProtocolBoundaries(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/", "/docs", "/how-to", "/pricing", "/nano", "/register", "/login"} {
		r, e := http.Get(h.URL + path)
		if e != nil || r.StatusCode != 200 {
			t.Fatalf("%s: %v %d", path, e, r.StatusCode)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if path == "/nano" {
			text := string(body)
			for _, want := range []string{"Neovim 0.12.5", "about 2.4 KB", "recipe package, not Neovim itself", "Mema"} {
				if !strings.Contains(text, want) {
					t.Fatalf("nano page missing %q", want)
				}
			}
		}
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
