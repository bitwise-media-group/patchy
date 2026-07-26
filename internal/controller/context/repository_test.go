// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package context

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/pkg/enhance"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// cloudFinding is an SCC finding as ingest writes it: a cloud resource, and
// no repository at all.
func cloudFinding() *v1alpha1.Finding {
	return &v1alpha1.Finding{
		ObjectMeta: metav1.ObjectMeta{Name: "finding-aa-1", Namespace: "patchy"},
		Spec: v1alpha1.FindingSpec{
			IntegrationRef: v1alpha1.LocalObjectReference{Name: "gcp"},
			Source:         "gcp-scc",
			Advisories:     []string{"category:PUBLIC_BUCKET_ACL"},
			CloudResource: &v1alpha1.FindingCloudResource{
				Provider: v1alpha1.CloudProviderGoogle,
				Name:     "//storage.googleapis.com/projects/acme-prod/buckets/artifacts",
				Type:     "google.cloud.storage.Bucket",
				Project:  "projects/acme-prod",
			},
		},
		Status: v1alpha1.FindingStatus{Phase: v1alpha1.PhaseOpened},
	}
}

// resolver is an enhancer that names a repository.
type resolver struct {
	id  string
	ref *source.RepositoryRef
	err error
	// saw records the issue it was handed, to prove the cloud resource
	// reaches enhancers at all.
	saw *enhance.Issue
}

func (r *resolver) ID() string { return r.id }

func (r *resolver) Enhance(_ context.Context, issue enhance.Issue) (*enhance.Enrichment, error) {
	r.saw = &issue
	if r.err != nil {
		return nil, r.err
	}
	if r.ref == nil {
		return nil, nil
	}
	return &enhance.Enrichment{Repository: r.ref}, nil
}

func TestCloudResourceReachesEnhancers(t *testing.T) {
	e := &resolver{id: "gcp"}
	r, _ := newCRDReconciler(t, []enhance.Enhancer{e}, cloudFinding())
	run(t, r)

	if e.saw == nil || e.saw.CloudResource == nil {
		t.Fatal("enhancer received no cloud resource; it cannot resolve a repository without one")
	}
	if got := e.saw.CloudResource.Name; got != "//storage.googleapis.com/projects/acme-prod/buckets/artifacts" {
		t.Errorf("CloudResource.Name = %q, want the SCC resource name", got)
	}
	if got := e.saw.CloudResource.Provider; got != "google" {
		t.Errorf("CloudResource.Provider = %q, want google", got)
	}
}

func TestResolvedRepositoryIsWrittenToSpec(t *testing.T) {
	e := &resolver{id: "gcp", ref: &source.RepositoryRef{
		Provider: "github", Owner: "acme", Name: "infra-prod",
	}}
	r, c := newCRDReconciler(t, []enhance.Enhancer{e}, cloudFinding())
	run(t, r)

	fnd := getFinding(t, c)
	if fnd.Spec.Repository == nil {
		t.Fatal("spec.repository is nil; the enhancer's answer was dropped")
	}
	want := v1alpha1.FindingRepository{
		Type: v1alpha1.RepositoryTypeGitHub,
		URL:  "https://github.com/acme/infra-prod",
		Name: "acme/infra-prod",
	}
	if *fnd.Spec.Repository != want {
		t.Errorf("spec.repository = %+v, want %+v", *fnd.Spec.Repository, want)
	}
	if fnd.Status.Phase != v1alpha1.PhaseEnhanced {
		t.Errorf("phase = %s, want Enhanced", fnd.Status.Phase)
	}
	if !meta.IsStatusConditionTrue(fnd.Status.Conditions, v1alpha1.ConditionContextEnhanced) {
		t.Error("ContextEnhanced is not True on a finding whose repository resolved")
	}
}

// Set-once is what keeps the rollup ledger, the clone artifact and the agent
// Jobs agreeing on one repository. An enhancer must never revise it.
func TestResolvedRepositoryNeverOverwrites(t *testing.T) {
	existing := openedFinding() // already carries acme/orders
	e := &resolver{id: "gcp", ref: &source.RepositoryRef{
		Provider: "github", Owner: "someone", Name: "else",
	}}
	r, c := newCRDReconciler(t, []enhance.Enhancer{e}, existing)
	run(t, r)

	if got := getFinding(t, c).Spec.Repository.Name; got != "acme/orders" {
		t.Errorf("spec.repository.name = %q, want the original acme/orders", got)
	}
}

// The first enhancer to name a repository wins, matching how the projection
// already resolves colliding attributes.
func TestFirstResolvedRepositoryWins(t *testing.T) {
	chain := []enhance.Enhancer{
		&resolver{id: "a", ref: &source.RepositoryRef{Provider: "github", Owner: "acme", Name: "first"}},
		&resolver{id: "b", ref: &source.RepositoryRef{Provider: "github", Owner: "acme", Name: "second"}},
	}
	r, c := newCRDReconciler(t, chain, cloudFinding())
	run(t, r)

	if got := getFinding(t, c).Spec.Repository.Name; got != "acme/first" {
		t.Errorf("spec.repository.name = %q, want acme/first", got)
	}
}

// A resource carrying no ownership labels is the normal case, not a failure:
// the finding must advance so it reaches its hand-off rather than being held
// out of sight forever.
func TestUnlabelledResourceAdvancesWithoutRepository(t *testing.T) {
	r, c := newCRDReconciler(t, []enhance.Enhancer{&resolver{id: "gcp"}}, cloudFinding())
	res, err := r.Reconcile(t.Context(), request())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0: a clean 'no labels' answer is final", res.RequeueAfter)
	}

	fnd := getFinding(t, c)
	if fnd.Status.Phase != v1alpha1.PhaseEnhanced {
		t.Errorf("phase = %s, want Enhanced", fnd.Status.Phase)
	}
	if fnd.Spec.Repository != nil {
		t.Errorf("spec.repository = %+v, want nil", fnd.Spec.Repository)
	}
	// The reason is recorded on the object, so the hand-off is explicable
	// without reading logs.
	cond := meta.FindStatusCondition(fnd.Status.Conditions, v1alpha1.ConditionContextEnhanced)
	if cond == nil || cond.Status != metav1.ConditionFalse ||
		cond.Reason != v1alpha1.ReasonRepositoryUnresolved {
		t.Errorf("ContextEnhanced = %+v, want False/RepositoryUnresolved", cond)
	}
}

// The chain runs exactly once and there is no edge back to Opened, so
// advancing after a failed lookup loses the repository permanently — and a
// repo-less finding is terminally handed off. Hold instead.
func TestFailedLookupHoldsAtOpened(t *testing.T) {
	e := &resolver{id: "gcp", err: errors.New("cloud asset inventory unavailable")}
	r, c := newCRDReconciler(t, []enhance.Enhancer{e}, cloudFinding())
	r.RetryAfter = 30 * time.Second

	res, err := r.Reconcile(t.Context(), request())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 30*time.Second {
		t.Errorf("RequeueAfter = %v, want the configured retry interval", res.RequeueAfter)
	}

	fnd := getFinding(t, c)
	if fnd.Status.Phase != v1alpha1.PhaseOpened {
		t.Errorf("phase = %s, want Opened: the finding must stay retryable", fnd.Status.Phase)
	}
	cond := meta.FindStatusCondition(fnd.Status.Conditions, v1alpha1.ConditionContextEnhanced)
	if cond == nil || cond.Status != metav1.ConditionFalse ||
		cond.Reason != v1alpha1.ReasonRepositoryUnresolved {
		t.Errorf("ContextEnhanced = %+v, want False/RepositoryUnresolved", cond)
	}
}

// The hold is bounded. Once the accumulation window has closed the finding is
// ready to be investigated, so a still-broken lookup stops being worth
// waiting for: better handed to a human than held indefinitely.
func TestHoldIsBoundedByAccumulationWindow(t *testing.T) {
	fnd := cloudFinding()
	meta.SetStatusCondition(&fnd.Status.Conditions, metav1.Condition{
		Type:   v1alpha1.ConditionAccumulationComplete,
		Status: metav1.ConditionTrue,
		Reason: "WindowElapsed",
	})
	e := &resolver{id: "gcp", err: errors.New("still unavailable")}
	r, c := newCRDReconciler(t, []enhance.Enhancer{e}, fnd)

	res, err := r.Reconcile(t.Context(), request())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0 once the window has closed", res.RequeueAfter)
	}
	if got := getFinding(t, c).Status.Phase; got != v1alpha1.PhaseEnhanced {
		t.Errorf("phase = %s, want Enhanced: the hold must not outlive the window", got)
	}
}

// Expediting is an operator saying "this cannot wait", and the gate honours
// that by skipping the accumulation window. Holding here would put the delay
// straight back.
func TestExpeditedFindingIsNeverHeld(t *testing.T) {
	fnd := cloudFinding()
	fnd.Spec.Expedite = &v1alpha1.ActionRequest{By: "alice", At: metav1.NewTime(crdClock)}
	e := &resolver{id: "gcp", err: errors.New("unavailable")}
	r, c := newCRDReconciler(t, []enhance.Enhancer{e}, fnd)

	res, err := r.Reconcile(t.Context(), request())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0 for an expedited finding", res.RequeueAfter)
	}
	if got := getFinding(t, c).Status.Phase; got != v1alpha1.PhaseEnhanced {
		t.Errorf("phase = %s, want Enhanced", got)
	}
}

// A code finding whose enhancer fails keeps the original behaviour: one
// broken enhancer must not wedge the pipeline.
func TestFailedEnhancerOnCodeFindingStillAdvances(t *testing.T) {
	e := &resolver{id: "cmdb", err: errors.New("cmdb down")}
	r, c := newCRDReconciler(t, []enhance.Enhancer{e}, openedFinding())

	res, err := r.Reconcile(t.Context(), request())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0: only cloud findings hold", res.RequeueAfter)
	}
	if got := getFinding(t, c).Status.Phase; got != v1alpha1.PhaseEnhanced {
		t.Errorf("phase = %s, want Enhanced", got)
	}
}

func TestToFindingRepository(t *testing.T) {
	for _, tt := range []struct {
		name string
		ref  *source.RepositoryRef
		want *v1alpha1.FindingRepository
	}{
		{"nil", nil, nil},
		{
			"owner and name compose a github url",
			&source.RepositoryRef{Provider: "github", Owner: "acme", Name: "infra"},
			&v1alpha1.FindingRepository{
				Type: "github", URL: "https://github.com/acme/infra", Name: "acme/infra",
			},
		},
		{
			// The enhancer knows its own host; a URL is the only way to name a
			// self-hosted forge, so it wins.
			"an explicit url supersedes owner and name",
			&source.RepositoryRef{
				Provider: "github", Owner: "acme", Name: "infra",
				URL: "https://ghe.acme.internal/platform/infra",
			},
			&v1alpha1.FindingRepository{
				Type: "github", URL: "https://ghe.acme.internal/platform/infra", Name: "acme/infra",
			},
		},
		{
			"a url alone recovers the display name",
			&source.RepositoryRef{Provider: "github", URL: "https://github.com/acme/infra.git"},
			&v1alpha1.FindingRepository{
				Type: "github", URL: "https://github.com/acme/infra.git", Name: "acme/infra",
			},
		},
		// A forge patchy cannot clone would stall the finding at the gate
		// instead of handing it off, so it is dropped rather than written.
		{
			"an unsupported forge is dropped",
			&source.RepositoryRef{Provider: "gitlab", Owner: "acme", Name: "infra"},
			nil,
		},
		{
			"a ref naming nothing is dropped",
			&source.RepositoryRef{Provider: "github"},
			nil,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := toFindingRepository(tt.ref)
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("toFindingRepository() = %+v, want nil", got)
			case tt.want != nil && got == nil:
				t.Errorf("toFindingRepository() = nil, want %+v", tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Errorf("toFindingRepository() = %+v, want %+v", *got, *tt.want)
			}
		})
	}
}

// request is the reconcile request for the fixture finding.
func request() ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "patchy", Name: "finding-aa-1"}}
}
