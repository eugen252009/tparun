package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func sshSigners(identity string) ([]ssh.Signer, func(), error) {
	if identity != "" {
		b, err := os.ReadFile(identity)
		if err != nil {
			return nil, func() {}, fmt.Errorf("identity file not found: %w", err)
		}
		key, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return nil, func() {}, fmt.Errorf("unsupported or unreadable SSH key: %w", err)
		}
		return []ssh.Signer{key}, func() {}, nil
	}
	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			agentClient := agent.NewClient(conn)
			signers, err := agentClient.Signers()
			if err == nil && len(signers) > 0 {
				return signers, func() { _ = conn.Close() }, nil
			}
			_ = conn.Close()
		}
	}
	home, _ := os.UserHomeDir()
	var result []ssh.Signer
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		b, err := os.ReadFile(filepath.Join(home, ".ssh", name))
		if err != nil {
			continue
		}
		key, err := ssh.ParsePrivateKey(b)
		if err == nil {
			result = append(result, key)
		}
	}
	if len(result) == 0 {
		return nil, func() {}, fmt.Errorf("no SSH identity available (use SSH_AUTH_SOCK or --ssh <identity-file>)")
	}
	return result, func() {}, nil
}

func sshLoginCLI(c cliConfig, identity string, nonInteractive bool) (cliConfig, error) {
	if identity == "" {
		identity = c.IdentityFile
	}
	email := tparunEnv("TPARUN_EMAIL")
	if email == "" {
		email = c.Email
	}
	if email == "" && nonInteractive {
		return c, fmt.Errorf("TPARUN_EMAIL or a configured account email is required for non-interactive SSH login")
	}
	if email == "" {
		email = input("Email: ", false)
	}
	if nonInteractive && identity == "" && os.Getenv("SSH_AUTH_SOCK") == "" {
		return c, fmt.Errorf("explicit SSH identity is required in non-interactive mode")
	}
	signers, closeFn, err := sshSigners(identity)
	if err != nil {
		return c, err
	}
	defer closeFn()
	var lastErr error
	for _, signer := range signers {
		public := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
		var challenge struct {
			ChallengeID string `json:"challenge_id"`
			Nonce       string `json:"nonce"`
			AccountID   string `json:"account_id"`
			Service     string `json:"service"`
		}
		if err := postJSON(c.Server+"/v1/login/ssh/challenge", map[string]string{"email": email, "public_key": public}, &challenge); err != nil {
			lastErr = err
			continue
		}
		payload := sshLoginPayload(sshLoginChallenge{ID: challenge.ChallengeID, Nonce: challenge.Nonce, AccountID: challenge.AccountID, Service: challenge.Service})
		sig, err := signer.Sign(rand.Reader, payload)
		if err != nil {
			lastErr = fmt.Errorf("SSH identity could not sign challenge: %w", err)
			continue
		}
		var out struct{ AccountID, Email, Token string }
		err = postJSON(c.Server+"/v1/login/ssh", map[string]string{"challenge_id": challenge.ChallengeID, "public_key": public, "signature_format": sig.Format, "signature": base64.RawStdEncoding.EncodeToString(sig.Blob)}, &out)
		if err == nil {
			c.Email, c.Token, c.IdentityFile = out.Email, out.Token, identity
			return c, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("public key not registered")
	}
	return c, lastErr
}
