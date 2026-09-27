package tpa

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner executes TPA without GPG signing authority. ProtectedPaths are masked
// inside bounded publication namespaces and never passed to the TPA process.
type Runner struct {
	Bin            string
	ProtectedPaths []string
}

func runnerEnvironment(home string) []string {
	filtered := make([]string, 0, len(os.Environ())+2)
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		switch name {
		case "GNUPGHOME", "GPG_AGENT_INFO", "GPG_TTY", "TPARUN_GPG_FINGERPRINT", "HOME", "LC_ALL":
			continue
		}
		filtered = append(filtered, variable)
	}
	return append(filtered, "HOME="+home, "LC_ALL=C")
}

func (r Runner) protectedPaths(input, output, parent string) ([]string, error) {
	inputPath, err := canonicalExistingPath(input)
	if err != nil {
		return nil, err
	}
	parentPath, err := canonicalExistingPath(parent)
	if err != nil {
		return nil, err
	}
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return nil, err
	}
	protected := make([]string, 0, len(r.ProtectedPaths))
	seen := make(map[string]bool, len(r.ProtectedPaths))
	for _, path := range r.ProtectedPaths {
		absolute, absErr := filepath.Abs(path)
		if absErr != nil {
			return nil, absErr
		}
		resolved, resolveErr := filepath.EvalSymlinks(absolute)
		if os.IsNotExist(resolveErr) {
			continue
		}
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve protected runner path: %w", resolveErr)
		}
		resolved, resolveErr = filepath.Abs(resolved)
		if resolveErr != nil {
			return nil, resolveErr
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("protected runner path is not a directory: %s", path)
		}
		if resolved == string(filepath.Separator) || pathsOverlap(resolved, inputPath) || pathsOverlap(resolved, outputPath) || resolved == parentPath || pathContains(resolved, parentPath) {
			return nil, fmt.Errorf("protected runner path overlaps publication files: %s", path)
		}
		if !seen[resolved] {
			protected = append(protected, resolved)
			seen[resolved] = true
		}
	}
	return protected, nil
}

func canonicalExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return filepath.Abs(resolved)
	}
	if os.IsNotExist(err) {
		return absolute, nil
	}
	return "", err
}

func pathsOverlap(a, b string) bool {
	return pathContains(a, b) || pathContains(b, a)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}

const defaultPublicationInodes int64 = 65536
const maxDiagnosticBytes = 64 << 10

type boundedDiagnostic struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *boundedDiagnostic) Len() int { return b.buf.Len() }

func (b *boundedDiagnostic) Write(p []byte) (int, error) {
	original := len(p)
	remaining := maxDiagnosticBytes - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return original, nil
	}
	_, err := b.buf.Write(p)
	return original, err
}

func (b *boundedDiagnostic) String() string {
	message := b.buf.String()
	if b.truncated {
		return message + "\n[diagnostic output truncated]"
	}
	return message
}

func (r Runner) command(ctx context.Context, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, r.Bin, args...)
	var out bytes.Buffer
	var errout boundedDiagnostic
	c.Stdout = &out
	c.Stderr = &errout
	c.Env = runnerEnvironment("/nonexistent")
	e := c.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if e != nil {
		if errors.Is(e, exec.ErrNotFound) {
			return nil, fmt.Errorf("TPA executable not found: %s", r.Bin)
		}
		msg := strings.TrimSpace(errout.String())
		if msg != "" {
			return nil, fmt.Errorf("TPA failed: %w: %s", e, msg)
		}
		return nil, fmt.Errorf("TPA failed: %w", e)
	}
	return out.Bytes(), nil
}
func (r Runner) Check(ctx context.Context) error {
	_, e := r.command(ctx, "schema")
	if e != nil {
		return fmt.Errorf("TPA executable failed validation: %w", e)
	}
	return nil
}

type PublishRequest struct{ Input, Output string }

// PublishBounded runs the external publisher in a fixed-size tmpfs mount. The
// completed tree is copied to the host only after the publisher succeeds, so a
// writer cannot allocate more candidate data than the admitted bound.
func (r Runner) PublishBounded(ctx context.Context, p PublishRequest, maximum int64) error {
	return r.PublishBoundedWithInodes(ctx, p, maximum, defaultPublicationInodes)
}

// PublishBoundedWithInodes runs the publisher with an explicit byte bound and
// an explicit tmpfs inode-count bound. The two resources are independent.
func (r Runner) PublishBoundedWithInodes(ctx context.Context, p PublishRequest, maximum, inodeLimit int64) error {
	if maximum <= 0 {
		return fmt.Errorf("invalid publication output bound")
	}
	if inodeLimit <= 0 {
		return fmt.Errorf("invalid publication inode bound")
	}
	if _, err := os.Lstat(p.Output); err == nil {
		return fmt.Errorf("publication output already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	parent, err := filepath.Abs(filepath.Dir(p.Output))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	protectedPaths, err := r.protectedPaths(p.Input, p.Output, parent)
	if err != nil {
		return err
	}
	sandbox, err := os.MkdirTemp(parent, ".tpahub-candidate-sandbox-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(sandbox)
	defer func() {
		if err != nil {
			_ = os.RemoveAll(p.Output)
		}
	}()
	boundedOutput := filepath.Join(sandbox, "repository")
	args := []string{"pack", "-in", p.Input, "--atomic-publish", boundedOutput}
	const script = `set -eu
sandbox=$1
output=$2
maximum=$3
inodes=$4
mask_count=$5
shift 5
i=0
while [ "$i" -lt "$mask_count" ]; do
  mount -t tmpfs -o "size=65536,nr_inodes=16,mode=0700,nosuid,nodev,noexec" tmpfs "$1"
  shift
  i=$((i + 1))
done
mount -t tmpfs -o "size=$maximum,nr_inodes=$inodes,nosuid,nodev,noexec" tmpfs "$sandbox"
cleanup() { umount "$sandbox"; }
trap cleanup EXIT INT TERM
prlimit --fsize="$maximum:$maximum" -- setpriv --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- "$@"
mkdir -m 0755 "$output"
cp -a "$sandbox/repository/." "$output/"`
	shellArgs := []string{"--user", "--map-root-user", "--mount", "--pid", "--fork", "--kill-child", "sh", "-c", script, "tpahub-output-limit", sandbox, p.Output, fmt.Sprint(maximum), fmt.Sprint(inodeLimit), fmt.Sprint(len(protectedPaths))}
	shellArgs = append(shellArgs, protectedPaths...)
	shellArgs = append(shellArgs, r.Bin)
	shellArgs = append(shellArgs, args...)
	cmd := exec.CommandContext(ctx, "unshare", shellArgs...)
	cmd.Env = runnerEnvironment("/nonexistent")
	var stderr boundedDiagnostic
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("bounded TPA publication failed: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("bounded TPA publication failed: %w", err)
	}
	return nil
}
