package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestSSHCredentialCapabilitiesRestrictSessionWithoutChangingAccountPolicy(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	account := "capability-account"
	if err := s.db.CreateAccount(ctx, store.Account{ID: account, Email: "capability@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	repoID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.db.CreateRepository(ctx, store.Repository{ID: repoID, AccountID: account, Name: "owned", Visibility: "public", CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateSSHCredential(ctx, store.SSHCredential{ID: "capability-key", AccountID: account, CredentialType: "ssh", PublicKey: []byte("key"), Fingerprint: "fingerprint-capability", Flags: store.CredentialEnabled | store.CredentialWrite, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateSession(ctx, store.Session{ID: "capability-session", AccountID: account, CredentialID: "capability-key", TokenPrefix: "capability-token"}, store.HashToken("capability-token")); err != nil {
		t.Fatal(err)
	}
	request := func() *http.Response {
		r, _ := http.NewRequest(http.MethodGet, h.URL+"/v1/repositories/"+repoID, nil)
		r.Header.Set("Authorization", "Bearer capability-token")
		response, _ := http.DefaultClient.Do(r)
		return response
	}
	management, _ := http.NewRequest(http.MethodGet, h.URL+"/v1/account/ssh-credentials", nil)
	management.Header.Set("Authorization", "Bearer capability-token")
	managementResponse, _ := http.DefaultClient.Do(management)
	if managementResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("SSH session managed credentials status=%d", managementResponse.StatusCode)
	}
	managementResponse.Body.Close()
	response := request()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("read without read capability status=%d", response.StatusCode)
	}
	response.Body.Close()
	if err := s.db.UpdateSSHCredential(ctx, account, "capability-key", "", store.CredentialEnabled|store.CredentialRead|store.CredentialWrite); err != nil {
		t.Fatal(err)
	}
	response = request()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read with capability status=%d", response.StatusCode)
	}
	response.Body.Close()
	if err := s.db.UpdateSSHCredential(ctx, account, "capability-key", "", store.CredentialRead|store.CredentialWrite); err != nil {
		t.Fatal(err)
	}
	response = request()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled credential session status=%d", response.StatusCode)
	}
	response.Body.Close()
	if err := s.db.UpdateSSHCredential(ctx, account, "capability-key", "", store.CredentialEnabled|store.CredentialRead); err != nil {
		t.Fatal(err)
	}
	response = request()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("re-enabled credential read status=%d", response.StatusCode)
	}
	response.Body.Close()
	if err := s.db.RevokeSSHCredential(ctx, account, "capability-key", "revoked"); err != nil {
		t.Fatal(err)
	}
	response = request()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked credential session status=%d", response.StatusCode)
	}
	response.Body.Close()
	if err := s.db.CreateSession(ctx, store.Session{ID: "missing-credential-session", AccountID: account, CredentialID: "missing-credential", TokenPrefix: "missing-token"}, store.HashToken("missing-token")); err != nil {
		t.Fatal(err)
	}
	missing, _ := http.NewRequest(http.MethodGet, h.URL+"/v1/repositories/"+repoID, nil)
	missing.Header.Set("Authorization", "Bearer missing-token")
	missingResponse, _ := http.DefaultClient.Do(missing)
	if missingResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing credential session status=%d", missingResponse.StatusCode)
	}
	missingResponse.Body.Close()
}
