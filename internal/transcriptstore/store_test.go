// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package transcriptstore

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/transcript"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func investigation(name string) *v1alpha1.Investigation {
	return &v1alpha1.Investigation{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy", UID: types.UID("uid-1")},
	}
}

var invGVK = metav1.TypeMeta{APIVersion: "patchy.bitwisemedia.uk/v1alpha1", Kind: "Investigation"}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	want := []transcript.Turn{
		{Seq: 1, Role: transcript.RoleSystem, Kind: transcript.KindNotice, Text: "session started"},
		{Seq: 2, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "Reading the handler."},
		{Seq: 3, Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Read", Text: "server.go"},
		{Seq: 4, Role: transcript.RoleUser, Kind: transcript.KindToolResult, Text: "func Handler()", Truncated: true},
	}
	raw, err := Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d turns, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Seq != want[i].Seq || got[i].Kind != want[i].Kind ||
			got[i].Tool != want[i].Tool || got[i].Text != want[i].Text ||
			got[i].Truncated != want[i].Truncated {
			t.Errorf("turn %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMarshalCompresses(t *testing.T) {
	// Agent transcripts are highly repetitive; the ConfigMap cap depends on it.
	turns := make([]transcript.Turn, 200)
	for i := range turns {
		turns[i] = transcript.Turn{Seq: i + 1, Role: transcript.RoleAssistant,
			Kind: transcript.KindToolUse, Tool: "Read",
			Text: "internal/controller/investigation/investigation_controller.go"}
	}
	raw, err := Marshal(turns)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(raw) > 4<<10 {
		t.Errorf("compressed to %d bytes, want well under the ConfigMap cap", len(raw))
	}
}

func TestPersistOwnsTheConfigMap(t *testing.T) {
	// The owner reference is the retention design: without it a transcript
	// outlives the finding it explains.
	c := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	inv := investigation("fnd-abc-inv-1")
	turns := []transcript.Turn{{Seq: 1, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "hi"}}

	ref, err := Persist(context.Background(), c, "patchy", map[string]string{"k": "v"}, inv, invGVK, turns)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if ref == nil || ref.Name != "fnd-abc-inv-1-transcript" || ref.Turns != 1 || ref.Truncated {
		t.Fatalf("ref = %+v", ref)
	}

	var cm corev1.ConfigMap
	key := types.NamespacedName{Namespace: "patchy", Name: ref.Name}
	if err := c.Get(context.Background(), key, &cm); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(cm.OwnerReferences) != 1 {
		t.Fatalf("got %d owner refs, want 1", len(cm.OwnerReferences))
	}
	owner := cm.OwnerReferences[0]
	if owner.Kind != "Investigation" || owner.Name != inv.Name || owner.UID != inv.UID {
		t.Errorf("ownerRef = %+v", owner)
	}
	if owner.Controller == nil || !*owner.Controller {
		t.Error("ownerRef Controller = false, want true so the transcript cascades")
	}
}

func TestPersistReportsTruncation(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	turns := []transcript.Turn{
		{Seq: 1, Role: transcript.RoleAssistant, Kind: transcript.KindText, Text: "a"},
		{Seq: 2, Role: transcript.RoleSystem, Kind: transcript.KindNotice, Text: "cut", Truncated: true},
	}
	ref, err := Persist(context.Background(), c, "patchy", nil, investigation("fnd-x-inv-1"), invGVK, turns)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if !ref.Truncated || ref.Turns != 2 {
		t.Errorf("ref = %+v, want 2 turns and truncated", ref)
	}
}

func TestPersistNoTurnsWritesNothing(t *testing.T) {
	// A harness that cannot transcribe must not fail the collect.
	c := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	ref, err := Persist(context.Background(), c, "patchy", nil, investigation("fnd-y-inv-1"), invGVK, nil)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if ref != nil {
		t.Errorf("ref = %+v, want nil", ref)
	}
	var list corev1.ConfigMapList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("wrote %d ConfigMaps, want 0", len(list.Items))
	}
}

func TestPersistIsIdempotent(t *testing.T) {
	// A re-collected run rewrites its own transcript rather than failing.
	c := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	inv := investigation("fnd-z-inv-1")
	first := make([]transcript.Turn, 0, 2)
	first = append(first, transcript.Turn{Seq: 1, Role: transcript.RoleAssistant,
		Kind: transcript.KindText, Text: "one"})
	second := append(first, transcript.Turn{Seq: 2, Role: transcript.RoleAssistant,
		Kind: transcript.KindText, Text: "two"})

	if _, err := Persist(context.Background(), c, "patchy", nil, inv, invGVK, first); err != nil {
		t.Fatalf("first Persist: %v", err)
	}
	ref, err := Persist(context.Background(), c, "patchy", nil, inv, invGVK, second)
	if err != nil {
		t.Fatalf("second Persist: %v", err)
	}
	if ref.Turns != 2 {
		t.Errorf("Turns = %d, want 2", ref.Turns)
	}

	got, err := Load(context.Background(), c, "patchy", ref.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[1].Text != "two" {
		t.Errorf("Load = %+v, want the rewritten turns", got)
	}
}

func TestLoadRoundTrip(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	inv := investigation("fnd-r-inv-1")
	turns := []transcript.Turn{
		{Seq: 1, Role: transcript.RoleAssistant, Kind: transcript.KindToolUse, Tool: "Bash", Text: "go test"},
	}
	ref, err := Persist(context.Background(), c, "patchy", nil, inv, invGVK, turns)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	got, err := Load(context.Background(), c, "patchy", ref.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Tool != "Bash" || got[0].Text != "go test" {
		t.Errorf("Load = %+v", got)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	// Playback of an absent transcript is an empty conversation, not an error.
	c := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	got, err := Load(context.Background(), c, "patchy", "nope-transcript")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != nil {
		t.Errorf("Load = %+v, want nil", got)
	}
}

func TestLoadMissingKeyIsEmpty(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "empty-transcript", Namespace: "patchy"},
		}).Build()
	got, err := Load(context.Background(), c, "patchy", "empty-transcript")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != nil {
		t.Errorf("Load = %+v, want nil", got)
	}
}
