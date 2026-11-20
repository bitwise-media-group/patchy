// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package runarchive

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/archive"
	"github.com/bitwise-media-group/patchy/internal/archive/archivetest"
	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/internal/transcriptstore"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func testRun() Run {
	rem := &v1alpha1.Remediation{ObjectMeta: metav1.ObjectMeta{
		Namespace: "patchy", Name: "f-rem-2", UID: types.UID("uid-2"),
		CreationTimestamp: metav1.NewTime(now.Add(-time.Hour)),
	}}
	fnd := &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: "f", UID: "f-uid"},
		Spec:       v1alpha1.FindingSpec{Source: "github-code-scanning", RuleID: "js/xss", Title: "XSS"},
	}
	return Run{Kind: v1alpha1.RunKindRemediation, Object: rem, Attempt: 2, Finding: fnd,
		BaseSHA: "abc123", FinishedAt: now.Add(-time.Minute)}
}

// longTurns has a turn far beyond the ConfigMap store's per-turn bound, so a
// compacted record would visibly differ.
func longTurns() []transcript.Turn {
	return []transcript.Turn{
		{Seq: 1, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: strings.Repeat("a", 10<<10)},
		{Seq: 2, Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Edit", Text: "x.go"},
	}
}

func testOutput() jobs.RunOutput {
	return jobs.RunOutput{
		Turns: longTurns(),
		Events: []envelope.Event{{Type: envelope.TypeRemediation, Remediation: &envelope.Remediation{
			Stage:          envelope.Stage{Outcome: envelope.OutcomeOK, Harness: "claude", Model: "m"},
			ReportMarkdown: "---\nconfidence: 0.9\n---\n# Fix\n" + strings.Repeat("r", 80<<10),
			Success:        true,
			Changeset: &envelope.Changeset{BaseSHA: "abc123", CommitMessage: "fix",
				Upserts: []envelope.FileChange{{Path: "x.go", Mode: "100644", ContentB64: "eA=="}},
				Patch:   "From 1 Mon Sep 17 00:00:00 2001\n"},
		}}},
	}
}

func newArchiver(store archive.Store) *Archiver {
	return &Archiver{
		Store:  store,
		Config: archive.Config{Provider: archive.ProviderS3, Bucket: "b", Prefix: "audit"},
		Now:    func() time.Time { return now },
	}
}

func TestArchiveObjects(t *testing.T) {
	store := archivetest.New()
	a := newArchiver(store)
	ref, err := a.Archive(t.Context(), testRun(), testOutput())
	if err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	if ref.Prefix != "audit/patchy/f/f-rem-2/uid-2" {
		t.Errorf("Prefix = %q", ref.Prefix)
	}
	want := []string{"transcript.jsonl.gz", "report.md", "patch.mbox", "changeset.json.gz", "manifest.json"}
	if got := store.Keys(); len(got) != len(want) || !strings.HasSuffix(got[len(got)-1], "/manifest.json") {
		t.Fatalf("write order = %v, want %v with the manifest last", got, want)
	}
	checkTranscriptObject(t, store, ref.Prefix)
	checkReportAndChangeset(t, store, ref.Prefix)
	checkManifest(t, store, ref)
}

// checkTranscriptObject asserts the transcript is every turn, uncompacted.
func checkTranscriptObject(t *testing.T, store *archivetest.Store, prefix string) {
	t.Helper()
	obj, _ := store.Object(prefix + "/transcript.jsonl.gz")
	if obj.ContentType != "application/gzip" {
		t.Errorf("transcript content type = %q", obj.ContentType)
	}
	turns, err := transcriptstore.Unmarshal(obj.Body)
	if err != nil || len(turns) != 2 {
		t.Fatalf("archived turns = %d (%v), want 2", len(turns), err)
	}
	if len(turns[0].Text) != 10<<10 || turns[0].Truncated {
		t.Errorf("archived turns were compacted: first is %d bytes", len(turns[0].Text))
	}
}

// checkReportAndChangeset asserts the full raw report and the pushed
// changeset are archived as they were.
func checkReportAndChangeset(t *testing.T, store *archivetest.Store, prefix string) {
	t.Helper()
	if obj, _ := store.Object(prefix + "/report.md"); !bytes.HasPrefix(obj.Body, []byte("---\nconfidence")) ||
		len(obj.Body) < 80<<10 {
		t.Errorf("report.md = %d bytes, want the full raw report", len(obj.Body))
	}
	obj, _ := store.Object(prefix + "/changeset.json.gz")
	zr, err := gzip.NewReader(bytes.NewReader(obj.Body))
	if err != nil {
		t.Fatal(err)
	}
	var cs envelope.Changeset
	raw, _ := io.ReadAll(zr)
	if err := json.Unmarshal(raw, &cs); err != nil || len(cs.Upserts) != 1 || cs.Upserts[0].Path != "x.go" {
		t.Errorf("changeset = %+v (%v)", cs, err)
	}
}

// checkManifest asserts the manifest describes the run and digests every
// other object exactly as stored.
func checkManifest(t *testing.T, store *archivetest.Store, ref *v1alpha1.ArchiveRef) {
	t.Helper()
	obj, _ := store.Object(ref.Prefix + "/manifest.json")
	var man Manifest
	if err := json.Unmarshal(obj.Body, &man); err != nil {
		t.Fatal(err)
	}
	if man.Run.UID != "uid-2" || man.Run.Attempt != 2 || man.BaseSHA != "abc123" || man.Finding.RuleID != "js/xss" ||
		man.Stage == nil || man.Stage.Harness != "claude" || man.Transcript.Turns != 2 {
		t.Errorf("manifest = %s", obj.Body)
	}
	if len(man.Objects) != 4 {
		t.Fatalf("manifest objects = %+v", man.Objects)
	}
	for _, o := range man.Objects {
		stored, _ := store.Object(o.Key)
		if archive.SHA256(stored.Body) != o.SHA256 || int64(len(stored.Body)) != o.Size {
			t.Errorf("manifest entry %s does not match the stored bytes", o.Name)
		}
	}
	last := ref.Objects[len(ref.Objects)-1]
	if last.Name != "manifest.json" || last.SHA256 != archive.SHA256(obj.Body) {
		t.Errorf("ref's manifest entry = %+v", last)
	}
}

func TestArchiveIsIdempotent(t *testing.T) {
	store := archivetest.New()
	a := newArchiver(store)
	first, err := a.Archive(t.Context(), testRun(), testOutput())
	if err != nil {
		t.Fatal(err)
	}
	// A later pass, at a later time, rewrites byte-identical objects: the
	// create-only store accepts it rather than reporting a conflict.
	a.Now = func() time.Time { return now.Add(10 * time.Minute) }
	second, err := a.Archive(t.Context(), testRun(), testOutput())
	if err != nil {
		t.Fatalf("second Archive() error = %v, want an idempotent retry", err)
	}
	for i := range first.Objects {
		if first.Objects[i] != second.Objects[i] {
			t.Errorf("object %d differs between passes: %+v vs %+v", i, first.Objects[i], second.Objects[i])
		}
	}
}

func TestArchiveRefusesRunWithoutUID(t *testing.T) {
	run := testRun()
	run.Object.SetUID("")
	a := newArchiver(archivetest.New())
	_, err := a.Archive(t.Context(), run, testOutput())
	if err == nil {
		t.Fatal("Archive() error = nil, want a refusal")
	}
	if retry, _ := a.retryable(err, now); retry {
		t.Error("a run without a uid was deemed retryable")
	}
}

func TestRecordPush(t *testing.T) {
	store := archivetest.New()
	a := newArchiver(store)
	ref := &v1alpha1.ArchiveRef{Prefix: "audit/x"}
	p := Push{Branch: "patchy/f", Commit: "c0ffee"}
	p.PullRequest.Number, p.PullRequest.URL = 7, "https://example/pr/7"
	if err := a.RecordPush(t.Context(), ref, p); err != nil {
		t.Fatal(err)
	}
	obj, ok := store.Object("audit/x/push.json")
	if !ok || !strings.Contains(string(obj.Body), `"commit": "c0ffee"`) {
		t.Errorf("push.json = %s", obj.Body)
	}
	if err := a.RecordPush(t.Context(), nil, p); err != nil {
		t.Errorf("RecordPush(nil ref) = %v, want a no-op", err)
	}
}

func TestRetryable(t *testing.T) {
	a := &Archiver{RetryWindow: 45 * time.Minute, Now: func() time.Time { return now }}
	transient := errors.New("timeout")
	tests := []struct {
		name     string
		err      error
		finished time.Time
		want     bool
		maxAfter time.Duration
	}{
		{name: "fresh failure", err: transient, finished: now.Add(-time.Minute), want: true, maxAfter: time.Minute},
		{name: "older failure backs off", err: transient, finished: now.Add(-30 * time.Minute), want: true,
			maxAfter: 5 * time.Minute},
		{name: "lands on the window close", err: transient, finished: now.Add(-44 * time.Minute), want: true,
			maxAfter: time.Minute},
		{name: "window closed", err: transient, finished: now.Add(-45 * time.Minute)},
		{name: "conflict never heals", err: archive.ErrConflict, finished: now},
		{name: "unknown finish time", err: transient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, after := a.retryable(tt.err, tt.finished)
			if got != tt.want {
				t.Fatalf("retryable() = %v, want %v", got, tt.want)
			}
			if got && (after <= 0 || after > tt.maxAfter) {
				t.Errorf("after = %s, want (0, %s]", after, tt.maxAfter)
			}
		})
	}
}

func TestCollectWithoutArchiverPersistsConfigMap(t *testing.T) {
	run := testRun()
	c := fake.NewClientBuilder().WithScheme(kube.Scheme()).WithObjects(run.Object).Build()
	col := &Collector{Client: c}
	rec, retry := col.Collect(t.Context(), run, testOutput(), Target{
		GVK: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Remediation"},
	})
	if retry != 0 || rec.Archive != nil || rec.Condition != nil {
		t.Fatalf("rec = %+v retry = %s", rec, retry)
	}
	if rec.Transcript == nil || rec.Transcript.Name != "f-rem-2-transcript" {
		t.Errorf("transcript = %+v", rec.Transcript)
	}
	var cm corev1.ConfigMap
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "f-rem-2-transcript"}, &cm); err != nil {
		t.Errorf("ConfigMap not written: %v", err)
	}
}

func TestRecordApply(t *testing.T) {
	var stage v1alpha1.StageResult
	var conds []metav1.Condition
	(*Record)(nil).Apply(&stage, &conds, 1)
	if stage.Transcript != nil || len(conds) != 0 {
		t.Error("nil record stamped something")
	}
	rec := &Record{
		Archive:   &v1alpha1.ArchiveRef{Prefix: "p"},
		Condition: &metav1.Condition{Type: v1alpha1.ConditionArchived, Status: metav1.ConditionTrue, Reason: "Archived"},
	}
	rec.Apply(&stage, &conds, 3)
	if stage.Archive == nil || len(conds) != 1 || conds[0].ObservedGeneration != 3 {
		t.Errorf("stage=%+v conds=%+v", stage, conds)
	}
}
