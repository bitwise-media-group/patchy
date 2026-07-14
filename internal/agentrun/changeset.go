// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/envelope"
)

// errChangesetTooLarge marks a changeset whose cumulative content exceeds
// the configured cap; the caller maps it to OutcomeChangesetTooLarge.
var errChangesetTooLarge = errors.New("changeset too large")

// buildChangeset expresses base..branch as file contents for the API push.
// Renames arrive as delete+add (--no-renames); gitlinks are rejected — the
// GitHub API push cannot express submodule pointer changes.
func buildChangeset(ctx context.Context, dir, baseSHA, branch string, maxBytes int) (*envelope.Changeset, error) {
	cs := &envelope.Changeset{BaseSHA: baseSHA}

	message, err := git(ctx, dir, "log", "--reverse", "--format=%B", baseSHA+".."+branch)
	if err != nil {
		return nil, err
	}
	cs.CommitMessage = message

	diff, err := gitRaw(ctx, dir, "diff", "--name-status", "-z", "--no-renames", baseSHA+".."+branch)
	if err != nil {
		return nil, err
	}
	total := 0
	fields := strings.Split(strings.TrimSuffix(string(diff), "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		status, path := fields[i], fields[i+1]
		switch status {
		case "D":
			cs.Deletes = append(cs.Deletes, path)
		case "A", "M", "T":
			fc, rawLen, err := fileChange(ctx, dir, branch, path)
			if err != nil {
				return nil, err
			}
			total += rawLen
			if total > maxBytes {
				return nil, fmt.Errorf("%w: content exceeds %d bytes at %s", errChangesetTooLarge, maxBytes, path)
			}
			cs.Upserts = append(cs.Upserts, fc)
		default:
			return nil, fmt.Errorf("changeset: unsupported diff status %q for %s", status, path)
		}
	}
	if len(cs.Upserts) == 0 && len(cs.Deletes) == 0 {
		return nil, fmt.Errorf("changeset: %s..%s is empty", baseSHA, branch)
	}
	return cs, nil
}

// fileChange reads one changed file's mode and blob content from the branch,
// also returning the raw content length for the cumulative size cap.
func fileChange(ctx context.Context, dir, branch, path string) (envelope.FileChange, int, error) {
	entry, err := git(ctx, dir, "ls-tree", branch, "--", path)
	if err != nil {
		return envelope.FileChange{}, 0, err
	}
	// "<mode> <type> <sha>\t<path>"
	meta, _, ok := strings.Cut(entry, "\t")
	parts := strings.Fields(meta)
	if !ok || len(parts) != 3 {
		return envelope.FileChange{}, 0, fmt.Errorf("changeset: unexpected ls-tree entry for %s: %q", path, entry)
	}
	mode, typ, sha := parts[0], parts[1], parts[2]
	switch mode {
	case "100644", "100755", "120000":
	default:
		return envelope.FileChange{}, 0, fmt.Errorf("changeset: unsupported mode %s (%s) for %s", mode, typ, path)
	}
	content, err := gitRaw(ctx, dir, "cat-file", "blob", sha)
	if err != nil {
		return envelope.FileChange{}, 0, err
	}
	return envelope.FileChange{Path: path, Mode: mode, ContentB64: encodeB64(content)}, len(content), nil
}
