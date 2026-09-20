package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestSSHExplicitIdentityFileTakesPrecedence(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "work_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(key), 0600); err != nil {
		t.Fatal(err)
	}
	signers, closeFn, err := sshSigners(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if len(signers) != 1 || ssh.FingerprintSHA256(signers[0].PublicKey()) != ssh.FingerprintSHA256(mustPublicSigner(t, private).PublicKey()) {
		t.Fatal("explicit identity was not selected")
	}
}

func mustPublicSigner(t *testing.T, key ed25519.PrivateKey) ssh.Signer {
	t.Helper()
	s, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
