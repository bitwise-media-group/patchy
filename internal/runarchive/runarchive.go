// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package runarchive

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/archive"
	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/internal/transcriptstore"
)

// errUnarchivable marks a run that can never be archived, so retrying is
// pointless.
var errUnarchivable = errors.New("runarchive: run cannot be archived")

// ManifestVersion is the manifest.json schema version.
const ManifestVersion = 1

// Content types. Gzipped objects are stored as application/gzip with no
// Content-Encoding, so GCS never transcodes them and every reader gets the
// exact bytes the manifest digests.
const (
	contentGzip     = "application/gzip"
	contentJSON     = "application/json"
	contentMarkdown = "text/markdown; charset=utf-8"
	contentMbox     = "application/mbox"
)

// Run identifies the run being archived and what it ran against.
type Run struct {
	// Kind is investigation or remediation.
	Kind v1alpha1.RunKind
	// Object is the Investigation or Remediation.
	Object client.Object
	// Attempt ordinal of the run.
	Attempt int32
	// Finding the run belongs to; nil when it is already gone.
	Finding *v1alpha1.Finding
	// BaseSHA is the commit the agent's workspace was pinned to.
	BaseSHA string
	// FinishedAt is when the agent Job finished; zero when unknown.
	FinishedAt time.Time
}

// Archiver writes runs to an archive store.
type Archiver struct {
	// Store receives the objects.
	Store archive.Store
	// Config is where Store points (provider, bucket, prefix), recorded on
	// each reference so a reader needs no configuration of its own.
	Config archive.Config
	// Required fails a run whose archive cannot be written, instead of
	// falling back to the ConfigMap transcript.
	Required bool
	// RetryWindow bounds how long after the Job finished a failed upload is
	// retried. It must stay below the Job TTL, after which the log is gone.
	RetryWindow time.Duration
	// Now tells time; nil means time.Now.
	Now func() time.Time
}

// DefaultRetryWindow is the retry window when none is configured: well
// inside the default one-hour Job TTL.
const DefaultRetryWindow = 45 * time.Minute

// Manifest describes one archived run. It is written last; its presence
// means every object it lists is complete.
type Manifest struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Run     struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		UID       string `json:"uid"`
		Attempt   int32  `json:"attempt"`
		CreatedAt string `json:"createdAt,omitempty"`
	} `json:"run"`
	Finding    *ManifestFinding `json:"finding,omitempty"`
	BaseSHA    string           `json:"baseSHA,omitempty"`
	FinishedAt string           `json:"finishedAt,omitempty"`
	// Stage is the agent's own stage block, verbatim from the envelope.
	Stage *envelope.Stage `json:"stage,omitempty"`
	// Fatal carries a runner that could not produce a stage at all.
	Fatal      string `json:"fatal,omitempty"`
	Transcript *struct {
		Turns     int  `json:"turns"`
		Truncated bool `json:"truncated"`
	} `json:"transcript,omitempty"`
	// Objects digests every other object in the run's prefix.
	Objects []v1alpha1.ArchiveObject `json:"objects"`
}

// ManifestFinding is what the manifest records about the finding, so the
// archive explains itself after the Finding resource has expired.
type ManifestFinding struct {
	Name          string                         `json:"name"`
	UID           string                         `json:"uid,omitempty"`
	Source        string                         `json:"source,omitempty"`
	Integration   string                         `json:"integration,omitempty"`
	RuleID        string                         `json:"ruleID,omitempty"`
	Title         string                         `json:"title,omitempty"`
	Severity      v1alpha1.Level                 `json:"severity,omitempty"`
	Advisories    []string                       `json:"advisories,omitempty"`
	Repository    *v1alpha1.FindingRepository    `json:"repository,omitempty"`
	CloudResource *v1alpha1.FindingCloudResource `json:"cloudResource,omitempty"`
	Alerts        []v1alpha1.Alert               `json:"alerts,omitempty"`
}

// Push is push.json: what a remediation's archive produced on the forge.
type Push struct {
	Branch      string `json:"branch"`
	Commit      string `json:"commit,omitempty"`
	PullRequest struct {
		Number int64  `json:"number"`
		URL    string `json:"url"`
	} `json:"pullRequest"`
}

// object is one archive object before upload.
type object struct {
	name        string
	body        []byte
	contentType string
}

// Prefix is the run's key prefix.
func (a *Archiver) Prefix(run Run) string {
	finding := ""
	if run.Finding != nil {
		finding = run.Finding.Name
	}
	return a.Config.Key(run.Object.GetNamespace(), finding, run.Object.GetName(), string(run.Object.GetUID()))
}

// Archive uploads the run's objects, the manifest last, and returns the
// reference to record on the run. Every object is create-only, so calling it
// again for the same output is safe.
func (a *Archiver) Archive(ctx context.Context, run Run, out jobs.RunOutput) (*v1alpha1.ArchiveRef, error) {
	if run.Object.GetUID() == "" {
		// The UID is what keeps a reused run name from landing on an
		// earlier attempt's immutable prefix.
		return nil, fmt.Errorf("%w: run %s has no uid", errUnarchivable, run.Object.GetName())
	}
	objs, man, err := a.objects(run, out)
	if err != nil {
		return nil, err
	}
	prefix := a.Prefix(run)
	ref := &v1alpha1.ArchiveRef{
		Provider: v1alpha1.ArchiveProvider(a.Config.Provider),
		Bucket:   a.Config.Bucket,
		Region:   a.Config.Region,
		Endpoint: a.Config.Endpoint,
		Prefix:   prefix,
	}
	for _, o := range objs {
		key := archive.Join(prefix, o.name)
		if err := a.Store.Put(ctx, key, o.body, o.contentType); err != nil {
			return nil, err
		}
		ref.Objects = append(ref.Objects, v1alpha1.ArchiveObject{
			Name: o.name, Key: key, SHA256: archive.SHA256(o.body), Size: int64(len(o.body)),
		})
	}
	man.Objects = ref.Objects
	body, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("runarchive: manifest: %w", err)
	}
	key := archive.Join(prefix, v1alpha1.ArchiveObjectManifest)
	if err := a.Store.Put(ctx, key, body, contentJSON); err != nil {
		return nil, err
	}
	ref.Objects = append(ref.Objects, v1alpha1.ArchiveObject{
		Name: v1alpha1.ArchiveObjectManifest, Key: key, SHA256: archive.SHA256(body), Size: int64(len(body)),
	})
	ref.ArchivedAt = metav1.NewTime(a.now())
	return ref, nil
}

// RecordPush writes push.json beside an archived remediation, linking the
// archive to the branch and pull request it became.
func (a *Archiver) RecordPush(ctx context.Context, ref *v1alpha1.ArchiveRef, p Push) error {
	if ref == nil {
		return nil
	}
	body, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("runarchive: push record: %w", err)
	}
	return a.Store.Put(ctx, archive.Join(ref.Prefix, v1alpha1.ArchiveObjectPush), body, contentJSON)
}

// objects builds every object but the manifest, plus the manifest skeleton.
func (a *Archiver) objects(run Run, out jobs.RunOutput) ([]object, *Manifest, error) {
	man := &Manifest{Version: ManifestVersion, Kind: string(run.Kind), BaseSHA: run.BaseSHA}
	man.Run.Namespace = run.Object.GetNamespace()
	man.Run.Name = run.Object.GetName()
	man.Run.UID = string(run.Object.GetUID())
	man.Run.Attempt = run.Attempt
	if ts := run.Object.GetCreationTimestamp(); !ts.IsZero() {
		man.Run.CreatedAt = ts.UTC().Format(time.RFC3339)
	}
	if !run.FinishedAt.IsZero() {
		man.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339)
	}
	if f := run.Finding; f != nil {
		man.Finding = &ManifestFinding{
			Name: f.Name, UID: string(f.UID), Source: f.Spec.Source,
			Integration: f.Spec.IntegrationRef.Name, RuleID: f.Spec.RuleID, Title: f.Spec.Title,
			Severity: f.Spec.Severity, Advisories: f.Spec.Advisories, Repository: f.Spec.Repository,
			CloudResource: f.Spec.CloudResource, Alerts: f.Spec.Alerts,
		}
	}

	var objs []object
	if len(out.Turns) > 0 {
		raw, err := transcriptstore.Marshal(out.Turns)
		if err != nil {
			return nil, nil, err
		}
		objs = append(objs, object{v1alpha1.ArchiveObjectTranscript, raw, contentGzip})
		man.Transcript = &struct {
			Turns     int  `json:"turns"`
			Truncated bool `json:"truncated"`
		}{len(out.Turns), transcript.Truncated(out.Turns)}
	}

	var report string
	var cs *envelope.Changeset
	for _, e := range out.Events {
		switch e.Type {
		case envelope.TypeInvestigation:
			if e.Investigation != nil {
				report, man.Stage = e.Investigation.ReportMarkdown, &e.Investigation.Stage
			}
		case envelope.TypeRemediation:
			if e.Remediation != nil {
				report, man.Stage = e.Remediation.ReportMarkdown, &e.Remediation.Stage
				cs = e.Remediation.Changeset
			}
		case envelope.TypeFatal:
			man.Fatal = e.Error
		}
	}
	if report != "" {
		objs = append(objs, object{v1alpha1.ArchiveObjectReport, []byte(report), contentMarkdown})
	}
	if cs != nil {
		if cs.Patch != "" {
			objs = append(objs, object{v1alpha1.ArchiveObjectPatch, []byte(cs.Patch), contentMbox})
		}
		raw, err := gzipJSON(cs)
		if err != nil {
			return nil, nil, err
		}
		objs = append(objs, object{v1alpha1.ArchiveObjectChangeset, raw, contentGzip})
	}
	return objs, man, nil
}

// gzipJSON renders v as gzipped JSON. The gzip header carries no name or
// time, so equal input always gives equal bytes.
func gzipJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(v); err != nil {
		return nil, fmt.Errorf("runarchive: encode: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("runarchive: encode: %w", err)
	}
	return buf.Bytes(), nil
}

func (a *Archiver) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}
	return a.Now()
}

func (a *Archiver) retryWindow() time.Duration {
	if a.RetryWindow <= 0 {
		return DefaultRetryWindow
	}
	return a.RetryWindow
}

// retryable reports whether a failed upload is worth trying again: not when
// the store says the object differs (that never heals), and not once the
// window since the Job finished has closed.
func (a *Archiver) retryable(err error, finished time.Time) (bool, time.Duration) {
	if errors.Is(err, archive.ErrConflict) || errors.Is(err, errUnarchivable) || finished.IsZero() {
		return false, 0
	}
	left := finished.Add(a.retryWindow()).Sub(a.now())
	if left <= 0 {
		return false, 0
	}
	// Back off with the age of the failure, but always land one attempt at
	// the window's close rather than overshooting it towards the Job TTL.
	step := min(max(a.now().Sub(finished)/4, 15*time.Second), 5*time.Minute)
	return true, min(step, left)
}
