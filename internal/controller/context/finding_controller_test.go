// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package context

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
)

var crdClock = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// crdEnhancer returns canned enrichments (or an error).
type crdEnhancer struct {
	id  string
	enr *enhance.Enrichment
	err error
}

func (f *crdEnhancer) ID() string { return f.id }

func (f *crdEnhancer) Enhance(context.Context, enhance.Issue) (*enhance.Enrichment, error) {
	return f.enr, f.err
}

func openedFinding() *v1alpha1.Finding {
	return &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1", Namespace: "patchy"},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gh"},
			Source:         "ghas",
			Advisories:     []string{"CVE-2026-0001"},
			Repository: &v1alpha1.FindingRepository{
				Type: "github", URL: "https://github.com/acme/orders", Name: "acme/orders",
			},
		},
		Status: v1alpha1.FindingStatus{Phase: v1alpha1.PhaseOpened},
	}
}

func newCRDReconciler(
	t *testing.T, enhancers []enhance.Enhancer, objs ...client.Object,
) (*FindingReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Finding{}).
		Build()
	return &FindingReconciler{
		Client:    c,
		Enhancers: enhancers,
		Now:       func() time.Time { return crdClock },
	}, c
}

func run(t *testing.T, r *FindingReconciler) {
	t.Helper()
	if _, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func getFinding(t *testing.T, c client.Client) *v1alpha1.Finding {
	t.Helper()
	var f v1alpha1.Finding
	if err := c.Get(t.Context(), types.NamespacedName{Namespace: "patchy", Name: "finding-aa-1"}, &f); err != nil {
		t.Fatalf("Get: %v", err)
	}
	return &f
}

func TestEnhanceAdvancesFinding(t *testing.T) {
	enhancers := []enhance.Enhancer{
		&crdEnhancer{id: "cmdb", enr: &enhance.Enrichment{
			Owners:          []string{"alice", "bob"},
			CommentMarkdown: "owned by team-payments",
		}},
		&crdEnhancer{id: "empty"}, // (nil, nil): nothing to add
	}
	r, c := newCRDReconciler(t, enhancers, openedFinding())
	run(t, r)

	f := getFinding(t, c)
	if f.Status.Phase != v1alpha1.PhaseEnhanced {
		t.Errorf("phase = %q, want Enhanced", f.Status.Phase)
	}
	if len(f.Status.Enrichments) != 1 || f.Status.Enrichments[0].Enhancer != "cmdb" {
		t.Errorf("enrichments = %+v, want one from cmdb", f.Status.Enrichments)
	}
	if len(f.Status.Owners) != 2 {
		t.Errorf("owners = %v, want alice+bob", f.Status.Owners)
	}
	if !meta.IsStatusConditionTrue(f.Status.Conditions, v1alpha1.ConditionContextEnhanced) {
		t.Errorf("ContextEnhanced = %+v, want True", f.Status.Conditions)
	}
}

func TestEnhancerErrorSkipped(t *testing.T) {
	enhancers := []enhance.Enhancer{
		&crdEnhancer{id: "broken", err: errors.New("cmdb down")},
		&crdEnhancer{id: "ok", enr: &enhance.Enrichment{Owners: []string{"carol"}}},
	}
	r, c := newCRDReconciler(t, enhancers, openedFinding())
	run(t, r)

	f := getFinding(t, c)
	if f.Status.Phase != v1alpha1.PhaseEnhanced {
		t.Errorf("phase = %q, want Enhanced despite broken enhancer", f.Status.Phase)
	}
	if len(f.Status.Owners) != 1 || f.Status.Owners[0] != "carol" {
		t.Errorf("owners = %v, want carol", f.Status.Owners)
	}
}

func TestEnhanceSkipsWrongPhase(t *testing.T) {
	fnd := openedFinding()
	fnd.Status.Phase = v1alpha1.PhaseInvestigating
	r, c := newCRDReconciler(t, nil, fnd)
	run(t, r)
	if got := getFinding(t, c).Status.Phase; got != v1alpha1.PhaseInvestigating {
		t.Errorf("phase = %q, want untouched Investigating", got)
	}
}

func TestEnhanceSkipsSuspended(t *testing.T) {
	fnd := openedFinding()
	fnd.Spec.Suspend = true
	r, c := newCRDReconciler(t, nil, fnd)
	run(t, r)
	if got := getFinding(t, c).Status.Phase; got != v1alpha1.PhaseOpened {
		t.Errorf("phase = %q, want untouched Opened", got)
	}
}

func TestEnrichmentMarkdownTruncated(t *testing.T) {
	huge := strings.Repeat("x", maxEnrichmentMarkdown+100)
	enhancers := []enhance.Enhancer{
		&crdEnhancer{id: "big", enr: &enhance.Enrichment{CommentMarkdown: huge}},
	}
	r, c := newCRDReconciler(t, enhancers, openedFinding())
	run(t, r)
	if got := len(getFinding(t, c).Status.Enrichments[0].Markdown); got != maxEnrichmentMarkdown {
		t.Errorf("markdown len = %d, want %d", got, maxEnrichmentMarkdown)
	}
}
