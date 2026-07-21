// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package source

import (
	"context"
	"io"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/artifact"
	"github.com/bitwise-media-group/patchy/internal/forge"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/kube"
)

// fakeForgeClient serves canned branch/SHA/tarball answers.
type fakeForgeClient struct {
	defaultBranch string
	headSHA       string
	tarball       string
	headCalls     int
}

func (f *fakeForgeClient) DefaultBranch(context.Context, ghclient.Repo) (string, error) {
	return f.defaultBranch, nil
}

func (f *fakeForgeClient) HeadSHA(context.Context, ghclient.Repo, string) (string, error) {
	f.headCalls++
	return f.headSHA, nil
}

func (f *fakeForgeClient) Tarball(context.Context, ghclient.Repo, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.tarball)), nil
}

func testForge(name string) *v1alpha1.Forge {
	return &v1alpha1.Forge{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "patchy"},
		Spec: v1alpha1.ForgeSpec{
			Provider:  v1alpha1.ForgeProviderGitHub,
			SecretRef: v1alpha1.LocalSecretReference{Name: "creds"},
		},
	}
}

func testRepository() *v1alpha1.Repository {
	return &v1alpha1.Repository{
		ObjectMeta: metav1.ObjectMeta{Name: "finding-ab-1-src", Namespace: "patchy"},
		Spec:       v1alpha1.RepositorySpec{URL: "https://github.com/acme/orders"},
	}
}

// harness builds a reconciler over a fake cluster seeded with objs.
func harness(t *testing.T, gh *fakeForgeClient, objs ...client.Object) (*RepositoryReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Repository{}, &v1alpha1.Forge{}).
		Build()
	store, err := artifact.NewStore(t.TempDir(), "http://arts.local")
	if err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	r := &RepositoryReconciler{
		Client:    c,
		Forges:    forge.NewStore(c),
		Artifacts: store,
		ClientFor: func(context.Context, *forge.Resolved) (forgeClient, error) { return gh, nil },
	}
	return r, c
}

// repoName is the single Repository every table case reconciles.
const repoName = "finding-ab-1-src"

func reconcile(t *testing.T, r *RepositoryReconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "patchy", Name: repoName},
	})
	if err != nil {
		t.Fatalf("Reconcile(%s): %v", repoName, err)
	}
	return res
}

func getRepo(t *testing.T, c client.Client) *v1alpha1.Repository {
	t.Helper()
	var out v1alpha1.Repository
	key := types.NamespacedName{Namespace: "patchy", Name: repoName}
	if err := c.Get(t.Context(), key, &out); err != nil {
		t.Fatalf("Get(%s): %v", repoName, err)
	}
	return &out
}

func TestReconcileProducesArtifact(t *testing.T) {
	gh := &fakeForgeClient{defaultBranch: "main", headSHA: "abc123", tarball: "tar-bytes"}
	r, c := harness(t, gh, testForge("gh"), testRepository())

	reconcile(t, r)

	repo := getRepo(t, c)
	if repo.Status.ResolvedSHA != "abc123" {
		t.Errorf("resolvedSHA = %q, want abc123", repo.Status.ResolvedSHA)
	}
	if repo.Status.Forge == nil || repo.Status.Forge.Name != "gh" {
		t.Errorf("forge = %+v, want gh", repo.Status.Forge)
	}
	art := repo.Status.Artifact
	if art == nil || art.Digest == "" || art.SizeBytes != int64(len("tar-bytes")) {
		t.Errorf("artifact = %+v, want digest and size set", repo.Status.Artifact)
	}
	if !meta.IsStatusConditionTrue(repo.Status.Conditions, v1alpha1.ConditionReady) {
		t.Errorf("Ready condition = %+v, want True", repo.Status.Conditions)
	}
}

func TestReconcilePinsSHAOnce(t *testing.T) {
	gh := &fakeForgeClient{defaultBranch: "main", headSHA: "abc123", tarball: "v1"}
	r, c := harness(t, gh, testForge("gh"), testRepository())

	reconcile(t, r)
	gh.headSHA = "def456" // head moved upstream
	reconcile(t, r)

	repo := getRepo(t, c)
	if repo.Status.ResolvedSHA != "abc123" {
		t.Errorf("resolvedSHA = %q after second reconcile, want the pinned abc123", repo.Status.ResolvedSHA)
	}
	if gh.headCalls != 1 {
		t.Errorf("HeadSHA calls = %d, want 1 (pin exactly once)", gh.headCalls)
	}
}

func TestReconcileRefetchesAfterRestart(t *testing.T) {
	gh := &fakeForgeClient{defaultBranch: "main", headSHA: "abc123", tarball: "tar-bytes"}
	r, c := harness(t, gh, testForge("gh"), testRepository())
	reconcile(t, r)
	first := getRepo(t, c).Status.Artifact

	// A restarted controller has an empty store but a populated status.
	fresh, err := artifact.NewStore(t.TempDir(), "http://arts.local")
	if err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	r.Artifacts = fresh
	reconcile(t, r)

	repo := getRepo(t, c)
	if repo.Status.Artifact == nil || repo.Status.Artifact.URL == first.URL {
		t.Errorf("artifact URL = %+v, want re-served under a fresh id", repo.Status.Artifact)
	}
	if _, ok := fresh.Get("patchy/finding-ab-1-src"); !ok {
		t.Error("fresh store has no artifact after reconcile")
	}
}

func TestReconcileNoForgeMatch(t *testing.T) {
	gh := &fakeForgeClient{}
	r, c := harness(t, gh, testRepository()) // no forges at all

	reconcile(t, r)

	repo := getRepo(t, c)
	cond := meta.FindStatusCondition(repo.Status.Conditions, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != v1alpha1.ReasonNoForgeMatch {
		t.Errorf("Ready condition = %+v, want False/NoForgeMatch", cond)
	}
}

func TestReconcileAmbiguousForges(t *testing.T) {
	gh := &fakeForgeClient{}
	r, c := harness(t, gh, testForge("gh-a"), testForge("gh-b"), testRepository())

	reconcile(t, r)

	repo := getRepo(t, c)
	cond := meta.FindStatusCondition(repo.Status.Conditions, v1alpha1.ConditionReady)
	if cond == nil || cond.Reason != v1alpha1.ReasonAmbiguous {
		t.Errorf("Ready condition = %+v, want reason Ambiguous", cond)
	}
}

func TestReconcileTooLargeStalls(t *testing.T) {
	gh := &fakeForgeClient{defaultBranch: "main", headSHA: "abc123", tarball: strings.Repeat("x", 64)}
	r, c := harness(t, gh, testForge("gh"), testRepository())
	r.MaxArtifactBytes = 16

	reconcile(t, r)

	repo := getRepo(t, c)
	if !meta.IsStatusConditionTrue(repo.Status.Conditions, v1alpha1.ConditionStalled) {
		t.Errorf("Stalled condition = %+v, want True", repo.Status.Conditions)
	}
	if _, ok := r.Artifacts.Get("patchy/finding-ab-1-src"); ok {
		t.Error("over-cap artifact left in store")
	}
}

func TestReconcileDeletionDropsArtifact(t *testing.T) {
	gh := &fakeForgeClient{defaultBranch: "main", headSHA: "abc123", tarball: "v"}
	r, c := harness(t, gh, testForge("gh"), testRepository())
	reconcile(t, r)
	if _, ok := r.Artifacts.Get("patchy/finding-ab-1-src"); !ok {
		t.Fatal("artifact missing after first reconcile")
	}

	if err := c.Delete(t.Context(), testRepository()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reconcile(t, r)
	if _, ok := r.Artifacts.Get("patchy/finding-ab-1-src"); ok {
		t.Error("artifact still in store after Repository deletion")
	}
}
