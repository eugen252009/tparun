package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestRepositoryAPTSignerRegistrationAndRevocation(t *testing.T) {
	s, server := testServer(t)
	repositoryID := strings.Repeat("e", 32)
	if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "apt-signer", Visibility: "private", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	signing := testCandidateSigningConfig(t)
	key, err := exec.Command("gpg", "--batch", "--armor", "--homedir", signing.GPGHome, "--export", signing.GPGFingerprint).Output()
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := exec.Command("gpg", "--batch", "--armor", "--homedir", signing.GPGHome, "--export-secret-keys", signing.GPGFingerprint).Output()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = importAPTSignerPublicKey(privateKey); err == nil {
		t.Fatal("private OpenPGP key material was accepted for signer registration")
	}
	payload, _ := json.Marshal(map[string]string{"public_key": string(key)})
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/repositories/"+repositoryID+"/apt-signers", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer tph_s_test")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var registered repositorySignerView
	if err = json.NewDecoder(response.Body).Decode(&registered); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || registered.Fingerprint != signing.GPGFingerprint {
		t.Fatalf("register status=%d response=%+v", response.StatusCode, registered)
	}
	response, err = http.Get(server.URL + "/v1/repositories/" + repositoryID + "/apt-signers")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("anonymous private signer listing status=%d", response.StatusCode)
	}
	request, err = http.NewRequest(http.MethodGet, server.URL+"/v1/repositories/"+repositoryID+"/apt-signers", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer tph_s_test")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("owner signer listing status=%d", response.StatusCode)
	}
	request, err = http.NewRequest(http.MethodDelete, server.URL+"/v1/repositories/"+repositoryID+"/apt-signers/"+registered.Fingerprint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer tph_s_test")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status=%d", response.StatusCode)
	}
	trusted, err := s.db.RepositoryAPTSigners(context.Background(), repositoryID)
	if err != nil || len(trusted) != 0 {
		t.Fatalf("revoked signer remains trusted: %d err=%v", len(trusted), err)
	}
}
