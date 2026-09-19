package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
	"golang.org/x/crypto/ssh"
)

const sshChallengeTTL = 2 * time.Minute

type sshLoginChallenge struct {
	ID, Nonce, AccountID, Fingerprint, Service string
	Expires                                    time.Time
}

func sshFingerprint(key ssh.PublicKey) string { return ssh.FingerprintSHA256(key) }

func sshLoginPayload(c sshLoginChallenge) []byte {
	return []byte(fmt.Sprintf("TPAHUB-ACCOUNT-LOGIN\nprotocol=1\npurpose=tpahub-account-login\nchallenge_id=%s\nchallenge_nonce=%s\naccount_id=%s\nservice=%s\n", c.ID, c.Nonce, c.AccountID, c.Service))
}

func (s *server) sshChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.customerAuthAllowed(r) {
		bad(w, http.StatusTooManyRequests, "too many authentication attempts")
		return
	}
	var in struct {
		Email     string `json:"email"`
		PublicKey string `json:"public_key"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in) != nil {
		bad(w, http.StatusBadRequest, "invalid SSH challenge request")
		return
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(in.PublicKey))
	if err != nil {
		bad(w, http.StatusBadRequest, "invalid SSH public key")
		return
	}
	a, err := s.db.ActiveAccountByEmail(r.Context(), strings.ToLower(strings.TrimSpace(in.Email)))
	if err != nil {
		bad(w, http.StatusUnauthorized, "public key not registered")
		return
	}
	credential, err := s.db.SSHCredentialByFingerprint(r.Context(), sshFingerprint(key))
	if err != nil || credential.AccountID != a.ID || credential.RevokedAt != "" {
		bad(w, http.StatusUnauthorized, "public key not registered")
		return
	}
	id, err := randomToken("tph_ch_")
	if err != nil {
		bad(w, http.StatusInternalServerError, "challenge unavailable")
		return
	}
	nonce, err := randomToken("nonce_")
	if err != nil {
		bad(w, http.StatusInternalServerError, "challenge unavailable")
		return
	}
	c := sshLoginChallenge{ID: id, Nonce: nonce, AccountID: a.ID, Fingerprint: credential.Fingerprint, Service: r.Host, Expires: s.currentTime().Add(sshChallengeTTL)}
	s.sshChallengeMu.Lock()
	if s.sshChallenges == nil {
		s.sshChallenges = make(map[string]sshLoginChallenge)
	}
	for old, value := range s.sshChallenges {
		if !s.currentTime().Before(value.Expires) {
			delete(s.sshChallenges, old)
		}
	}
	s.sshChallenges[id] = c
	s.sshChallengeMu.Unlock()
	jsonOut(w, map[string]string{"challenge_id": c.ID, "nonce": c.Nonce, "account_id": c.AccountID, "service": c.Service, "expires_at": c.Expires.Format(time.RFC3339Nano)})
}

func (s *server) sshLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in struct {
		ChallengeID     string `json:"challenge_id"`
		PublicKey       string `json:"public_key"`
		SignatureFormat string `json:"signature_format"`
		Signature       string `json:"signature"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in) != nil {
		bad(w, http.StatusUnauthorized, "invalid SSH login request")
		return
	}
	s.sshChallengeMu.Lock()
	c, ok := s.sshChallenges[in.ChallengeID]
	if ok {
		delete(s.sshChallenges, in.ChallengeID)
	}
	s.sshChallengeMu.Unlock()
	if !ok {
		bad(w, http.StatusUnauthorized, "expired or already-used challenge")
		return
	}
	if !s.currentTime().Before(c.Expires) {
		bad(w, http.StatusUnauthorized, "expired challenge")
		return
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(in.PublicKey))
	if err != nil || sshFingerprint(key) != c.Fingerprint {
		bad(w, http.StatusUnauthorized, "public key not registered")
		return
	}
	sigBlob, err := base64.RawStdEncoding.DecodeString(in.Signature)
	if err != nil {
		bad(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	if verifyErr := key.Verify(sshLoginPayload(c), &ssh.Signature{Format: in.SignatureFormat, Blob: sigBlob}); verifyErr != nil {
		bad(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	a, err := s.db.AccountByID(r.Context(), c.AccountID)
	if err != nil || a.Status != store.AccountStatusActive {
		bad(w, http.StatusUnauthorized, "account disabled")
		return
	}
	t, err := s.issue(r.Context(), a.ID)
	if err != nil {
		bad(w, http.StatusInternalServerError, "session creation failed")
		return
	}
	jsonOut(w, map[string]any{"account_id": a.ID, "email": a.Email, "token": t})
}

func (s *server) sshCredentialRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/account/ssh-credentials/")
	if id == "" || strings.Contains(id, "/") {
		bad(w, http.StatusNotFound, "credential not found")
		return
	}
	if err := s.db.RevokeSSHCredential(r.Context(), accountID(r), id, s.currentTime().UTC().Format(time.RFC3339Nano)); err != nil {
		bad(w, http.StatusNotFound, "credential not found")
		return
	}
	jsonOut(w, map[string]string{"result": "revoked"})
}

func (s *server) sshCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		items, err := s.db.ListSSHCredentials(r.Context(), accountID(r))
		if err != nil {
			bad(w, 500, "credentials unavailable")
			return
		}
		out := make([]map[string]any, 0, len(items))
		for _, c := range items {
			out = append(out, map[string]any{"id": c.ID, "type": c.CredentialType, "fingerprint": c.Fingerprint, "label": c.Label, "created_at": c.CreatedAt, "revoked": c.RevokedAt != ""})
		}
		jsonOut(w, out)
		return
	}
	if r.Method != http.MethodPost {
		bad(w, 405, "method not allowed")
		return
	}
	var in struct {
		PublicKey string `json:"public_key"`
		Label     string `json:"label"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in) != nil {
		bad(w, 400, "invalid SSH credential")
		return
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(in.PublicKey))
	if err != nil {
		bad(w, 400, "invalid SSH public key")
		return
	}
	c := store.SSHCredential{ID: newID(), AccountID: accountID(r), CredentialType: "ssh", PublicKey: ssh.MarshalAuthorizedKey(key), Fingerprint: sshFingerprint(key), Label: strings.TrimSpace(in.Label), CreatedAt: s.currentTime().UTC().Format(time.RFC3339Nano)}
	if err := s.db.CreateSSHCredential(r.Context(), c); err != nil {
		bad(w, 409, "SSH public key already registered")
		return
	}
	jsonOut(w, map[string]any{"id": c.ID, "type": c.CredentialType, "fingerprint": c.Fingerprint, "label": c.Label, "created_at": c.CreatedAt, "revoked": false})
}
