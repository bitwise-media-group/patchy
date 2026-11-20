// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package investigation

import (
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/archive"
	"github.com/bitwise-media-group/patchy/internal/archive/archivetest"
	"github.com/bitwise-media-group/patchy/internal/runarchive"
)

func archivingInvestigation(t *testing.T, runner *fakeRunner, required bool) (
	*InvestigationReconciler, *archivetest.Store, func() (v1alpha1.Investigation, v1alpha1.Finding, bool),
) {
	t.Helper()
	objs := investigationFixture()
	objs[1].SetUID("inv-uid")
	r, c := newInvestigation(t, runner, objs...)
	store := archivetest.New()
	r.Archive = &runarchive.Archiver{
		Store:    store,
		Config:   archive.Config{Provider: archive.ProviderGCS, Bucket: "audit"},
		Required: required,
		Now:      func() time.Time { return clock },
	}
	read := func() (v1alpha1.Investigation, v1alpha1.Finding, bool) {
		t.Helper()
		var inv v1alpha1.Investigation
		key := types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-1"}
		if err := c.Get(t.Context(), key, &inv); err != nil {
			t.Fatal(err)
		}
		var cm corev1.ConfigMap
		key.Name += "-transcript"
		hasCM := c.Get(t.Context(), key, &cm) == nil
		return inv, *getF(t, c), hasCM
	}
	return r, store, read
}

func TestInvestigationArchiveSuccess(t *testing.T) {
	runner := &fakeRunner{
		done: true, finishedAt: clock, events: investigationEvent("remediate", 0.9, false), turns: sampleTurns(),
	}
	r, store, read := archivingInvestigation(t, runner, false)
	applyOnce(t, r)

	inv, fnd, hasCM := read()
	if hasCM {
		t.Error("transcript ConfigMap written, want the archive only")
	}
	ref := inv.Status.Stage.Archive
	if ref == nil || !strings.HasSuffix(ref.Prefix, "finding-aa-1/finding-aa-1-inv-1/inv-uid") {
		t.Fatalf("archive ref = %+v", ref)
	}
	if _, ok := ref.Object(v1alpha1.ArchiveObjectPatch); ok {
		t.Error("an investigation archived a patch")
	}
	report, ok := store.Object(ref.Prefix + "/report.md")
	if !ok || string(report.Body) != "---\nfrontmatter\n---\nanalysis" {
		t.Errorf("report.md = %q, want the full raw report", report.Body)
	}
	// The CR keeps the final report too.
	if inv.Status.Report == "" || fnd.Status.Phase != v1alpha1.PhaseQueued {
		t.Errorf("report=%q phase=%s", inv.Status.Report, fnd.Status.Phase)
	}
	if c := meta.FindStatusCondition(inv.Status.Conditions, v1alpha1.ConditionArchived); c == nil || c.Status != "True" {
		t.Errorf("Archived condition = %+v", c)
	}
}

func TestInvestigationArchiveRequiredFailsClosed(t *testing.T) {
	runner := &fakeRunner{
		done: true, finishedAt: clock.Add(-time.Hour),
		events: investigationEvent("remediate", 0.9, false), turns: sampleTurns(),
	}
	r, store, read := archivingInvestigation(t, runner, true)
	store.PutErr = errors.New("access denied")
	applyOnce(t, r)

	inv, fnd, hasCM := read()
	if inv.Status.Phase != v1alpha1.RunFailed || inv.Status.Stage.Outcome != runarchive.OutcomeArchiveFailed {
		t.Errorf("run = %s/%s, want Failed/archive_failed", inv.Status.Phase, inv.Status.Stage.Outcome)
	}
	if fnd.Status.Phase != v1alpha1.PhaseFailed || fnd.Status.Investigation != nil {
		t.Errorf("finding phase=%s investigation=%+v, want Failed with no verdict routed",
			fnd.Status.Phase, fnd.Status.Investigation)
	}
	if !hasCM {
		t.Error("fail-closed run lost its transcript")
	}
}

func TestInvestigationWithoutArchiveSetsNoCondition(t *testing.T) {
	runner := &fakeRunner{done: true, events: investigationEvent("ignore", 0.9, false), turns: sampleTurns()}
	r, c := newInvestigation(t, runner, investigationFixture()...)
	applyOnce(t, r)
	var inv v1alpha1.Investigation
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-1"}, &inv); err != nil {
		t.Fatal(err)
	}
	if meta.FindStatusCondition(inv.Status.Conditions, v1alpha1.ConditionArchived) != nil {
		t.Error("Archived condition set with archival off")
	}
}
