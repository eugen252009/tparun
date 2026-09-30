package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/eugen252009/tpahub/internal/store"
)

const maxRepositoryAPTSigners = 16

type repositorySignerView struct {
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
	CreatedAt   string `json:"created_at"`
}

func (s *server) repositoryAPTSigners(w http.ResponseWriter, r *http.Request, repositoryID, revokeFingerprint string) {
	repository, err := s.db.Repository(r.Context(), repositoryID)
	if err != nil {
		bad(w, http.StatusNotFound, "repository not found")
		return
	}
	if repository.Visibility == "private" && repository.AccountID != accountID(r) {
		bad(w, http.StatusNotFound, "repository not found")
		return
	}
	if revokeFingerprint != "" {
		if r.Method != http.MethodDelete {
			bad(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if _, err = s.repositoryPolicy().AuthorizeMutation(r.Context(), accountID(r), repositoryID); err != nil {
			s.repositoryAuthorizationError(w, err)
			return
		}
		repositoryLock := s.generationLock(repositoryID)
		repositoryLock.Lock()
		defer repositoryLock.Unlock()
		if err = s.db.RevokeRepositoryAPTSigner(r.Context(), repositoryID, revokeFingerprint, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			bad(w, http.StatusNotFound, "trusted signer not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.Method {
	case http.MethodGet:
		signers, signerErr := s.db.RepositoryAPTSigners(r.Context(), repositoryID)
		if signerErr != nil {
			bad(w, http.StatusInternalServerError, "repository signer lookup failed")
			return
		}
		views := make([]repositorySignerView, 0, len(signers))
		for _, signer := range signers {
			views = append(views, repositorySignerView{Fingerprint: signer.Fingerprint, PublicKey: base64.StdEncoding.EncodeToString(signer.PublicKey), CreatedAt: signer.CreatedAt})
		}
		jsonOut(w, views)
	case http.MethodPost:
		if _, err = s.repositoryPolicy().AuthorizeMutation(r.Context(), accountID(r), repositoryID); err != nil {
			s.repositoryAuthorizationError(w, err)
			return
		}
		var input struct {
			PublicKey string `json:"public_key"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&input); err != nil || strings.TrimSpace(input.PublicKey) == "" {
			bad(w, http.StatusBadRequest, "valid public_key is required")
			return
		}
		fingerprint, exported, keyErr := importAPTSignerPublicKey([]byte(input.PublicKey))
		if keyErr != nil {
			bad(w, http.StatusBadRequest, "invalid public OpenPGP key")
			return
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		repositoryLock := s.generationLock(repository.ID)
		repositoryLock.Lock()
		defer repositoryLock.Unlock()
		registered, signerErr := s.db.RepositoryAPTSigners(r.Context(), repository.ID)
		if signerErr != nil {
			bad(w, http.StatusInternalServerError, "repository signer lookup failed")
			return
		}
		if len(registered) >= maxRepositoryAPTSigners {
			bad(w, http.StatusConflict, "repository signer limit reached")
			return
		}
		if err = s.db.AddRepositoryAPTSigner(r.Context(), store.RepositoryAPTSigner{RepositoryID: repository.ID, Fingerprint: fingerprint, PublicKey: exported, CreatedAt: now}); err != nil {
			bad(w, http.StatusConflict, "signer already registered or registration failed")
			return
		}
		jsonOut(w, repositorySignerView{Fingerprint: fingerprint, PublicKey: base64.StdEncoding.EncodeToString(exported), CreatedAt: now})
	default:
		bad(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func importAPTSignerPublicKey(publicKey []byte) (string, []byte, error) {
	trimmedKey := bytes.TrimSpace(publicKey)
	if len(publicKey) == 0 || len(publicKey) > 64<<10 || !bytes.HasPrefix(trimmedKey, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) || !bytes.HasSuffix(trimmedKey, []byte("-----END PGP PUBLIC KEY BLOCK-----")) {
		return "", nil, fmt.Errorf("invalid public key size")
	}
	home, err := os.MkdirTemp("", "tparun-apt-signer-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(home)
	if err = os.Chmod(home, 0o700); err != nil {
		return "", nil, err
	}
	keyPath := filepath.Join(home, "submitted.asc")
	if err = os.WriteFile(keyPath, publicKey, 0o600); err != nil {
		return "", nil, err
	}
	command := func(args ...string) *exec.Cmd {
		cmd := exec.Command("gpg", append([]string{"--batch", "--no-options", "--homedir", home}, args...)...)
		return cmd
	}
	if output, importErr := command("--import", keyPath).CombinedOutput(); importErr != nil {
		return "", nil, fmt.Errorf("import submitted public key: %w: %s", importErr, strings.TrimSpace(string(output)))
	}
	output, err := command("--with-colons", "--fingerprint", "--list-keys").Output()
	if err != nil {
		return "", nil, err
	}
	fingerprint := ""
	primaryKeys := 0
	wantPrimaryFingerprint := false
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "pub" {
			primaryKeys++
			wantPrimaryFingerprint = true
		} else if fields[0] == "sub" {
			wantPrimaryFingerprint = false
		} else if fields[0] == "fpr" && wantPrimaryFingerprint && len(fields) > 9 {
			fingerprint = strings.ToUpper(fields[9])
			break
		}
	}
	if fingerprint == "" || primaryKeys != 1 {
		return "", nil, fmt.Errorf("exactly one public key primary fingerprint is required")
	}
	secretKeys, err := command("--with-colons", "--list-secret-keys").Output()
	if err != nil {
		return "", nil, err
	}
	for _, line := range strings.Split(string(secretKeys), "\n") {
		if strings.HasPrefix(line, "sec:") || strings.HasPrefix(line, "ssb:") {
			return "", nil, fmt.Errorf("private OpenPGP keys are not accepted")
		}
	}
	exported, err := command("--export", fingerprint).Output()
	if err != nil || len(exported) == 0 {
		return "", nil, fmt.Errorf("export normalized public key: %w", err)
	}
	return fingerprint, exported, nil
}

func trustedAPTSigner(ctx context.Context, s *server, repositoryID, fingerprint string) (store.RepositoryAPTSigner, error) {
	signers, err := s.db.RepositoryAPTSigners(ctx, repositoryID)
	if err != nil {
		return store.RepositoryAPTSigner{}, err
	}
	for _, signer := range signers {
		if strings.EqualFold(signer.Fingerprint, fingerprint) {
			return signer, nil
		}
	}
	return store.RepositoryAPTSigner{}, fmt.Errorf("APT signer is not trusted for repository")
}
