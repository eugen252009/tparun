package tpa

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSSHArchiveManifestRejectsMutationAndUnsafeEntries(t *testing.T) {
	root := t.TempDir()
	data := []byte("fixture deb")
	if err := os.WriteFile(filepath.Join(root, "x.deb"), data, 0644); err != nil {
		t.Fatal(err)
	}
	payload, files, err := archiveInputs(root)
	if err != nil {
		t.Fatal(err)
	}
	unpacked := filepath.Join(t.TempDir(), "in")
	if err = os.Mkdir(unpacked, 0700); err != nil {
		t.Fatal(err)
	}
	if err = unpackArchive(payload, files, unpacked, 1<<20, 100, true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(unpacked, "x.deb"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("roundtrip=%q err=%v", got, err)
	}
	bad := append([]protocolFile(nil), files...)
	bad[0].SHA256 = hex.EncodeToString(make([]byte, 32))
	if err = unpackArchive(payload, bad, filepath.Join(t.TempDir(), "bad"), 1<<20, 100, true); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	var tarbuf bytes.Buffer
	tw := tar.NewWriter(&tarbuf)
	if err = tw.WriteHeader(&tar.Header{Name: "../escape.deb", Mode: 0600, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	outside := filepath.Join(t.TempDir(), "escape.deb")
	sum := sha256.Sum256([]byte("x"))
	manifest := []protocolFile{{Name: "../escape.deb", Size: 1, SHA256: hex.EncodeToString(sum[:]), Mode: 0600}}
	if err = unpackArchive(tarbuf.Bytes(), manifest, t.TempDir(), 1<<20, 100, true); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err = os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote outside: %v", err)
	}
}

func TestSSHRunnerRejectsUnpinnedAndMismatchedAttempt(t *testing.T) {
	ctx := context.Background()
	if err := (SSHRunner{Config: SSHConfig{Target: "worker", IdentityFile: "id"}}).Check(ctx); err == nil {
		t.Fatal("missing pinned host key accepted")
	}
	root := t.TempDir()
	key := filepath.Join(root, "key")
	known := filepath.Join(root, "known_hosts")
	for _, p := range []string{key, known} {
		if err := os.WriteFile(p, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := (SSHRunner{Config: SSHConfig{Target: "worker", IdentityFile: key, KnownHostsFile: known, SSHBinary: "ssh"}}).Check(ctx); err != nil {
		t.Fatal(err)
	}
	request := RemoteRequest{PublishRequest: PublishRequest{Input: root, Output: filepath.Join(root, "candidate")}, JobID: "0123456789abcdef0123456789abcdef", AttemptID: "11111111111111111111111111111111", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), MaxBytes: 1 << 20, MaxInodes: 100}
	var response bytes.Buffer
	files := []protocolFile{{Name: "dists/stable/Release", Size: 3, SHA256: sha256Hex([]byte("bad")), Mode: 0644}}
	payload, _, err := makeFixtureArchive(t, "Release", "bad", 0644)
	if err != nil {
		t.Fatal(err)
	}
	h := protocolHeader{Version: sshProtocolVersion, Kind: "result", JobID: request.JobID, AttemptID: "22222222222222222222222222222222", Backend: "ssh", StartedAt: request.StartedAt, MaxBytes: request.MaxBytes, MaxInodes: request.MaxInodes, PayloadSize: int64(len(payload)), PayloadHash: sha256Hex(payload), Files: files}
	if err = writeFrame(&response, h, payload); err != nil {
		t.Fatal(err)
	}
	if err = readCandidate(bytes.NewReader(response.Bytes()), request); err == nil {
		t.Fatal("stale remote attempt accepted")
	}
}

func TestSSHTransportRunnerProtocolRoundTrip(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "fixture.deb"), []byte("deb bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(root, "identity")
	known := filepath.Join(root, "known_hosts")
	for _, p := range []string{identity, known} {
		if err := os.WriteFile(p, []byte("test-only"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TPARUN_PRIVATE_TEST_SENTINEL", "do-not-forward")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "ssh-helper")
	script := "#!/bin/sh\nTPARUN_SSH_PROTOCOL_TEST_HELPER=1 exec \"" + self + "\" -test.run='^TestSSHProtocolHelperProcess$'\n"
	if err = os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "candidate")
	runner := SSHRunner{Config: SSHConfig{Target: "worker", IdentityFile: identity, KnownHostsFile: known, SSHBinary: helper}}
	req := RemoteRequest{PublishRequest: PublishRequest{Input: input, Output: output}, JobID: "0123456789abcdef0123456789abcdef", AttemptID: "11111111111111111111111111111111", StartedAt: "2026-01-01T00:00:00Z", MaxBytes: 1 << 20, MaxInodes: 100}
	if err = runner.PublishRemote(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(output, "dists", "stable", "Release"))
	if err != nil || string(got) != "Release fixture" {
		t.Fatalf("candidate=%q err=%v", got, err)
	}
	mode, err := os.Stat(filepath.Join(output, "dists", "stable", "Release"))
	if err != nil || mode.Mode().Perm() != 0644 {
		t.Fatalf("candidate mode=%v err=%v", mode, err)
	}
}

func TestSSHClientOverPinnedRestrictedSSHProtocol(t *testing.T) {
	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostPublicKey, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := ssh.NewPublicKey(clientPublic)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), authorized.Marshal()) {
			return nil, fmt.Errorf("unexpected client key")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			sshConn, channels, requests, handshakeErr := ssh.NewServerConn(conn, serverConfig)
			if handshakeErr != nil {
				_ = conn.Close()
				continue
			}
			go ssh.DiscardRequests(requests)
			for newChannel := range channels {
				if newChannel.ChannelType() != "session" {
					_ = newChannel.Reject(ssh.UnknownChannelType, "sessions only")
					continue
				}
				channel, channelRequests, channelErr := newChannel.Accept()
				if channelErr != nil {
					continue
				}
				go func(ch ssh.Channel, reqs <-chan *ssh.Request) {
					defer ch.Close()
					for request := range reqs {
						if request.Type != "exec" {
							_ = request.Reply(false, nil)
							continue
						}
						// Like sshd ForceCommand, ignore the client's requested command.
						_ = request.Reply(true, nil)
						if workerErr := serveProtocolFixture(ch); workerErr != nil {
							_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
							return
						}
						_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						return
					}
				}(channel, channelRequests)
			}
			_ = sshConn.Close()
		}
	}()
	root := t.TempDir()
	identity := filepath.Join(root, "identity")
	known := filepath.Join(root, "known_hosts")
	keyBlock, err := ssh.MarshalPrivateKey(clientPrivate, "tparun-test")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(identity, pem.EncodeToMemory(keyBlock), 0600); err != nil {
		t.Fatal(err)
	}
	hostPublic, err := ssh.NewPublicKey(hostPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	knownEntry := fmt.Sprintf("[127.0.0.1]:%d %s", port, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostPublic))))
	if err = os.WriteFile(known, []byte(knownEntry+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "input")
	if err = os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(input, "fixture.deb"), []byte("input deb"), 0644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "candidate")
	runner := SSHRunner{Config: SSHConfig{Target: "worker@127.0.0.1", Port: port, IdentityFile: identity, KnownHostsFile: known, SSHBinary: "ssh", ConnectTimeout: 5 * time.Second}}
	request := RemoteRequest{PublishRequest: PublishRequest{Input: input, Output: output}, JobID: "0123456789abcdef0123456789abcdef", AttemptID: "11111111111111111111111111111111", StartedAt: "2026-01-01T00:00:00Z", MaxBytes: 1 << 20, MaxInodes: 100}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = runner.PublishRemote(ctx, request); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(output, "dists", "stable", "Release"))
	if err != nil || string(got) != "Release fixture" {
		t.Fatalf("candidate=%q err=%v", got, err)
	}
	wrongHostPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongSigner, err := ssh.NewPublicKey(wrongHostPub)
	if err != nil {
		t.Fatal(err)
	}
	wrongKnown := filepath.Join(root, "wrong_known_hosts")
	wrongEntry := fmt.Sprintf("[127.0.0.1]:%d %s\n", port, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(wrongSigner))))
	if err = os.WriteFile(wrongKnown, []byte(wrongEntry), 0600); err != nil {
		t.Fatal(err)
	}
	wrongRunner := runner
	wrongRunner.Config.KnownHostsFile = wrongKnown
	wrongRequest := request
	wrongRequest.Output = filepath.Join(root, "should-not-exist")
	if err = wrongRunner.PublishRemote(ctx, wrongRequest); err == nil {
		t.Fatal("mismatched pinned host key was accepted")
	}
	if _, err = os.Lstat(wrongRequest.Output); !os.IsNotExist(err) {
		t.Fatalf("candidate appeared after host-key failure: %v", err)
	}
}

func serveProtocolFixture(channel ssh.Channel) error {
	header, payload, err := readFrame(channel)
	if err != nil {
		return err
	}
	if header.Kind != "build" || header.Version != sshProtocolVersion || len(payload) == 0 {
		return fmt.Errorf("unexpected request")
	}
	root, err := os.MkdirTemp("", "ssh-server-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err = os.MkdirAll(filepath.Join(root, "dists", "stable"), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "dists", "stable", "Release"), []byte("Release fixture"), 0644); err != nil {
		return err
	}
	result, files, err := archiveTree(root, header.MaxBytes)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(result)
	response := protocolHeader{Version: sshProtocolVersion, Kind: "result", JobID: header.JobID, AttemptID: header.AttemptID, Backend: header.Backend, StartedAt: header.StartedAt, MaxBytes: header.MaxBytes, MaxInodes: header.MaxInodes, PayloadSize: int64(len(result)), PayloadHash: hex.EncodeToString(sum[:]), Files: files}
	return writeFrame(channel, response, result)
}

func TestSSHProtocolHelperProcess(t *testing.T) {
	if os.Getenv("TPARUN_SSH_PROTOCOL_TEST_HELPER") == "" {
		return
	}
	h, payload, err := readFrame(os.Stdin)
	if err != nil {
		_, _ = io.WriteString(os.Stderr, err.Error())
		os.Exit(10)
	}
	if h.Kind != "build" || h.Version != sshProtocolVersion || h.JobID == "" || len(payload) == 0 || os.Getenv("TPARUN_PRIVATE_TEST_SENTINEL") != "" {
		os.Exit(11)
	}
	root, err := os.MkdirTemp("", "ssh-protocol-helper-")
	if err != nil {
		os.Exit(12)
	}
	defer os.RemoveAll(root)
	if err = os.MkdirAll(filepath.Join(root, "dists", "stable"), 0700); err != nil {
		os.Exit(13)
	}
	if err = os.WriteFile(filepath.Join(root, "dists", "stable", "Release"), []byte("Release fixture"), 0644); err != nil {
		os.Exit(14)
	}
	out, files, err := archiveTree(root, h.MaxBytes)
	if err != nil {
		os.Exit(15)
	}
	sum := sha256.Sum256(out)
	response := protocolHeader{Version: sshProtocolVersion, Kind: "result", JobID: h.JobID, AttemptID: h.AttemptID, Backend: h.Backend, StartedAt: h.StartedAt, MaxBytes: h.MaxBytes, MaxInodes: h.MaxInodes, PayloadSize: int64(len(out)), PayloadHash: hex.EncodeToString(sum[:]), Files: files}
	if err = writeFrame(os.Stdout, response, out); err != nil {
		os.Exit(16)
	}
	os.Exit(0)
}

func makeFixtureArchive(t *testing.T, name, content string, mode int64) ([]byte, []protocolFile, error) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), os.FileMode(mode)); err != nil {
		t.Fatal(err)
	}
	return archiveTree(root, 1<<20)
}
func sha256Hex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
