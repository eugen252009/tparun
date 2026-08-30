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
		if path == "/" || path == "/nano" || path == "/pricing" || path == "/docs" {
			text := string(body)
			wants := []string{"Nano"}
			if path == "/" {
				wants = append(wants, "Same capability. Different capacity.", "same core TPA Hub capabilities", "5 MB", "1 GB", "500 MB", "20 GB", "Pay as you go", "AVAILABLE NOW", "COMING SOON", "actual resources", "package or repository-count limits", "/pricing")
			} else if path == "/nano" {
				wants = append(wants, "Neovim 0.12.5", "about 2.4 KB", "recipe package, not Neovim itself", "2,500 recipe-sized packages", "200 times", "Your mileage may vary", "STANDARD", "PRO", "same core TPA Hub capabilities")
			} else if path == "/pricing" {
				wants = append(wants, "Standard", "Pro", "Same capability. Different capacity.", "same core TPA Hub capabilities", "5 MB", "1 GB", "500 MB", "20 GB", "Pay as you go", "storage blocks", "egress blocks", "AVAILABLE NOW", "COMING SOON")
			} else {
				wants = append(wants, "Same capability. Different capacity.", "not feature-based", "5 MB", "1 GB", "500 MB", "20 GB", "pay as you go", "future", "not implemented")
			}
			for _, want := range wants {
				if !strings.Contains(text, want) {
					t.Fatalf("website page %s missing %q", path, want)
				}
			}
			if path == "/pricing" && (strings.Contains(text, "€") || strings.Contains(text, "$") || strings.Contains(text, "Buy Pro")) {
				t.Fatal("pricing exposes an unimplemented Pro price or purchase action")
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
