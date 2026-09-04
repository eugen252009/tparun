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

type Runner struct{ Bin, GPGHome, Fingerprint string }

func (r Runner) command(ctx context.Context, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, r.Bin, args...)
	var out, errout bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errout
	c.Env = os.Environ()
	if r.GPGHome != "" {
		c.Env = append(c.Env, "GNUPGHOME="+r.GPGHome)
	}
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

func (r Runner) Publish(ctx context.Context, p PublishRequest) error {
	return r.publish(ctx, p)
}

func (r Runner) publish(ctx context.Context, p PublishRequest) error {
	args := []string{"pack", "-in", p.Input, "--atomic-publish", p.Output}
	if r.Fingerprint != "" {
		args = append(args, "-gpg", r.Fingerprint)
	}
	_, e := r.command(ctx, args...)
	return e
}

// PublishBounded runs the external publisher in a fixed-size tmpfs mount. The
// completed tree is copied to the host only after the publisher succeeds, so a
// writer cannot allocate more candidate data than the admitted bound.
func (r Runner) PublishBounded(ctx context.Context, p PublishRequest, maximum int64) error {
	if maximum <= 0 {
		return fmt.Errorf("invalid publication output bound")
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
	inodeLimit := maximum / int64(os.Getpagesize())
	if inodeLimit < 64 {
		inodeLimit = 64
	}
	args := []string{"pack", "-in", p.Input, "--atomic-publish", p.Output}
	if r.Fingerprint != "" {
		args = append(args, "-gpg", r.Fingerprint)
	}
	args[4] = boundedOutput
	const script = `set -eu
sandbox=$1
output=$2
maximum=$3
inodes=$4
shift 4
mount -t tmpfs -o "size=$maximum,nr_inodes=$inodes,nosuid,nodev" tmpfs "$sandbox"
cleanup() { umount "$sandbox"; }
trap cleanup EXIT INT TERM
prlimit --fsize="$maximum:$maximum" -- "$@"
mkdir -m 0755 "$output"
cp -a "$sandbox/repository/." "$output/"`
	shellArgs := []string{"--user", "--map-root-user", "--mount", "--pid", "--fork", "--kill-child", "sh", "-c", script, "tpahub-output-limit", sandbox, p.Output, fmt.Sprint(maximum), fmt.Sprint(inodeLimit), r.Bin}
	shellArgs = append(shellArgs, args...)
	cmd := exec.CommandContext(ctx, "unshare", shellArgs...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if r.GPGHome != "" {
		cmd.Env = append(cmd.Env, "GNUPGHOME="+r.GPGHome)
	}
	var stderr bytes.Buffer
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
