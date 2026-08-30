package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPrivacyNoticeAndFooterLinks(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/", "/docs", "/pricing", "/nano", "/register", "/login"} {
		response, err := http.Get(h.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `href="/datenschutz"`) {
			t.Fatalf("%s does not link to the privacy notice", path)
		}
	}

	response, err := http.Get(h.URL + "/datenschutz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	text := string(body)
	for _, want := range []string{"Datenschutzerklärung", "Eugen Lupricht", "ZAP-Hosting", "systemd-journald", "Stripe", "tpahub_session", "Öffentliche und private Repositories"} {
		if !strings.Contains(text, want) {
			t.Fatalf("privacy notice missing %q", want)
		}
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("privacy notice status=%d", response.StatusCode)
	}
}
