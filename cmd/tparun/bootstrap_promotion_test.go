package main

import (
	"net/http"
	"testing"
)

func TestBootstrapAliasUnavailableWithoutPromotion(t *testing.T) {
	_, h := testServer(t)
	response, err := http.Get(h.URL + "/tparun-bootstrap.deb")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 without a promoted bootstrap", response.StatusCode)
	}
}
