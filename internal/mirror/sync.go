// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/mirror/helmchart"
	"github.com/bitwise-media-group/patchy/internal/mirror/imageref"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
	"github.com/bitwise-media-group/patchy/internal/mirror/verify"
)

// Sync actions.
const (
	// ActionPushed means the artifact was published (or would be, dry run).
	ActionPushed = "pushed"
	// ActionSkippedTagExists means the chart tag already exists — an
	// existing tag is NEVER replaced.
	ActionSkippedTagExists = "skipped-tag-exists"
	// ActionSkippedCurrent means the target already matches the lock and
	// carries a valid mirror signature.
	ActionSkippedCurrent = "skipped-current"
)

// SyncRecord is one published artifact's outcome.
type SyncRecord struct {
	// Ref is the mirror reference operated on.
	Ref string `json:"ref"`
	// Digest is the manifest digest at Ref after the sync.
	Digest string `json:"digest"`
	// Action is what happened (pushed, skipped-tag-exists,
	// skipped-current).
	Action string `json:"action"`
	// Signed reports whether a signature was created this run.
	Signed bool `json:"signed"`
}

// SyncResult is one entry's sync outcome.
type SyncResult struct {
	Name    string       `json:"name"`
	Kind    string       `json:"kind"`
	Records []SyncRecord `json:"records"`
	// Err carries a per-entry failure in --all summaries.
	Err string `json:"error,omitempty"`
}

// Sync converges the registry onto the entry's committed state. Read-only
// on the working tree: every write goes to the registry. Idempotent per
// tag — an existing chart tag is never replaced, a current-and-signed
// image is skipped. dryRun computes every action without writing.
func (e *Engine) Sync(ctx context.Context, entry spec.Entry, dryRun bool) (*SyncResult, error) {
	result := &SyncResult{Name: entry.Name, Kind: entry.Kind}
	signing := e.global.Signing
	if override := entry.Signing(); override != nil {
		signing = *override
	}
	if entry.Kind == spec.KindArtifact {
		lock, err := spec.LoadArtifactLock(entry.LockPath())
		if err != nil {
			return nil, fmt.Errorf("%s: no lock (run upgrade first): %w", entry.Name, err)
		}
		rec, err := e.syncImage(ctx, entry.Name, lock.Artifact.Ref+":"+lock.Artifact.Version,
			lock.Artifact.Digest, lock.Artifact.Target, &signing, dryRun)
		if err != nil {
			return nil, err
		}
		result.Records = append(result.Records, rec)
		return result, nil
	}

	rec, err := e.syncChart(ctx, entry, &signing, dryRun)
	if err != nil {
		return nil, err
	}
	result.Records = append(result.Records, rec)

	lock, err := spec.LoadImagesLock(entry.LockPath())
	if err != nil {
		return nil, fmt.Errorf("%s: no lock (run upgrade first): %w", entry.Name, err)
	}
	for _, img := range lock.Images {
		rec, err := e.syncImage(ctx, entry.Name, img.Source, img.Digest, img.Target, &signing, dryRun)
		if err != nil {
			return nil, err
		}
		result.Records = append(result.Records, rec)
	}
	return result, nil
}

// syncChart publishes the chart artifact: re-pull the upstream tgz into
// memory, trip on any upstream mutation (digest vs lock, tree vs committed
// vendor), push unless the tag exists, and ensure the mirror signature.
func (e *Engine) syncChart(ctx context.Context, entry spec.Entry, signing *spec.Signing,
	dryRun bool) (SyncRecord, error) {
	m := entry.Chart
	lock, err := spec.LoadImagesLock(entry.LockPath())
	if err != nil {
		return SyncRecord{}, fmt.Errorf("%s: no lock (run upgrade first): %w", entry.Name, err)
	}

	// The published artifact must be exactly what the PR reviewed: the
	// tripwire catches upstream mutating a released version in place.
	tgz, sha, err := e.puller.Pull(ctx, m.Chart.Repo, m.Chart.Name, m.Chart.Version)
	if err != nil {
		return SyncRecord{}, fmt.Errorf("%s: %w", entry.Name, err)
	}
	if sha != lock.Chart.UpstreamTgzSha256 {
		return SyncRecord{}, fmt.Errorf(
			"%s: upstream tgz digest mismatch: lock has %s, upstream now serves %s",
			entry.Name, lock.Chart.UpstreamTgzSha256, sha)
	}
	scratch, err := os.MkdirTemp("", "patchy-mirror-sync-*")
	if err != nil {
		return SyncRecord{}, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if err := helmchart.Extract(tgz, scratch); err != nil {
		return SyncRecord{}, fmt.Errorf("%s: %w", entry.Name, err)
	}
	diffs, err := helmchart.TreeDiff(
		filepath.Join(scratch, m.Chart.Name),
		filepath.Join(vendorDir(entry), m.Chart.Name))
	if err != nil {
		return SyncRecord{}, fmt.Errorf("%s: %w", entry.Name, err)
	}
	if len(diffs) > 0 {
		return SyncRecord{}, fmt.Errorf(
			"%s: committed vendor tree differs from the upstream chart (%s); re-run upgrade",
			entry.Name, strings.Join(diffs, "; "))
	}

	target := fmt.Sprintf("%s/%s:%s", e.global.Registry.URL, e.chartRepo(entry), m.Chart.Version)
	rec := SyncRecord{Ref: target}
	digest, exists, err := e.reg.Exists(ctx, target)
	if err != nil {
		return SyncRecord{}, fmt.Errorf("%s: %w", entry.Name, err)
	}
	switch {
	case exists:
		// An existing tag is never replaced.
		rec.Action = ActionSkippedTagExists
		rec.Digest = digest
		e.notef(entry.Name, "sync", "chart version already published (%s)", digest)
	case dryRun:
		rec.Action = ActionPushed
		rec.Signed = true
		e.notef(entry.Name, "sync", "would push and sign %s", target)
		return rec, nil
	default:
		e.notef(entry.Name, "sync", "pushing %s %s to %s", m.Chart.Name, m.Chart.Version, target)
		digest, err = e.pushChart(tgz, target)
		if err != nil {
			return SyncRecord{}, fmt.Errorf("%s: %w", entry.Name, err)
		}
		rec.Action = ActionPushed
		rec.Digest = digest
	}

	signed, err := e.ensureSigned(ctx, entry.Name, repoOf(target)+"@"+rec.Digest, signing, false, dryRun)
	if err != nil {
		return SyncRecord{}, err
	}
	rec.Signed = signed
	return rec, nil
}

// repoOf strips the tag from a tagged reference, tolerating registry
// ports.
func repoOf(ref string) string {
	base := strings.SplitN(ref, "@", 2)[0]
	if i := strings.LastIndex(base, ":"); i > strings.LastIndex(base, "/") {
		base = base[:i]
	}
	return base
}

// syncImage converges one image or artifact copy: skip when the target
// already matches the lock digest and carries a valid mirror signature;
// otherwise copy by digest, re-assert the digest, and sign.
func (e *Engine) syncImage(ctx context.Context, entryName, source, digest, target string,
	signing *spec.Signing, dryRun bool) (SyncRecord, error) {
	rec := SyncRecord{Ref: target, Digest: digest}
	srcRef, err := imageref.Parse(source)
	if err != nil {
		return SyncRecord{}, fmt.Errorf("%s: %w", entryName, err)
	}
	targetDigestRef := repoOf(target) + "@" + digest

	current, exists, err := e.reg.Exists(ctx, target)
	if err != nil {
		return SyncRecord{}, fmt.Errorf("%s: %w", entryName, err)
	}
	if exists && current == digest && e.mirrorSignatureValid(ctx, targetDigestRef, signing) {
		e.notef(entryName, "sync", "up to date: %s", target)
		rec.Action = ActionSkippedCurrent
		return rec, nil
	}

	rec.Action = ActionPushed
	if dryRun {
		e.notef(entryName, "sync", "would copy %s -> %s", source, target)
		return rec, nil
	}
	pullRef := e.rewrite(srcRef.Repository) + "@" + digest
	if exists && current != digest {
		e.warnf(entryName, "sync", "target %s moved off the locked digest; re-converging", target)
	}
	e.notef(entryName, "sync", "copying %s -> %s", source, target)
	if err := e.reg.Copy(ctx, pullRef, target); err != nil {
		return SyncRecord{}, fmt.Errorf("%s: %w", entryName, err)
	}
	after, err := e.reg.Digest(ctx, target)
	if err != nil {
		return SyncRecord{}, fmt.Errorf("%s: %w", entryName, err)
	}
	if after != digest {
		return SyncRecord{}, fmt.Errorf("%s: digest changed while copying %s (%s != %s)", entryName, source, after, digest)
	}
	signed, err := e.ensureSigned(ctx, entryName, targetDigestRef, signing, true, false)
	if err != nil {
		return SyncRecord{}, err
	}
	rec.Signed = signed
	return rec, nil
}

// mirrorSignatureValid reports whether ref already carries a valid mirror
// signature.
func (e *Engine) mirrorSignatureValid(ctx context.Context, ref string, signing *spec.Signing) bool {
	s, err := verify.MirrorSubject(ref, signing)
	if err != nil {
		return false
	}
	return e.verifyFn(ctx, s) == nil
}

// ensureSigned signs ref unless it already carries a valid mirror
// signature. Returns whether a signature was created.
func (e *Engine) ensureSigned(ctx context.Context, entryName, ref string,
	signing *spec.Signing, recursive, dryRun bool) (bool, error) {
	if dryRun {
		if !e.mirrorSignatureValid(ctx, ref, signing) {
			e.notef(entryName, "sync", "would sign %s", ref)
			return true, nil
		}
		return false, nil
	}
	if e.mirrorSignatureValid(ctx, ref, signing) {
		return false, nil
	}
	e.notef(entryName, "sync", "signing %s", ref)
	signer, err := e.newSigner(signing)
	if err != nil {
		return false, fmt.Errorf("%s: %w", entryName, err)
	}
	if err := signer.Sign(ctx, ref, recursive); err != nil {
		return false, fmt.Errorf("%s: %w", entryName, err)
	}
	return true, nil
}
