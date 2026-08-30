package tpa

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	args := []string{"pack", "-in", p.Input, "--atomic-publish", p.Output}
	if r.Fingerprint != "" {
		args = append(args, "-gpg", r.Fingerprint)
	}
	_, e := r.command(ctx, args...)
	return e
}
