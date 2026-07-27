// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package remediation

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/internal/transcriptstore"
)

func remTurns() []transcript.Turn {
	return []transcript.Turn{
		{Seq: 1, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Escaping the sink."},
		{Seq: 2, Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Edit", Text: "app.js"},
	}
}

func TestRemediationCollectPersistsTranscript(t *testing.T) {
	runner := &fakeCRRunner{done: true, events: crdRemediationEvent(true), turns: remTurns()}
	r, c := newRemediation(t, runner, &fakeForge{}, runningRemediation()...)
	remOnce(t, r, "finding-aa-1-rem-1")

	var cm corev1.ConfigMap
	key := types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-rem-1-transcript"}
	if err := c.Get(t.Context(), key, &cm); err != nil {
		t.Fatalf("transcript not written: %v", err)
	}
	got, err := transcriptstore.FromConfigMap(&cm)
	if err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	if len(got) != 2 || got[1].Tool != "Edit" {
		t.Errorf("stored turns = %+v", got)
	}

	// Owned by the run, which is owned by the finding: TTL cleans both up.
	if len(cm.OwnerReferences) != 1 {
		t.Fatalf("got %d owner refs, want 1", len(cm.OwnerReferences))
	}
	owner := cm.OwnerReferences[0]
	if owner.Kind != "Remediation" || owner.Name != "finding-aa-1-rem-1" {
		t.Errorf("ownerRef = %+v", owner)
	}
	if owner.Controller == nil || !*owner.Controller {
		t.Error("ownerRef Controller = false, want true so deletion cascades")
	}
	if cm.Labels[v1alpha1.LabelRunKind] != string(v1alpha1.RunKindRemediation) {
		t.Errorf("labels = %v", cm.Labels)
	}

	var rem v1alpha1.Remediation
	if err := c.Get(t.Context(), types.NamespacedName{
		Namespace: "patchy", Name: "finding-aa-1-rem-1"}, &rem); err != nil {
		t.Fatal(err)
	}
	ref := rem.Status.Stage.Transcript
	if ref == nil || ref.Name != cm.Name || ref.Turns != 2 {
		t.Errorf("ref = %+v", ref)
	}
}

func TestRemediationHandOffKeepsItsTranscript(t *testing.T) {
	// An unsuccessful run hands off to a human, who needs to see what was tried.
	runner := &fakeCRRunner{done: true, events: crdRemediationEvent(false), turns: remTurns()}
	r, c := newRemediation(t, runner, &fakeForge{}, runningRemediation()...)
	remOnce(t, r, "finding-aa-1-rem-1")

	var rem v1alpha1.Remediation
	if err := c.Get(t.Context(), types.NamespacedName{
		Namespace: "patchy", Name: "finding-aa-1-rem-1"}, &rem); err != nil {
		t.Fatal(err)
	}
	if rem.Status.Stage.Transcript == nil {
		t.Fatal("handed-off run lost its transcript reference")
	}

	var cm corev1.ConfigMap
	key := types.NamespacedName{Namespace: "patchy", Name: rem.Status.Stage.Transcript.Name}
	if err := c.Get(t.Context(), key, &cm); err != nil {
		t.Fatalf("transcript not written: %v", err)
	}
}

func TestRemediationWithoutTurnsWritesNoTranscript(t *testing.T) {
	runner := &fakeCRRunner{done: true, events: crdRemediationEvent(true)}
	r, c := newRemediation(t, runner, &fakeForge{}, runningRemediation()...)
	remOnce(t, r, "finding-aa-1-rem-1")

	var list corev1.ConfigMapList
	if err := c.List(t.Context(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("wrote %d ConfigMaps, want 0", len(list.Items))
	}
}
