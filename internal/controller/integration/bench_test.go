// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Tier-1 microbenchmarks for the ingest hot path — the cost of folding one
// scanner alert into a cluster already holding N findings. Run via
// `mise run bench`; the TestComplexity* assertions are opt-in
// (PATCHY_BENCH_ASSERT=1) so plain `go test` stays fast and green.
//
// The controller-runtime fake client deep-copies the whole tracker on every
// List, so it cannot show the difference between a label-selector scan and a
// field-index lookup. cacheEmulator stands in for the manager's informer
// cache with the real store's semantics: a label-selector List scans every
// cached object, a field-selector List is an O(1) index lookup, and only the
// matches are copied.

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/kube"
	"github.com/bitwise-media-group/patchy/internal/loadgen"
)

// benchSweep is the CR-backed dataset sweep; per-op ratios across it
// extrapolate to the 2M design target.
var benchSweep = []int{1_000, 10_000, 100_000}

// loadgenIntegration is the ingesting Integration for loadgen findings —
// issues-enabled so trackingRef resolution short-circuits (the bench measures
// accumulation, not tracker selection).
func loadgenIntegration() *v1alpha1.Integration {
	return &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: loadgen.IntegrationName, Namespace: loadgen.Namespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider: v1alpha1.IntegrationProviderGitHub,
			GitHub: &v1alpha1.GitHubIntegration{
				Issues: &v1alpha1.GitHubIssues{Enabled: true},
			},
		},
	}
}

// cacheEmulator emulates the informer cache's List over a preloaded finding
// set. Writes (Get/Create/Update/Status) pass through to the embedded fake
// client; List answers from the emulator's own store the way the real
// thread-safe store would — a field selector hits the key-hash index, any
// other selector scans.
type cacheEmulator struct {
	client.Client
	all    []v1alpha1.Finding
	byHash map[string][]*v1alpha1.Finding
}

func newCacheEmulator(c client.Client, findings []v1alpha1.Finding) *cacheEmulator {
	e := &cacheEmulator{
		Client: c,
		all:    findings,
		byHash: make(map[string][]*v1alpha1.Finding, len(findings)),
	}
	for i := range findings {
		h := findings[i].Labels[v1alpha1.LabelKeyHash]
		e.byHash[h] = append(e.byHash[h], &findings[i])
	}
	return e
}

// List implements the informer-store read path: index lookup for a field
// selector, full scan for a label selector.
func (e *cacheEmulator) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	fl, ok := list.(*v1alpha1.FindingList)
	if !ok {
		return fmt.Errorf("cacheEmulator: unsupported list %T", list)
	}
	lo := client.ListOptions{}
	lo.ApplyOptions(opts)
	fl.Items = fl.Items[:0]
	if lo.FieldSelector != nil {
		// O(1): the registered index, exactly what MatchingFields buys on the
		// real cache.
		hash := ""
		for _, req := range lo.FieldSelector.Requirements() {
			hash = req.Value
		}
		for _, f := range e.byHash[hash] {
			fl.Items = append(fl.Items, *f.DeepCopy())
		}
		return nil
	}
	// O(N): the label-selector scan over every cached object; only matches
	// are copied, matching the real store.
	sel := labels.Everything()
	if lo.LabelSelector != nil {
		sel = lo.LabelSelector
	}
	for i := range e.all {
		if sel.Matches(labels.Set(e.all[i].Labels)) {
			fl.Items = append(fl.Items, *e.all[i].DeepCopy())
		}
	}
	return nil
}

// newBenchIngestor preloads n findings into a fake client (for the write
// path) and the cache emulator (for the List path).
func newBenchIngestor(tb testing.TB, n int, o loadgen.Opts) *Ingestor {
	tb.Helper()
	findings := loadgen.Findings(n, o)
	c := fake.NewClientBuilder().
		WithScheme(kube.Scheme()).
		WithStatusSubresource(&v1alpha1.Finding{}).
		WithLists(&v1alpha1.FindingList{Items: findings}).
		Build()
	return &Ingestor{
		Client:    newCacheEmulator(c, findings),
		Namespace: loadgen.Namespace,
		Now:       func() time.Time { return testClock },
	}
}

// TestLoadgenFoldsIntoFamily locks the loadgen↔ingest pairing contract: the
// alert built by SourceFinding(i) must fold into the finding built by
// Finding(i), never open a sibling family. Every bench below rests on this.
func TestLoadgenFoldsIntoFamily(t *testing.T) {
	o := loadgen.Opts{}
	in := newBenchIngestor(t, 10, o)
	if err := in.Ingest(t.Context(), loadgenIntegration(), loadgen.SourceFinding(3, 99, o)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	var fnd v1alpha1.Finding
	key := types.NamespacedName{Namespace: loadgen.Namespace, Name: loadgen.FindingName(3, o)}
	if err := in.Get(t.Context(), key, &fnd); err != nil {
		t.Fatalf("Get folded family: %v", err)
	}
	if len(fnd.Spec.Alerts) != 2 {
		t.Fatalf("alerts = %d, want the preloaded one plus the fold", len(fnd.Spec.Alerts))
	}
}

// BenchmarkIngestFold measures folding one alert into an existing family
// with N findings already in the cluster — the per-alert cost of a
// brownfield backlog. Before the key-hash field index this is O(N) per
// alert; after it, flat.
func BenchmarkIngestFold(b *testing.B) {
	for _, n := range benchSweep {
		b.Run(fmt.Sprintf("existing=%d", n), func(b *testing.B) {
			benchIngestFold(b, n)
		})
	}
}

func benchIngestFold(b *testing.B, n int) {
	o := loadgen.Opts{}
	in := newBenchIngestor(b, n, o)
	integ := loadgenIntegration()
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		// A fresh alert ordinal per delivery; families rotate across the
		// dataset so no single object grows past the alert cap dominates.
		if err := in.Ingest(ctx, integ, loadgen.SourceFinding(i%n, 1_000_000+i/n, o)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIngestCreate measures opening a brand-new family with N findings
// already in the cluster: the family List (empty result), the create, and
// the status init.
func BenchmarkIngestCreate(b *testing.B) {
	for _, n := range benchSweep {
		b.Run(fmt.Sprintf("existing=%d", n), func(b *testing.B) {
			benchIngestCreate(b, n)
		})
	}
}

func benchIngestCreate(b *testing.B, n int) {
	o := loadgen.Opts{}
	in := newBenchIngestor(b, n, o)
	integ := loadgenIntegration()
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		// Indices past the dataset are always fresh families.
		if err := in.Ingest(ctx, integ, loadgen.SourceFinding(n+i, 0, o)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFamilyLookup isolates the List mechanics both ways at each sweep
// point: mode=scan is the label-selector path (what ingest does without the
// index), mode=index the field-selector path (what it does with it). The
// spread between the two is the win the key-hash index buys.
func BenchmarkFamilyLookup(b *testing.B) {
	for _, n := range benchSweep {
		o := loadgen.Opts{}
		findings := loadgen.Findings(n, o)
		e := newCacheEmulator(nil, findings)
		ctx := context.Background()
		b.Run(fmt.Sprintf("existing=%d/mode=scan", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				var family v1alpha1.FindingList
				err := e.List(ctx, &family, client.InNamespace(loadgen.Namespace),
					client.MatchingLabels{v1alpha1.LabelKeyHash: loadgen.KeyHash(i%n, o)})
				if err != nil || len(family.Items) != 1 {
					b.Fatalf("scan lookup: %d items, err %v", len(family.Items), err)
				}
			}
		})
		b.Run(fmt.Sprintf("existing=%d/mode=index", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				var family v1alpha1.FindingList
				err := e.List(ctx, &family, client.InNamespace(loadgen.Namespace),
					client.MatchingFields{"labels.key-hash": loadgen.KeyHash(i%n, o)})
				if err != nil || len(family.Items) != 1 {
					b.Fatalf("index lookup: %d items, err %v", len(family.Items), err)
				}
			}
		})
	}
}

// BenchmarkKeyHash is the accumulation-key hash itself — the fixed floor
// under every delivery.
func BenchmarkKeyHash(b *testing.B) {
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_ = keyHash("loadgen", "loadgen-scanner",
			"https://github.com/loadgen/repo-1", "CVE-2026-000001")
	}
}

// TestComplexityIngestFold asserts the per-alert fold cost is flat in the
// backlog size: 100k existing findings may cost at most 3× what 1k does.
// Red before the key-hash field index (the label scan is O(N)), green after.
func TestComplexityIngestFold(t *testing.T) {
	if os.Getenv("PATCHY_BENCH_ASSERT") == "" {
		t.Skip("set PATCHY_BENCH_ASSERT=1 to run complexity assertions")
	}
	small := testing.Benchmark(func(b *testing.B) { benchIngestFold(b, 1_000) })
	large := testing.Benchmark(func(b *testing.B) { benchIngestFold(b, 100_000) })
	ratio := float64(large.NsPerOp()) / float64(small.NsPerOp())
	t.Logf("fold ns/op: existing=1k %d, existing=100k %d, ratio %.2f",
		small.NsPerOp(), large.NsPerOp(), ratio)
	if ratio > 3 {
		t.Errorf("fold cost grows with the backlog: 100k/1k ratio = %.2f, want <= 3 "+
			"(the family List needs the key-hash field index)", ratio)
	}
}
