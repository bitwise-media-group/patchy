// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package evalresults

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/bitwise-media-group/patchy/api/v1alpha1"
)

func newClient(t *testing.T) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme)
}

func TestPersistLoadRoundTrip(t *testing.T) {
	c := newClient(t).Build()
	ctx := context.Background()
	unit := &v1alpha1.EvaluationUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "eval-1-u000", Namespace: "patchy", UID: "uid-1"},
	}
	gvk := metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "EvaluationUnit"}
	entry := []byte(`{"schema":5,"cases":[{"id":"a","pass":true}]}`)

	ref, err := Persist(ctx, c, "patchy", map[string]string{v1alpha1.LabelEvaluation: "eval-1"}, unit, gvk, entry)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if ref == nil || ref.Name != "eval-1-u000-results" {
		t.Fatalf("ref = %+v, want name eval-1-u000-results", ref)
	}

	got, err := Load(ctx, c, "patchy", ref.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(got) != string(entry) {
		t.Errorf("Load = %q, want %q", got, entry)
	}

	// Re-persist (idempotent overwrite).
	entry2 := []byte(`{"schema":5,"cases":[]}`)
	if _, err := Persist(ctx, c, "patchy", nil, unit, gvk, entry2); err != nil {
		t.Fatalf("Persist(again): %v", err)
	}
	got, err = Load(ctx, c, "patchy", ref.Name)
	if err != nil {
		t.Fatalf("Load(again): %v", err)
	}
	if string(got) != string(entry2) {
		t.Errorf("Load after rewrite = %q, want %q", got, entry2)
	}
}

func TestPersistEmptyEntryIsNil(t *testing.T) {
	c := newClient(t).Build()
	unit := &v1alpha1.EvaluationUnit{ObjectMeta: metav1.ObjectMeta{Name: "u", Namespace: "patchy"}}
	gvk := metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "EvaluationUnit"}
	ref, err := Persist(context.Background(), c, "patchy", nil, unit, gvk, nil)
	if err != nil || ref != nil {
		t.Errorf("Persist(empty) = (%v, %v), want (nil, nil)", ref, err)
	}
}

func TestLoadMissingIsNil(t *testing.T) {
	c := newClient(t).Build()
	entry, err := Load(context.Background(), c, "patchy", "absent-results")
	if err != nil || entry != nil {
		t.Errorf("Load(missing) = (%q, %v), want (nil, nil)", entry, err)
	}
}
