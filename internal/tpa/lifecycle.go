package tpa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type LifecycleOperation string

const (
	LifecycleUnlist LifecycleOperation = "unlist"
	LifecycleDelete LifecycleOperation = "delete"
)

type LifecycleRequest struct {
	Operation                       LifecycleOperation
	Input, Output                   string
	Package, Version, Architecture  string
	SigningHome, SigningFingerprint string
}

type LifecycleResult struct {
	Output, Diagnostic string
	CleanupFailed      bool
}

// LifecycleBounded runs TPA against a private tmpfs copy, not the active
// generation. The bounded copy is returned only after TPA completes, including
// the safe-unlist/failed-artifact-cleanup result of delete.
func (r Runner) LifecycleBounded(ctx context.Context, request LifecycleRequest, maximum, inodeLimit int64) (LifecycleResult, error) {
	if request.Operation != LifecycleUnlist && request.Operation != LifecycleDelete {
		return LifecycleResult{}, fmt.Errorf("invalid package lifecycle operation")
	}
	if request.Package == "" || request.Version == "" || request.Architecture == "" || request.SigningFingerprint == "" {
		return LifecycleResult{}, fmt.Errorf("package identity and managed signing key are required")
	}
	if maximum <= 0 || inodeLimit <= 0 {
		return LifecycleResult{}, fmt.Errorf("invalid package lifecycle output bound")
	}
	inputInfo, err := os.Lstat(request.Input)
	if err != nil || !inputInfo.IsDir() || inputInfo.Mode()&os.ModeSymlink != 0 {
		return LifecycleResult{}, fmt.Errorf("lifecycle input must be a real repository directory")
	}
	if _, err = os.Lstat(request.Output); err == nil {
		return LifecycleResult{}, fmt.Errorf("lifecycle output already exists")
	} else if !os.IsNotExist(err) {
		return LifecycleResult{}, err
	}
	parent, err := filepath.Abs(filepath.Dir(request.Output))
	if err != nil {
		return LifecycleResult{}, err
	}
	if err = os.MkdirAll(parent, 0700); err != nil {
		return LifecycleResult{}, err
	}
	protectedPaths, err := r.protectedPaths(request.Input, request.Output, parent)
	if err != nil {
		return LifecycleResult{}, err
	}
	signingHome, err := filepath.Abs(request.SigningHome)
	if err != nil || request.SigningHome == "" {
		return LifecycleResult{}, fmt.Errorf("managed signing home is required")
	}
	signingHome, err = filepath.EvalSymlinks(signingHome)
	if err != nil {
		return LifecycleResult{}, fmt.Errorf("resolve managed signing home: %w", err)
	}
	signingHome, err = filepath.Abs(signingHome)
	if err != nil {
		return LifecycleResult{}, err
	}
	signInfo, err := os.Stat(signingHome)
	if err != nil || !signInfo.IsDir() {
		return LifecycleResult{}, fmt.Errorf("managed signing home is unavailable")
	}
	foundSigningHome := false
	masked := make([]string, 0, len(protectedPaths))
	for _, path := range protectedPaths {
		if path == signingHome {
			foundSigningHome = true
			continue
		}
		masked = append(masked, path)
	}
	if !foundSigningHome {
		return LifecycleResult{}, fmt.Errorf("managed signing home is not protected by the publication runner")
	}
	sandbox, err := os.MkdirTemp(parent, ".tpahub-lifecycle-sandbox-")
	if err != nil {
		return LifecycleResult{}, err
	}
	defer os.RemoveAll(sandbox)
	args := []string{string(request.Operation), "-in=" + filepath.Join(sandbox, "repository"), "-package=" + request.Package, "-ver=" + request.Version, "-arch=" + request.Architecture, "-components=main", "-codename=stable", "-gpg=" + request.SigningFingerprint}
	if request.Operation == LifecycleDelete {
		args = append(args, "--yes")
	}
	const script = `set -eu
sandbox=$1
input=$2
output=$3
maximum=$4
inodes=$5
mask_count=$6
shift 6
i=0
while [ "$i" -lt "$mask_count" ]; do
  mount -t tmpfs -o "size=65536,nr_inodes=16,mode=0700,nosuid,nodev,noexec" tmpfs "$1"
  shift
  i=$((i + 1))
done
mount -t tmpfs -o "size=$maximum,nr_inodes=$inodes,nosuid,nodev,noexec" tmpfs "$sandbox"
cleanup() { umount "$sandbox"; }
trap cleanup EXIT INT TERM
mkdir -m 0700 "$sandbox/repository"
cp -a "$input/." "$sandbox/repository/"
if prlimit --fsize="$maximum:$maximum" -- setpriv --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- "$@" >"$sandbox/stdout" 2>"$sandbox/stderr"; then
  status=0
else
  status=$?
fi
cat "$sandbox/stdout"
cat "$sandbox/stderr" >&2
cleanup_failure=0
if [ "$status" -ne 0 ] && grep -Fq 'was unlisted, but artifact deletion failed:' "$sandbox/stderr"; then
  cleanup_failure=1
fi
if [ "$status" -eq 0 ] || [ "$cleanup_failure" -eq 1 ]; then
  mkdir -m 0755 "$output"
  cp -a "$sandbox/repository/." "$output/"
fi
exit "$status"`
	shellArgs := []string{"--user", "--map-root-user", "--mount", "--pid", "--fork", "--kill-child", "sh", "-c", script, "tpahub-lifecycle-limit", sandbox, request.Input, request.Output, fmt.Sprint(maximum), fmt.Sprint(inodeLimit), fmt.Sprint(len(masked))}
	shellArgs = append(shellArgs, masked...)
	shellArgs = append(shellArgs, r.Bin)
	shellArgs = append(shellArgs, args...)
	cmd := exec.CommandContext(ctx, "unshare", shellArgs...)
	environment := runnerEnvironment("/nonexistent")
	environment = append(environment, "GNUPGHOME="+signingHome)
	cmd.Env = environment
	var stdout, stderr boundedDiagnostic
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return LifecycleResult{}, ctx.Err()
	}
	result := LifecycleResult{Output: stdout.String(), Diagnostic: stderr.String()}
	if runErr == nil {
		return result, nil
	}
	if strings.Contains(stderr.String(), "was unlisted, but artifact deletion failed:") {
		if _, statErr := os.Lstat(request.Output); statErr == nil {
			result.CleanupFailed = true
			return result, nil
		}
	}
	if errors.Is(runErr, exec.ErrNotFound) {
		return result, fmt.Errorf("bounded TPA lifecycle executable unavailable: %w", runErr)
	}
	message := strings.TrimSpace(stderr.String())
	if message != "" {
		return result, fmt.Errorf("bounded TPA lifecycle failed: %w: %s", runErr, message)
	}
	return result, fmt.Errorf("bounded TPA lifecycle failed: %w", runErr)
}
