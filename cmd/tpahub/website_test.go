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
		if path == "/nano" || path == "/pricing" {
			text := string(body)
			wants := []string{"Nano"}
			if path == "/nano" {
				wants = append(wants, "Neovim 0.12.5", "about 2.4 KB", "recipe package, not Neovim itself", "2,500 recipe-sized packages", "200 times", "Your mileage may vary", "STANDARD", "PRO")
			} else {
				wants = append(wants, "Standard", "Pro", "5 MB", "1 GB", "AVAILABLE NOW", "COMING SOON")
			}
			for _, want := range wants {
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
