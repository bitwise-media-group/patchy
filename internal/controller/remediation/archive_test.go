// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package remediation

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/archive"
	"github.com/bitwise-media-group/patchy/internal/archive/archivetest"
	"github.com/bitwise-media-group/patchy/internal/runarchive"
)

const remKey = "finding-aa-1-rem-1"

// archivingRemediation is a remediation reconciler with an in-memory archive.
func archivingRemediation(
	t *testing.T, runner *fakeCRRunner, fw *fakeForge, required bool,
) (*RemediationReconciler, *archivetest.Store, func() (v1alpha1.Remediation, v1alpha1.Finding, bool)) {
	t.Helper()
	objs := runningRemediation()
	for _, o := range objs {
		if o.GetName() == remKey {
			o.SetUID("rem-uid")
		}
	}
	r, c := newRemediation(t, runner, fw, objs...)
	store := archivetest.New()
	r.Archive = &runarchive.Archiver{
		Store:       store,
		Config:      archive.Config{Provider: archive.ProviderS3, Bucket: "audit", Prefix: "patchy"},
		Required:    required,
		RetryWindow: 45 * time.Minute,
		Now:         func() time.Time { return crdClock },
	}
	read := func() (v1alpha1.Remediation, v1alpha1.Finding, bool) {
		t.Helper()
		var rem v1alpha1.Remediation
		var fnd v1alpha1.Finding
		if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: remKey}, &rem); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"}, &fnd); err != nil {
			t.Fatal(err)
		}
		var cm corev1.ConfigMap
		err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: remKey + "-transcript"}, &cm)
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		return rem, fnd, err == nil
	}
	return r, store, read
}

func patchedEvent() *fakeCRRunner {
	events := crdRemediationEvent(true)
	events[0].Remediation.Changeset.Patch = "From abc Mon Sep 17 00:00:00 2001\nSubject: [PATCH] fix\n"
	return &fakeCRRunner{done: true, finishedAt: crdClock.Add(-time.Minute), events: events, turns: remTurns()}
}

func TestRemediationArchiveSuccess(t *testing.T) {
	fw := &fakeForge{}
	r, store, read := archivingRemediation(t, patchedEvent(), fw, false)
	remOnce(t, r, remKey)

	rem, fnd, hasCM := read()
	if hasCM {
		t.Error("transcript ConfigMap written, want the archive only")
	}
	ref := rem.Status.Stage.Archive
	if ref == nil || ref.Bucket != "audit" || ref.Provider != v1alpha1.ArchiveProviderS3 ||
		ref.Prefix != "patchy/patchy/finding-aa-1/"+remKey+"/rem-uid" {
		t.Fatalf("archive ref = %+v", ref)
	}
	names := make([]string, 0, len(ref.Objects))
	for _, o := range ref.Objects {
		names = append(names, o.Name)
	}
	want := []string{"transcript.jsonl.gz", "report.md", "patch.mbox", "changeset.json.gz", "manifest.json"}
	if !slices.Equal(names, want) {
		t.Errorf("objects = %v, want %v (manifest last)", names, want)
	}
	if tr := rem.Status.Stage.Transcript; tr == nil || tr.Object != "transcript.jsonl.gz" || tr.Name != "" ||
		tr.Turns != 2 {
		t.Errorf("transcript ref = %+v", tr)
	}
	cond := meta.FindStatusCondition(rem.Status.Conditions, v1alpha1.ConditionArchived)
	if cond == nil || cond.Status != "True" || cond.Reason != v1alpha1.ReasonArchived {
		t.Errorf("Archived condition = %+v", cond)
	}

	// The push happened after archival, and its record links the two.
	if len(fw.pushed) != 1 || rem.Status.PushedCommit != "c0ffee" || fnd.Status.Phase != v1alpha1.PhaseInReview {
		t.Errorf("pushed=%v commit=%q phase=%s", fw.pushed, rem.Status.PushedCommit, fnd.Status.Phase)
	}
	checkPushRecord(t, store, ref.Prefix)
}

// checkPushRecord asserts push.json links the archive to the pushed commit
// and PR.
func checkPushRecord(t *testing.T, store *archivetest.Store, prefix string) {
	t.Helper()
	obj, ok := store.Object(prefix + "/push.json")
	if !ok {
		t.Fatalf("push.json missing; keys %v", store.Keys())
	}
	var push runarchive.Push
	if err := json.Unmarshal(obj.Body, &push); err != nil || push.Commit != "c0ffee" || push.PullRequest.Number != 11 {
		t.Errorf("push.json = %s (%v)", obj.Body, err)
	}
}

func TestRemediationArchiveTransientFailureRequeues(t *testing.T) {
	fw := &fakeForge{}
	r, store, read := archivingRemediation(t, patchedEvent(), fw, false)
	store.PutErr = errors.New("connection refused")

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "patchy", Name: remKey}}
	res, err := r.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want a quiet requeue", err)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > 45*time.Minute {
		t.Errorf("RequeueAfter = %s, want a retry inside the window", res.RequeueAfter)
	}
	rem, fnd, hasCM := read()
	if hasCM || rem.Status.Phase != v1alpha1.RunRunning || fnd.Status.Phase != v1alpha1.PhaseRemediating ||
		len(fw.pushed) != 0 {
		t.Errorf("a deferred pass changed state: cm=%v run=%s finding=%s pushed=%v",
			hasCM, rem.Status.Phase, fnd.Status.Phase, fw.pushed)
	}

	// The store recovers: the next pass archives and carries on.
	store.PutErr = nil
	remOnce(t, r, remKey)
	if rem, _, _ := read(); rem.Status.Stage.Archive == nil || len(fw.pushed) != 1 {
		t.Errorf("recovered pass: archive=%v pushed=%v", rem.Status.Stage.Archive, fw.pushed)
	}
}

func TestRemediationArchiveWindowExceededFailsOpen(t *testing.T) {
	fw := &fakeForge{}
	runner := patchedEvent()
	runner.finishedAt = crdClock.Add(-time.Hour)
	r, store, read := archivingRemediation(t, runner, fw, false)
	store.PutErr = errors.New("access denied")
	remOnce(t, r, remKey)

	rem, fnd, hasCM := read()
	if !hasCM || rem.Status.Stage.Transcript == nil || rem.Status.Stage.Transcript.Name == "" {
		t.Errorf("fail-open did not fall back to the ConfigMap: cm=%v ref=%+v", hasCM, rem.Status.Stage.Transcript)
	}
	cond := meta.FindStatusCondition(rem.Status.Conditions, v1alpha1.ConditionArchived)
	if cond == nil || cond.Status != "False" || cond.Reason != v1alpha1.ReasonArchiveFailed {
		t.Errorf("Archived condition = %+v", cond)
	}
	if len(fw.pushed) != 1 || fnd.Status.Phase != v1alpha1.PhaseInReview {
		t.Errorf("fail-open must still push: pushed=%v phase=%s", fw.pushed, fnd.Status.Phase)
	}
}

func TestRemediationArchiveRequiredFailsClosed(t *testing.T) {
	fw := &fakeForge{}
	runner := patchedEvent()
	runner.finishedAt = crdClock.Add(-time.Hour)
	r, store, read := archivingRemediation(t, runner, fw, true)
	store.PutErr = errors.New("access denied")
	remOnce(t, r, remKey)

	rem, fnd, hasCM := read()
	if len(fw.pushed) != 0 || fw.prCalls != 0 {
		t.Errorf("fail-closed reached the forge: pushed=%v prs=%d", fw.pushed, fw.prCalls)
	}
	if rem.Status.Phase != v1alpha1.RunFailed || rem.Status.Stage.Outcome != runarchive.OutcomeArchiveFailed {
		t.Errorf("run = %s/%s, want Failed/archive_failed", rem.Status.Phase, rem.Status.Stage.Outcome)
	}
	// Attempt 1 of 2, yet straight to Failed: another attempt would not fix
	// the bucket.
	if fnd.Status.Phase != v1alpha1.PhaseFailed {
		t.Errorf("finding phase = %s, want Failed", fnd.Status.Phase)
	}
	if !hasCM {
		t.Error("fail-closed run lost its transcript")
	}
	if rem.Status.Stage.Usage.CostUSD == "" {
		t.Error("fail-closed run lost the agent's accounting")
	}
}
