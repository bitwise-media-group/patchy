// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package investigation

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/internal/transcriptstore"
)

func sampleTurns() []transcript.Turn {
	return []transcript.Turn{
		{Seq: 1, Role: transcript.RoleSystem, Kind: transcript.KindNotice, Text: "session s1 started"},
		{Seq: 2, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Reading the sink."},
		{Seq: 3, Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Read", Text: "app.js"},
	}
}

func getTranscript(t *testing.T, c client.Client, name string) *corev1.ConfigMap {
	t.Helper()
	var cm corev1.ConfigMap
	key := types.NamespacedName{Namespace: "patchy", Name: name}
	if err := c.Get(t.Context(), key, &cm); err != nil {
		t.Fatalf("transcript %s not written: %v", name, err)
	}
	return &cm
}

func TestCollectPersistsTranscript(t *testing.T) {
	runner := &fakeRunner{
		done:   true,
		events: investigationEvent("remediate", 0.9, false),
		turns:  sampleTurns(),
	}
	r, c := newInvestigation(t, runner, investigationFixture()...)
	applyOnce(t, r)

	cm := getTranscript(t, c, "finding-aa-1-inv-1-transcript")
	got, err := transcriptstore.FromConfigMap(cm)
	if err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	if len(got) != 3 || got[2].Tool != "Read" {
		t.Errorf("stored turns = %+v", got)
	}

	// The status points at it, so the projection needs no extra lookup.
	var inv v1alpha1.Investigation
	key := types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-1"}
	if err := c.Get(t.Context(), key, &inv); err != nil {
		t.Fatal(err)
	}
	ref := inv.Status.Stage.Transcript
	if ref == nil {
		t.Fatal("status.stage.transcript is nil, want the reference")
	}
	if ref.Name != cm.Name || ref.Turns != 3 || ref.Truncated {
		t.Errorf("ref = %+v", ref)
	}
}

func TestCollectTranscriptOwnedByTheRun(t *testing.T) {
	// The owner reference is the retention design: Finding → run → transcript.
	// Without it the ConfigMap outlives the finding it explains.
	runner := &fakeRunner{done: true, events: investigationEvent("remediate", 0.9, false), turns: sampleTurns()}
	r, c := newInvestigation(t, runner, investigationFixture()...)
	applyOnce(t, r)

	cm := getTranscript(t, c, "finding-aa-1-inv-1-transcript")
	if len(cm.OwnerReferences) != 1 {
		t.Fatalf("got %d owner refs, want 1", len(cm.OwnerReferences))
	}
	owner := cm.OwnerReferences[0]
	if owner.Kind != "Investigation" || owner.Name != "finding-aa-1-inv-1" {
		t.Errorf("ownerRef = %+v, want the Investigation that produced it", owner)
	}
	if owner.Controller == nil || !*owner.Controller {
		t.Error("ownerRef Controller = false, want true so deletion cascades")
	}
	if cm.Labels[v1alpha1.LabelFinding] != "finding-aa-1" ||
		cm.Labels[v1alpha1.LabelRunKind] != string(v1alpha1.RunKindInvestigation) ||
		cm.Labels[v1alpha1.LabelAttempt] != "1" {
		t.Errorf("labels = %v", cm.Labels)
	}
}

func TestCollectWithoutTurnsWritesNoTranscript(t *testing.T) {
	// A harness that cannot transcribe must not fail the collect or leave an
	// empty object behind.
	runner := &fakeRunner{done: true, events: investigationEvent("remediate", 0.9, false)}
	r, c := newInvestigation(t, runner, investigationFixture()...)
	applyOnce(t, r)

	var list corev1.ConfigMapList
	if err := c.List(t.Context(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("wrote %d ConfigMaps, want 0", len(list.Items))
	}

	var inv v1alpha1.Investigation
	key := types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-1"}
	if err := c.Get(t.Context(), key, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Status.Stage == nil {
		t.Fatal("stage not stamped")
	}
	if inv.Status.Stage.Transcript != nil {
		t.Errorf("transcript ref = %+v, want nil", inv.Status.Stage.Transcript)
	}
}

func TestFailedRunStillKeepsItsTranscript(t *testing.T) {
	// A failed run is exactly when the conversation matters most.
	runner := &fakeRunner{
		done:   true,
		events: nil, // no envelope event at all → the "aborted" path
		turns:  sampleTurns(),
	}
	r, c := newInvestigation(t, runner, investigationFixture()...)
	applyOnce(t, r)

	cm := getTranscript(t, c, "finding-aa-1-inv-1-transcript")
	if len(cm.BinaryData[transcriptstore.DataKey]) == 0 {
		t.Error("transcript is empty for a failed run")
	}

	var inv v1alpha1.Investigation
	key := types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1-inv-1"}
	if err := c.Get(t.Context(), key, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Status.Phase != v1alpha1.RunFailed {
		t.Errorf("phase = %q, want failed", inv.Status.Phase)
	}
	if inv.Status.Stage.Transcript == nil {
		t.Error("failed run lost its transcript reference")
	}
}
