package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
	"golang.org/x/crypto/ssh"
)

func sshTestCredential(t *testing.T, s *server, account string) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	key := ssh.MarshalAuthorizedKey(signer.PublicKey())
	if err := s.db.CreateSSHCredential(context.Background(), store.SSHCredential{ID: "ssh-" + account, AccountID: account, CredentialType: "ssh", PublicKey: key, Fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), Flags: store.CredentialEnabled | store.CredentialRead | store.CredentialWrite, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestSSHLoginChallengeIsSingleUseAndBindsSignature(t *testing.T) {
	s, h := testServer(t)
	if err := s.db.CreateAccount(context.Background(), store.Account{ID: "ssh-account", Email: "ssh@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	signer := sshTestCredential(t, s, "ssh-account")
	public := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	t.Logf("public=%q", public)
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(public))); err != nil {
		t.Fatalf("local parse: %v", err)
	}
	body := `{"email":"ssh@example.invalid","public_key":"` + strings.TrimSpace(public) + `"}`
	response, err := http.Post(h.URL+"/v1/login/ssh/challenge", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var challenge struct {
		ChallengeID string `json:"challenge_id"`
		Nonce       string `json:"nonce"`
		AccountID   string `json:"account_id"`
		Service     string `json:"service"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&challenge) != nil {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("challenge status=%d body=%s", response.StatusCode, raw)
	}
	payload := sshLoginPayload(sshLoginChallenge{ID: challenge.ChallengeID, Nonce: challenge.Nonce, AccountID: challenge.AccountID, Service: challenge.Service})
	sig, err := signer.Sign(rand.Reader, payload)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(signature string) *http.Response {
		b, _ := json.Marshal(map[string]string{"challenge_id": challenge.ChallengeID, "public_key": public, "signature_format": sig.Format, "signature": signature})
		r, _ := http.NewRequest(http.MethodPost, h.URL+"/v1/login/ssh", strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
		res, _ := http.DefaultClient.Do(r)
		return res
	}
	login := verify(base64.RawStdEncoding.EncodeToString(sig.Blob))
	if login.StatusCode != http.StatusOK {
		t.Fatalf("SSH login status=%d", login.StatusCode)
	}
	var loggedIn struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(login.Body).Decode(&loggedIn); err != nil || loggedIn.Token == "" {
		t.Fatalf("SSH login did not create normal session: %v", err)
	}
	login.Body.Close()
	opsRequest, _ := http.NewRequest(http.MethodGet, h.URL+"/ops/bootstrap", nil)
	opsRequest.AddCookie(&http.Cookie{Name: "tpahub_session", Value: loggedIn.Token})
	opsResponse, _ := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(opsRequest)
	if opsResponse.StatusCode != http.StatusSeeOther || opsResponse.Header.Get("Location") != "/ops/login" {
		t.Fatalf("SSH account reached operator route: status=%d location=%q", opsResponse.StatusCode, opsResponse.Header.Get("Location"))
	}
	opsResponse.Body.Close()
	replay := verify(base64.RawStdEncoding.EncodeToString(sig.Blob))
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed challenge status=%d", replay.StatusCode)
	}
	replay.Body.Close()
}

func TestSSHLoginRejectsUnregisteredAndExpiredKeys(t *testing.T) {
	s, h := testServer(t)
	if err := s.db.CreateAccount(context.Background(), store.Account{ID: "ssh-expired", Email: "expired@example.invalid", PasswordHash: "x", Status: store.AccountStatusActive, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	signer := sshTestCredential(t, s, "ssh-expired")
	public := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	_, unknownPrivate, _ := ed25519.GenerateKey(rand.Reader)
	unknown, _ := ssh.NewSignerFromKey(unknownPrivate)
	unknownBody := `{"email":"expired@example.invalid","public_key":"` + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(unknown.PublicKey()))) + `"}`
	unknownResponse, _ := http.Post(h.URL+"/v1/login/ssh/challenge", "application/json", strings.NewReader(unknownBody))
	if unknownResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unregistered key status=%d", unknownResponse.StatusCode)
	}
	unknownResponse.Body.Close()
	challengeResponse, _ := http.Post(h.URL+"/v1/login/ssh/challenge", "application/json", strings.NewReader(`{"email":"expired@example.invalid","public_key":"`+strings.TrimSpace(public)+`"}`))
	var challenge struct {
		ChallengeID string `json:"challenge_id"`
		Nonce       string `json:"nonce"`
		AccountID   string `json:"account_id"`
		Service     string `json:"service"`
	}
	_ = json.NewDecoder(challengeResponse.Body).Decode(&challenge)
	challengeResponse.Body.Close()
	s.now = func() time.Time { return time.Now().Add(sshChallengeTTL + time.Second) }
	payload := sshLoginPayload(sshLoginChallenge{ID: challenge.ChallengeID, Nonce: challenge.Nonce, AccountID: challenge.AccountID, Service: challenge.Service})
	sig, _ := signer.Sign(rand.Reader, payload)
	b, _ := json.Marshal(map[string]string{"challenge_id": challenge.ChallengeID, "public_key": public, "signature_format": sig.Format, "signature": base64.RawStdEncoding.EncodeToString(sig.Blob)})
	r, _ := http.NewRequest(http.MethodPost, h.URL+"/v1/login/ssh", strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	response, _ := http.DefaultClient.Do(r)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired challenge status=%d", response.StatusCode)
	}
	response.Body.Close()
}
