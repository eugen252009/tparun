package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCentralLegalNavigation(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/", "/docs", "/pricing", "/nano", "/register", "/login"} {
		response, err := http.Get(h.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		text := string(body)
		if response.StatusCode != http.StatusOK || !strings.Contains(text, `href="https://lupricht.net/legal/"`) || !strings.Contains(text, `href="https://lupricht.net/privacy/"`) {
			t.Fatalf("%s does not link to the central legal pages", path)
		}
	}

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(h.URL + "/datenschutz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusPermanentRedirect || response.Header.Get("Location") != "https://lupricht.net/privacy/" {
		t.Fatalf("legacy privacy redirect status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
}
