package tpa

// This file implements protocol v1 for the opt-in SSH publication runner.
// SSH carries only a deterministic archive of .deb inputs and a bounded,
// untrusted candidate archive. Signing authority remains on the server.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	sshProtocolVersion = 1
	maxProtocolHeader  = 4 << 20
	maxWorkerError     = 16 << 10
)

type SSHConfig struct {
	Target, IdentityFile, KnownHostsFile string
	Port                                 int
	ConnectTimeout                       time.Duration
	SSHBinary                            string
}

type SSHRunner struct {
	Config SSHConfig
}

type protocolFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

type protocolHeader struct {
	Version     int            `json:"version"`
	Kind        string         `json:"kind"`
	JobID       string         `json:"job_id"`
	AttemptID   string         `json:"attempt_id"`
	Backend     string         `json:"backend"`
	StartedAt   string         `json:"started_at"`
	MaxBytes    int64          `json:"max_bytes"`
	MaxInodes   int64          `json:"max_inodes"`
	PayloadSize int64          `json:"payload_size"`
	PayloadHash string         `json:"payload_sha256"`
	Files       []protocolFile `json:"files,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type RemoteRequest struct {
	PublishRequest
	JobID, AttemptID, StartedAt string
	MaxBytes, MaxInodes         int64
}

func (r SSHRunner) Check(ctx context.Context) error {
	if strings.TrimSpace(r.Config.Target) == "" || strings.HasPrefix(r.Config.Target, "-") || strings.ContainsAny(r.Config.Target, " \t\r\n") {
		return fmt.Errorf("SSH publication target must be one OpenSSH destination token")
	}
	if r.Config.IdentityFile == "" {
		return fmt.Errorf("SSH publication requires an explicit identity file")
	}
	if r.Config.Port < 0 || r.Config.Port > 65535 {
		return fmt.Errorf("SSH publication port must be between 1 and 65535")
	}
	if r.Config.KnownHostsFile == "" {
		return fmt.Errorf("SSH publication requires a pinned known-hosts file")
	}
	if r.Config.SSHBinary == "" {
		r.Config.SSHBinary = "ssh"
	}
	if _, err := exec.LookPath(r.Config.SSHBinary); err != nil {
		return fmt.Errorf("SSH client unavailable: %w", err)
	}
	for _, path := range []string{r.Config.KnownHostsFile, r.Config.IdentityFile} {
		if strings.ContainsAny(path, "\r\n") {
			return fmt.Errorf("SSH configuration path contains a line break")
		}
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("SSH credential/configuration file is not a regular file: %s", path)
		}
	}
	return nil
}

func (c SSHConfig) PortOrDefault() int {
	if c.Port == 0 {
		return 22
	}
	return c.Port
}

func (r SSHRunner) PublishBounded(ctx context.Context, request PublishRequest, maximum int64) error {
	return r.PublishRemote(ctx, RemoteRequest{PublishRequest: request, JobID: request.JobID, AttemptID: request.AttemptID, StartedAt: request.StartedAt, MaxBytes: maximum, MaxInodes: defaultPublicationInodes})
}

func (r SSHRunner) PublishBoundedWithInodes(ctx context.Context, request PublishRequest, maximum, inodeLimit int64) error {
	return r.PublishRemote(ctx, RemoteRequest{PublishRequest: request, JobID: request.JobID, AttemptID: request.AttemptID, StartedAt: request.StartedAt, MaxBytes: maximum, MaxInodes: inodeLimit})
}

func (r SSHRunner) PublishRemote(ctx context.Context, request RemoteRequest) error {
	if err := r.Check(ctx); err != nil {
		return err
	}
	if request.JobID == "" || request.AttemptID == "" || request.StartedAt == "" || request.MaxBytes <= 0 || request.MaxInodes <= 0 {
		return fmt.Errorf("incomplete SSH publication attempt identity or resource policy")
	}
	payload, files, err := archiveInputs(request.Input)
	if err != nil {
		return fmt.Errorf("prepare immutable SSH input: %w", err)
	}
	if int64(len(payload)) > request.MaxBytes {
		return fmt.Errorf("immutable SSH input exceeds transfer bound")
	}
	digest := sha256.Sum256(payload)
	requestHeader := protocolHeader{Version: sshProtocolVersion, Kind: "build", JobID: request.JobID, AttemptID: request.AttemptID,
		Backend: "ssh", StartedAt: request.StartedAt, MaxBytes: request.MaxBytes, MaxInodes: request.MaxInodes,
		PayloadSize: int64(len(payload)), PayloadHash: hex.EncodeToString(digest[:]), Files: files}

	var input bytes.Buffer
	if err = writeFrame(&input, requestHeader, payload); err != nil {
		return err
	}
	sshArgs := []string{"-F", "/dev/null", "-T", "-p", fmt.Sprint(r.Config.PortOrDefault()), "-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-oGlobalKnownHostsFile=/dev/null", "-oUpdateHostKeys=no", "-oUserKnownHostsFile=" + r.Config.KnownHostsFile,
		"-oClearAllForwardings=yes", "-oForwardAgent=no", "-oPasswordAuthentication=no", "-oKbdInteractiveAuthentication=no",
		"-oIdentitiesOnly=yes", "-oIdentityAgent=none", "-oProxyCommand=none", "-oProxyJump=none", "-oLocalCommand=none", "-oPermitLocalCommand=no", "-oKnownHostsCommand=none", "-oControlMaster=no", "-oControlPath=none", "-oRequestTTY=no"}
	if r.Config.ConnectTimeout < 0 {
		return fmt.Errorf("SSH connection timeout must not be negative")
	}
	if r.Config.ConnectTimeout > 0 {
		seconds := int(r.Config.ConnectTimeout / time.Second)
		if r.Config.ConnectTimeout%time.Second != 0 {
			seconds++
		}
		if seconds < 1 {
			seconds = 1
		}
		sshArgs = append(sshArgs, "-oConnectTimeout="+fmt.Sprint(seconds))
	}
	if r.Config.IdentityFile != "" {
		sshArgs = append(sshArgs, "-i", r.Config.IdentityFile)
	}
	sshArgs = append(sshArgs, r.Config.Target, "worker")
	binary := r.Config.SSHBinary
	if binary == "" {
		binary = "ssh"
	}
	cmd := exec.CommandContext(ctx, binary, sshArgs...)
	cmd.Env = []string{"LC_ALL=C"}
	cmd.Stdin = &input
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var diagnostic boundedDiagnostic
	cmd.Stderr = &diagnostic
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start SSH publication worker: %w", err)
	}
	candidateErr := readCandidate(stdout, request)
	if candidateErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		if candidateErr != nil {
			return fmt.Errorf("SSH publication attempt %s failed: %w (%s)", request.AttemptID, candidateErr, diagnostic.String())
		}
		return fmt.Errorf("SSH publication worker failed: %w: %s", waitErr, diagnostic.String())
	}
	return candidateErr
}

// RunWorker implements the forced-command worker endpoint. It never reads
// ambient server configuration and emits protocol bytes only on stdout.
func RunWorker(ctx context.Context, in io.Reader, out io.Writer, bin string) error {
	header, payload, err := readFrame(in)
	if err != nil {
		return err
	}
	var trailing [1]byte
	if n, readErr := io.ReadFull(in, trailing[:]); n != 0 || !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("trailing SSH worker request data")
	}
	if header.Version != sshProtocolVersion || header.Kind != "build" || header.Backend != "ssh" || !validAttemptToken(header.JobID) || !validAttemptToken(header.AttemptID) || header.StartedAt == "" {
		return fmt.Errorf("unsupported or invalid SSH worker request")
	}
	if header.MaxBytes <= 0 || header.MaxInodes <= 0 || header.PayloadSize != int64(len(payload)) || header.PayloadSize < 0 || header.PayloadSize > header.MaxBytes {
		return fmt.Errorf("invalid SSH worker resource or payload bound")
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != header.PayloadHash {
		return fmt.Errorf("SSH worker input digest mismatch")
	}
	tempRoot := os.TempDir()
	if err = cleanupStaleWorkerDirs(tempRoot); err != nil {
		return fmt.Errorf("clean abandoned worker directories: %w", err)
	}
	root, lease, err := createWorkerWorkspace(tempRoot)
	if err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(int(lease.Fd()), syscall.LOCK_UN)
		_ = lease.Close()
		_ = os.RemoveAll(root)
	}()
	input := filepath.Join(root, "input")
	if err = os.Mkdir(input, 0700); err != nil {
		return err
	}
	if err = unpackArchive(payload, header.Files, input, header.MaxBytes, header.MaxInodes, true); err != nil {
		return fmt.Errorf("invalid SSH worker input: %w", err)
	}
	candidate := filepath.Join(root, "candidate")
	if err = (Runner{Bin: bin, IsolateEnvironment: true}).PublishBoundedWithInodes(ctx, PublishRequest{Input: input, Output: candidate}, header.MaxBytes, header.MaxInodes); err != nil {
		return writeWorkerError(out, header, err)
	}
	responsePayload, files, err := archiveTree(candidate, header.MaxBytes)
	if err != nil {
		return writeWorkerError(out, header, err)
	}
	resultHash := sha256.Sum256(responsePayload)
	response := protocolHeader{Version: sshProtocolVersion, Kind: "result", JobID: header.JobID, AttemptID: header.AttemptID,
		Backend: "ssh", StartedAt: header.StartedAt, MaxBytes: header.MaxBytes, MaxInodes: header.MaxInodes,
		PayloadSize: int64(len(responsePayload)), PayloadHash: hex.EncodeToString(resultHash[:]), Files: files}
	return writeFrame(out, response, responsePayload)
}

func createWorkerWorkspace(tempRoot string) (string, *os.File, error) {
	root, err := os.MkdirTemp(tempRoot, "tparun-worker-")
	if err != nil {
		return "", nil, err
	}
	leasePath := filepath.Join(root, ".lease")
	lease, err := os.OpenFile(leasePath, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
	if err != nil {
		_ = os.RemoveAll(root)
		return "", nil, err
	}
	if err = syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lease.Close()
		_ = os.RemoveAll(root)
		return "", nil, err
	}
	return root, lease, nil
}

// cleanupStaleWorkerDirs removes only abandoned workspaces created by this
// worker implementation. A held lease protects concurrent worker invocations;
// legacy or incomplete directories without a lease are left for operator cleanup.
func cleanupStaleWorkerDirs(tempRoot string) error {
	entries, err := os.ReadDir(tempRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "tparun-worker-") {
			continue
		}
		root := filepath.Join(tempRoot, entry.Name())
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() {
			continue
		}
		leasePath := filepath.Join(root, ".lease")
		leaseInfo, err := os.Lstat(leasePath)
		if err != nil || !leaseInfo.Mode().IsRegular() || leaseInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		leaseStat, ok := leaseInfo.Sys().(*syscall.Stat_t)
		if !ok || int(leaseStat.Uid) != os.Geteuid() {
			continue
		}
		fd, err := syscall.Open(leasePath, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			continue
		}
		lease := os.NewFile(uintptr(fd), leasePath)
		if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = lease.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
				continue
			}
			return err
		}
		current, statErr := os.Lstat(root)
		if statErr == nil && os.SameFile(info, current) {
			if removeErr := os.RemoveAll(root); removeErr != nil {
				_ = syscall.Flock(fd, syscall.LOCK_UN)
				_ = lease.Close()
				return removeErr
			}
		}
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = lease.Close()
	}
	return nil
}

func writeWorkerError(out io.Writer, request protocolHeader, cause error) error {
	message := cause.Error()
	if len(message) > maxWorkerError {
		message = message[:maxWorkerError]
	}
	return writeFrame(out, protocolHeader{Version: sshProtocolVersion, Kind: "error", JobID: request.JobID, AttemptID: request.AttemptID,
		Backend: "ssh", StartedAt: request.StartedAt, Error: message}, nil)
}

func writeFrame(w io.Writer, header protocolHeader, payload []byte) error {
	encoded, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if len(encoded) == 0 || len(encoded) > maxProtocolHeader {
		return fmt.Errorf("SSH protocol header exceeds limit")
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(encoded)))
	if _, err = io.Copy(w, bytes.NewReader(length[:])); err != nil {
		return err
	}
	if _, err = io.Copy(w, bytes.NewReader(encoded)); err != nil {
		return err
	}
	_, err = io.Copy(w, bytes.NewReader(payload))
	return err
}

func decodeProtocolHeader(data []byte, header *protocolHeader) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(header); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing protocol header data")
	}
	return nil
}

func readFrame(r io.Reader) (protocolHeader, []byte, error) {
	var header protocolHeader
	var length [4]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return header, nil, fmt.Errorf("read SSH protocol frame: %w", err)
	}
	n := binary.BigEndian.Uint32(length[:])
	if n == 0 || n > maxProtocolHeader {
		return header, nil, fmt.Errorf("invalid SSH protocol header size")
	}
	encoded := make([]byte, n)
	if _, err := io.ReadFull(r, encoded); err != nil {
		return header, nil, err
	}
	if err := decodeProtocolHeader(encoded, &header); err != nil {
		return header, nil, err
	}
	if header.PayloadSize < 0 || header.PayloadSize > 1<<40 {
		return header, nil, fmt.Errorf("invalid SSH protocol payload length")
	}
	payload := make([]byte, header.PayloadSize)
	if _, err := io.ReadFull(r, payload); err != nil {
		return header, nil, err
	}
	return header, payload, nil
}

func validAttemptToken(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func archiveInputs(root string) ([]byte, []protocolFile, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".deb") {
			return nil, nil, fmt.Errorf("unexpected publication input entry %q", entry.Name())
		}
	}
	return archiveTree(root, 1<<40)
}

func archiveTree(root string, maxBytes int64) ([]byte, []protocolFile, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	files := make([]protocolFile, 0, len(entries))
	var payload bytes.Buffer
	tw := tar.NewWriter(&payload)
	var names []string
	if err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archive contains a non-regular entry")
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		tw.Close()
		return nil, nil, err
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			tw.Close()
			return nil, nil, err
		}
		if info.Size() < 0 || info.Size() > maxBytes {
			tw.Close()
			return nil, nil, fmt.Errorf("publication archive exceeds bound")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			tw.Close()
			return nil, nil, err
		}
		if int64(len(data)) != info.Size() {
			tw.Close()
			return nil, nil, fmt.Errorf("publication input changed while archiving")
		}
		sum := sha256.Sum256(data)
		files = append(files, protocolFile{Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Mode: uint32(info.Mode().Perm())})
		h := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if err = tw.WriteHeader(h); err != nil {
			tw.Close()
			return nil, nil, err
		}
		if _, err = tw.Write(data); err != nil {
			tw.Close()
			return nil, nil, err
		}
	}
	if err = tw.Close(); err != nil {
		return nil, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	if int64(payload.Len()) > maxBytes {
		return nil, nil, fmt.Errorf("publication archive exceeds byte bound")
	}
	return payload.Bytes(), files, nil
}

func unpackArchive(payload []byte, files []protocolFile, root string, maxBytes, maxInodes int64, debInputs bool) error {
	if payload == nil || int64(len(payload)) > maxBytes {
		return fmt.Errorf("input payload exceeds byte limit")
	}
	if maxInodes <= 0 || int64(len(files)) > maxInodes {
		return fmt.Errorf("archive exceeds inode limit")
	}
	allowed := make(map[string]protocolFile, len(files))
	directories := make(map[string]struct{})
	var manifestBytes int64
	for _, f := range files {
		clean := filepath.Clean(filepath.FromSlash(f.Name))
		if f.Name == "" || filepath.IsAbs(f.Name) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != f.Name || len(strings.Split(f.Name, "/")) > 32 || (debInputs && (strings.Contains(f.Name, "/") || !strings.HasSuffix(f.Name, ".deb"))) || f.Size < 0 || len(f.SHA256) != 64 || f.Mode > 0777 || f.Size > maxBytes-manifestBytes {
			return fmt.Errorf("invalid input manifest")
		}
		if _, ok := allowed[f.Name]; ok {
			return fmt.Errorf("duplicate input manifest entry")
		}
		manifestBytes += f.Size
		for dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(f.Name))); dir != "."; dir = filepath.ToSlash(filepath.Dir(filepath.FromSlash(dir))) {
			directories[dir] = struct{}{}
		}
		if int64(len(allowed)+len(directories)) > maxInodes {
			return fmt.Errorf("archive exceeds inode limit")
		}
		allowed[f.Name] = f
	}
	r := tar.NewReader(bytes.NewReader(payload))
	seen := make(map[string]bool, len(files))
	var total int64
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		f, ok := allowed[h.Name]
		if !ok || seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size != f.Size || h.Linkname != "" || h.Mode != int64(f.Mode) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(h.Name))) != h.Name {
			return fmt.Errorf("unexpected input archive entry")
		}
		if h.Size > maxBytes-total {
			return fmt.Errorf("input archive exceeds byte limit")
		}
		dest := filepath.Join(root, filepath.FromSlash(h.Name))
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.CopyN(io.MultiWriter(out, hash), r, h.Size)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != h.Size || hex.EncodeToString(hash.Sum(nil)) != f.SHA256 {
			return fmt.Errorf("input file digest mismatch")
		}
		if err := os.Chmod(dest, os.FileMode(f.Mode)); err != nil {
			return err
		}
		total += n
		seen[h.Name] = true
	}
	if len(seen) != len(allowed) {
		return fmt.Errorf("input manifest/archive mismatch")
	}
	return nil
}

func readCandidate(r io.Reader, request RemoteRequest) error {
	var length [4]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return fmt.Errorf("read SSH worker response: %w", err)
	}
	n := binary.BigEndian.Uint32(length[:])
	if n == 0 || n > maxProtocolHeader {
		return fmt.Errorf("invalid SSH worker response header size")
	}
	encoded := make([]byte, n)
	if _, err := io.ReadFull(r, encoded); err != nil {
		return err
	}
	var h protocolHeader
	if err := decodeProtocolHeader(encoded, &h); err != nil {
		return err
	}
	if h.Version != sshProtocolVersion || h.JobID != request.JobID || h.AttemptID != request.AttemptID || h.Backend != "ssh" || h.StartedAt != request.StartedAt {
		return fmt.Errorf("SSH worker returned stale or mismatched attempt identity")
	}
	if h.Kind == "error" {
		if h.Error == "" || len(h.Error) > maxWorkerError {
			return fmt.Errorf("invalid SSH worker error response")
		}
		return fmt.Errorf("remote publication failed: %s", h.Error)
	}
	transportOverhead := int64(maxProtocolHeader)
	if request.MaxInodes <= 0 || request.MaxInodes > (int64(^uint64(0)>>1)-transportOverhead)/1024 || request.MaxBytes <= 0 || request.MaxBytes > int64(^uint64(0)>>1)-transportOverhead-request.MaxInodes*1024 {
		return fmt.Errorf("invalid SSH result bounds")
	}
	transportOverhead += request.MaxInodes * 1024
	if h.Kind != "result" || h.PayloadSize < 0 || h.PayloadSize > request.MaxBytes+transportOverhead || h.MaxBytes != request.MaxBytes || h.MaxInodes != request.MaxInodes || int64(len(h.Files)) > request.MaxInodes {
		return fmt.Errorf("invalid SSH worker result bounds")
	}
	payload, err := io.ReadAll(io.LimitReader(r, h.PayloadSize+1))
	if err != nil {
		return err
	}
	if int64(len(payload)) != h.PayloadSize {
		return fmt.Errorf("SSH worker result length mismatch")
	}
	var extra [1]byte
	if n, err := r.Read(extra[:]); n != 0 || err == nil {
		return fmt.Errorf("trailing SSH worker response data")
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != h.PayloadHash {
		return fmt.Errorf("SSH worker result digest mismatch")
	}
	parent := filepath.Dir(request.Output)
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	if _, err = os.Lstat(request.Output); err == nil {
		return fmt.Errorf("candidate output already exists")
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err = os.Mkdir(request.Output, 0755); err != nil {
		return err
	}
	if err = unpackArchive(payload, h.Files, request.Output, request.MaxBytes, request.MaxInodes, false); err != nil {
		_ = os.RemoveAll(request.Output)
		return fmt.Errorf("invalid remote candidate archive: %w", err)
	}
	return nil
}
