// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"testing"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// TestKeyHashIsStableForRepositoryFindings pins the accumulation key against
// accidental change.
//
// The hash is persisted in a label on every live Finding, and the admission
// policy freezes that label, so there is no migration. Changing the hashed
// string for a repo-bearing finding would orphan every existing family: the
// family lookup would find nothing, accumulation would open generation 1
// alongside the still-live generation, and every open finding in the estate
// would get a second tracking issue. If this test fails, the input string
// changed — that is the bug, not the expectation.
// Verify the expectation independently rather than trusting the code that
// produced it:
//
//	printf 'gh|ghas|https://github.com/acme/shop|CWE-79' | shasum -a 256 | cut -c1-10
func TestKeyHashIsStableForRepositoryFindings(t *testing.T) {
	const want = "0acf1f87c7"
	got := keyHash("gh", "ghas", "https://github.com/acme/shop", "CWE-79")
	if got != want {
		t.Errorf("keyHash(repository finding) = %q, want %q\n"+
			"The accumulation key changed. Every live Finding's key-hash label is now stale, "+
			"so accumulation will open a duplicate family (and a duplicate tracking issue) for "+
			"each one. Keep the hashed string byte-identical for repo-bearing findings.", got, want)
	}
}

// A cloud finding hashes its resource in the position a repository URL
// occupies, so the two never collide and neither disturbs the other.
func TestKeyHashScopesCloudFindingsByResource(t *testing.T) {
	const (
		bucketA = "//storage.googleapis.com/projects/acme-prod/buckets/a"
		bucketB = "//storage.googleapis.com/projects/acme-prod/buckets/b"
	)
	a := keyHash("gcp", "gcp-scc", bucketA, "category:PUBLIC_BUCKET_ACL")
	b := keyHash("gcp", "gcp-scc", bucketB, "category:PUBLIC_BUCKET_ACL")
	if a == b {
		t.Error("two resources with the same category share a family; " +
			"they may resolve to different repositories, so they must not accumulate together")
	}
	// Same resource, same category: this is the re-notification SCC sends on
	// every finding update, and it must fold rather than pile up.
	if a != keyHash("gcp", "gcp-scc", bucketA, "category:PUBLIC_BUCKET_ACL") {
		t.Error("keyHash is not deterministic")
	}
}

func TestAccumulationScope(t *testing.T) {
	resource := &source.CloudResource{Provider: "google", Name: "//compute.googleapis.com/x"}
	for _, tt := range []struct {
		name    string
		repoURL string
		finding source.Finding
		want    string
	}{
		{
			"a repository finding scopes by repository",
			"https://github.com/acme/shop",
			source.Finding{CloudResource: resource},
			"https://github.com/acme/shop",
		},
		{
			"a cloud finding scopes by resource",
			"",
			source.Finding{CloudResource: resource},
			"//compute.googleapis.com/x",
		},
		{
			// Neither means there is nothing to accumulate against; Ingest
			// rejects it rather than collapsing every such finding into one.
			"a finding naming nothing has no scope",
			"",
			source.Finding{},
			"",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := accumulationScope(tt.repoURL, tt.finding); got != tt.want {
				t.Errorf("accumulationScope() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIngestCloudFinding(t *testing.T) {
	in, c := newIngestor(t)
	integ := testIntegration()

	f := source.Finding{
		Source:  "gcp-scc",
		AlertID: "organizations/1/sources/2/findings/abc",
		CloudResource: &source.CloudResource{
			Provider:    "google",
			Name:        "//storage.googleapis.com/projects/acme-prod/buckets/artifacts",
			Type:        "google.cloud.storage.Bucket",
			Project:     "projects/acme-prod",
			Location:    "europe-west2",
			DisplayName: "artifacts",
		},
		Advisories: []string{"category:PUBLIC_BUCKET_ACL"},
		Title:      "Public bucket acl",
		Severity:   "high",
		HTMLURL:    "https://console.cloud.google.com/x",
	}
	if err := in.Ingest(t.Context(), integ, f); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	found := listFindings(t, c)
	if len(found) != 1 {
		t.Fatalf("findings = %d, want 1", len(found))
	}
	fnd := found[0]

	// The whole point: a cloud finding arrives without a repository, and the
	// enhancer chain decides later whether it gets one.
	if fnd.Spec.Repository != nil {
		t.Errorf("spec.repository = %+v, want nil for a cloud finding", fnd.Spec.Repository)
	}
	if fnd.Spec.CloudResource == nil {
		t.Fatal("spec.cloudResource is nil; the enhancer has nothing to resolve from")
	}
	if got := fnd.Spec.CloudResource.Name; got != f.CloudResource.Name {
		t.Errorf("cloudResource.name = %q, want %q", got, f.CloudResource.Name)
	}
	if got := fnd.Spec.CloudResource.Provider; got != v1alpha1.CloudProviderGoogle {
		t.Errorf("cloudResource.provider = %q, want google", got)
	}

	// The opaque SCC name is the alert id; coercing it through an int would
	// have made it 0.
	if len(fnd.Spec.Alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(fnd.Spec.Alerts))
	}
	if got := fnd.Spec.Alerts[0].ID; got != f.AlertID {
		t.Errorf("alert id = %q, want the SCC finding name %q", got, f.AlertID)
	}
	// Provenance is recorded, so the verdict write-back can route it without
	// guessing from the id's shape.
	if got := fnd.Spec.Alerts[0].Source; got != "gcp-scc" {
		t.Errorf("alert source = %q, want gcp-scc", got)
	}

	// An empty repo-hash label would read as "belongs to some repository".
	if _, ok := fnd.Labels[v1alpha1.LabelRepoHash]; ok {
		t.Errorf("repo-hash label present on a repo-less finding: %q", fnd.Labels[v1alpha1.LabelRepoHash])
	}
}

// SCC re-sends a notification on every finding update, so the same resource
// and category must fold into the live finding rather than opening a new one.
func TestIngestCloudFindingAccumulates(t *testing.T) {
	in, c := newIngestor(t)
	integ := testIntegration()

	first := source.Finding{
		Source:        "gcp-scc",
		AlertID:       "organizations/1/sources/2/findings/abc",
		CloudResource: &source.CloudResource{Provider: "google", Name: "//storage.googleapis.com/b"},
		Advisories:    []string{"category:PUBLIC_BUCKET_ACL"},
	}
	second := first
	second.AlertID = "organizations/1/sources/2/findings/def"

	for _, f := range []source.Finding{first, second} {
		if err := in.Ingest(t.Context(), integ, f); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}

	found := listFindings(t, c)
	if len(found) != 1 {
		t.Fatalf("findings = %d, want 1", len(found))
	}
	fnd := found[0]
	if len(fnd.Spec.Alerts) != 2 {
		t.Errorf("alerts = %d, want both folded into one finding", len(fnd.Spec.Alerts))
	}
}

// A finding naming neither a repository nor a resource has no accumulation
// scope. Ingesting it would collapse every such finding sharing an advisory
// into a single family, so it is rejected instead.
func TestIngestRejectsScopelessFinding(t *testing.T) {
	in, _ := newIngestor(t)
	err := in.Ingest(t.Context(), testIntegration(), source.Finding{
		Source:     "mystery",
		Advisories: []string{"CVE-2026-0001"},
	})
	if err == nil {
		t.Error("Ingest() = nil, want a rejection: the finding has nothing to accumulate against")
	}
}
