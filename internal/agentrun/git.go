// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// git runs one git command in dir, returning trimmed stdout.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// ensureIdentity configures a commit identity when the clone has none, so
// commit.sh can commit.
func ensureIdentity(ctx context.Context, dir string) error {
	if _, err := git(ctx, dir, "config", "user.email"); err == nil {
		return nil
	}
	if _, err := git(ctx, dir, "config", "user.email", "patchy[bot]@users.noreply.github.com"); err != nil {
		return err
	}
	_, err := git(ctx, dir, "config", "user.name", "patchy[bot]")
	return err
}

// checkoutBranch creates (or resets to HEAD) the remediation branch.
func checkoutBranch(ctx context.Context, dir, branch string) error {
	_, err := git(ctx, dir, "checkout", "-B", branch)
	return err
}

// verifyCommitted checks the working tree is clean and the branch carries
// at least one commit over base.
func verifyCommitted(ctx context.Context, dir, base, branch string) error {
	status, err := git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("working tree not clean after commit.sh:\n%s", status)
	}
	count, err := git(ctx, dir, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(count)
	if err != nil || n < 1 {
		return fmt.Errorf("branch %s carries no commits over %s", branch, base)
	}
	return nil
}

// bundle writes a git bundle of base..branch to path.
func bundle(ctx context.Context, dir, base, branch, path string) error {
	_, err := git(ctx, dir, "bundle", "create", path, base+".."+branch)
	return err
}
