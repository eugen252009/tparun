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
	if err != nil || credential.AccountID != a.ID || credential.RevokedAt != "" || !credential.Flags.Has(store.CredentialEnabled) {
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
	credential, err := s.db.SSHCredentialByFingerprint(r.Context(), c.Fingerprint)
	if err != nil || credential.AccountID != c.AccountID || credential.RevokedAt != "" || !credential.Flags.Has(store.CredentialEnabled) {
		bad(w, http.StatusUnauthorized, "credential disabled or revoked")
		return
	}
	a, err := s.db.AccountByID(r.Context(), c.AccountID)
	if err != nil || a.Status != store.AccountStatusActive {
		bad(w, http.StatusUnauthorized, "account disabled")
		return
	}
	t, err := s.issueWithCredential(r.Context(), a.ID, credential.ID)
	if err != nil {
		bad(w, http.StatusInternalServerError, "session creation failed")
		return
	}
	jsonOut(w, map[string]any{"account_id": a.ID, "email": a.Email, "token": t})
}

func (s *server) sshCredentialRoute(w http.ResponseWriter, r *http.Request) {
	if !normalAccountSession(r) {
		bad(w, http.StatusForbidden, "normal account session required")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/account/ssh-credentials/")
	if id == "" || strings.Contains(id, "/") {
		bad(w, http.StatusNotFound, "credential not found")
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.db.RevokeSSHCredentialAudited(r.Context(), accountID(r), id, s.currentTime().UTC().Format(time.RFC3339Nano)); err != nil {
			bad(w, http.StatusNotFound, "credential not found")
			return
		}
		jsonOut(w, map[string]string{"result": "revoked"})
		return
	}
	if r.Method != http.MethodPatch {
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var raw map[string]any
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&raw) != nil {
		bad(w, 400, "invalid credential configuration")
		return
	}
	current, err := s.db.SSHCredentialByID(r.Context(), accountID(r), id)
	if err != nil {
		bad(w, 404, "credential not found")
		return
	}
	label := current.Label
	if value, ok := raw["label"]; ok {
		var valid bool
		label, valid = value.(string)
		if !valid {
			bad(w, 400, "label must be string")
			return
		}
	}
	flags := current.Flags
	for name, bit := range map[string]store.CredentialFlags{"enabled": store.CredentialEnabled, "list": store.CredentialList, "read": store.CredentialRead, "write": store.CredentialWrite} {
		if value, ok := raw[name]; ok {
			b, valid := value.(bool)
			if !valid {
				bad(w, 400, name+" must be boolean")
				return
			}
			if b {
				flags = flags.With(bit)
			} else {
				flags = flags.Without(bit)
			}
		}
	}
	action := credentialMutationAction(current.Flags, flags, current.Label != label)
	if flags.Has(store.CredentialEnabled) != current.Flags.Has(store.CredentialEnabled) {
		if flags.Has(store.CredentialEnabled) {
			action = "credential enabled"
		} else {
			action = "credential disabled"
		}
	}
	if err := s.db.UpdateSSHCredentialAudited(r.Context(), accountID(r), id, label, flags, action, s.currentTime().UTC().Format(time.RFC3339Nano)); err != nil {
		bad(w, 404, "credential not found")
		return
	}
	jsonOut(w, map[string]any{"id": id, "label": label, "enabled": flags.Has(store.CredentialEnabled), "capabilities": flags.Names(), "revoked": false})
}

func credentialMutationAction(oldFlags, newFlags store.CredentialFlags, labelChanged bool) string {
	if oldFlags.Has(store.CredentialEnabled) != newFlags.Has(store.CredentialEnabled) {
		if newFlags.Has(store.CredentialEnabled) {
			return "credential enabled"
		}
		return "credential disabled"
	}
	if oldFlags != newFlags {
		if newFlags&^oldFlags != 0 {
			return "capability granted"
		}
		return "capability revoked"
	}
	if labelChanged {
		return "label changed"
	}
	return "credential configuration changed"
}

func (s *server) sshCredentials(w http.ResponseWriter, r *http.Request) {
	if !normalAccountSession(r) {
		bad(w, http.StatusForbidden, "normal account session required")
		return
	}
	if r.Method == http.MethodGet {
		items, err := s.db.ListSSHCredentials(r.Context(), accountID(r))
		if err != nil {
			bad(w, 500, "credentials unavailable")
			return
		}
		out := make([]map[string]any, 0, len(items))
		for _, c := range items {
			out = append(out, map[string]any{"id": c.ID, "type": c.CredentialType, "fingerprint": c.Fingerprint, "label": c.Label, "created_at": c.CreatedAt, "revoked": c.RevokedAt != "", "enabled": c.Flags.Has(store.CredentialEnabled), "capabilities": c.Flags.Names()})
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
	c := store.SSHCredential{ID: newID(), AccountID: accountID(r), CredentialType: "ssh", PublicKey: ssh.MarshalAuthorizedKey(key), Fingerprint: sshFingerprint(key), Label: strings.TrimSpace(in.Label), Flags: 0, CreatedAt: s.currentTime().UTC().Format(time.RFC3339Nano)}
	if err := s.db.CreateSSHCredentialAudited(r.Context(), c); err != nil {
		bad(w, 409, "SSH public key already registered")
		return
	}
	jsonOut(w, map[string]any{"id": c.ID, "type": c.CredentialType, "fingerprint": c.Fingerprint, "label": c.Label, "created_at": c.CreatedAt, "revoked": false, "enabled": false, "capabilities": []string{}})
}
