package main

import (
	"net/http"
	"testing"
)

func TestInstallerEndpoint(t *testing.T) {
	_, h := testServer(t)
	r, e := http.Get(h.URL + "/install.sh")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("Content-Type") == "" {
		t.Fatalf("status=%d content-type=%q", r.StatusCode, r.Header.Get("Content-Type"))
	}
}
