package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestSSHCredentialEnrollmentDefaultsDisabledAndAuditsChanges(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if err := s.db.CreateAccount(ctx, store.Account{ID: "managed", Email: "managed@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateSession(ctx, store.Session{ID: "managed-session", AccountID: "managed", TokenPrefix: "managed-token"}, store.HashToken("managed-token")); err != nil {
		t.Fatal(err)
	}
	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	request := func(method, path, body string) *http.Response {
		r, _ := http.NewRequest(method, h.URL+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer managed-token")
		r.Header.Set("Content-Type", "application/json")
		response, _ := http.DefaultClient.Do(r)
		return response
	}
	registered := request(http.MethodPost, "/v1/account/ssh-credentials", `{"public_key":"`+pub+`","label":"desktop"}`)
	if registered.StatusCode != http.StatusOK {
		t.Fatalf("registration status=%d", registered.StatusCode)
	}
	var item struct {
		ID           string
		Enabled      bool
		Capabilities []string
	}
	_ = json.NewDecoder(registered.Body).Decode(&item)
	registered.Body.Close()
	if item.ID == "" || item.Enabled || len(item.Capabilities) != 0 {
		t.Fatalf("new credential state=%+v", item)
	}
	configured := request(http.MethodPatch, "/v1/account/ssh-credentials/"+item.ID, `{"enabled":true,"read":true}`)
	if configured.StatusCode != http.StatusOK {
		t.Fatalf("configuration status=%d", configured.StatusCode)
	}
	configured.Body.Close()
	audits, err := s.db.SSHCredentialAuditCount(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("audit entries=%d", audits)
	}
}
