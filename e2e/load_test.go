// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// The Tier-2 load suite: the real controller binaries against envtest at
// tens of thousands of findings — the brownfield-backlog rehearsal the
// microbenchmarks extrapolate from. Opt-in twice over: skipped without
// PATCHY_LOAD=1 (on top of the suite-wide KUBEBUILDER_ASSETS skip), sized by
// PATCHY_LOAD_N (default 50000), with hard assertions only under
// PATCHY_LOAD_ASSERT=1 — absolute numbers are hardware-bound, so they are
// reported, while the complexity-shaped ratios are asserted. Run via
// `mise run load`; targets live in docs/dev/benchmarks.md.
//
// The same caveat as the rest of the suite: envtest has no kubelet, so agent
// Jobs never execute — pipeline progression past ingest is fabricated via
// status writes. Each test boots its own cluster so phases run independently
// under -run; teardown at 50k+ objects is slow, so tests kill the env rather
// than deleting objects.

package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/e2e/fakegithub"
	"github.com/bitwise-media-group/patchy/internal/loadgen"
	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
)

const loadSecret = "e2e-load-hmac-secret"

// loadParams gates and sizes a load test.
func loadParams(t *testing.T) (n int, assert bool) {
	t.Helper()
	if os.Getenv("PATCHY_LOAD") == "" {
		t.Skip("set PATCHY_LOAD=1 (mise run load) to run the load suite")
	}
	n = 50_000
	if raw := os.Getenv("PATCHY_LOAD_N"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("PATCHY_LOAD_N = %q, want a positive integer", raw)
		}
		n = parsed
	}
	return n, os.Getenv("PATCHY_LOAD_ASSERT") != ""
}

// ---- measurement helpers ---------------------------------------------------

// rssSampler polls a controller subprocess's resident set via ps every 5s,
// reporting the peak at cleanup. Environments that forbid ps (sandboxes)
// degrade to "unavailable" without failing the test.
func rssSampler(t *testing.T, name string, pid int) {
	t.Helper()
	var peakKB atomic.Int64
	unavailable := atomic.Bool{}
	stop := make(chan struct{})
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
			if err != nil {
				unavailable.Store(true)
			} else if kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
				if kb > peakKB.Load() {
					peakKB.Store(kb)
				}
			}
			select {
			case <-stop:
				return
			case <-tick.C:
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		if unavailable.Load() && peakKB.Load() == 0 {
			t.Logf("LOAD %s rss: unavailable (ps not permitted here)", name)
			return
		}
		t.Logf("LOAD %s peak rss: %.1f MiB", name, float64(peakKB.Load())/1024)
	})
}

// countFindings counts via paginated metadata-only lists, so polling a 100k
// backlog does not fetch 100k full objects.
func countFindings(ctx context.Context, c client.Client) (int, error) {
	total := 0
	cont := ""
	for {
		pm := &metav1.PartialObjectMetadataList{}
		pm.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("FindingList"))
		opts := []client.ListOption{client.InNamespace(namespace), client.Limit(5000)}
		if cont != "" {
			opts = append(opts, client.Continue(cont))
		}
		if err := c.List(ctx, pm, opts...); err != nil {
			return 0, err
		}
		total += len(pm.Items)
		cont = pm.GetContinue()
		if cont == "" {
			return total, nil
		}
	}
}

// progressSample is one poll of the ingested-finding count.
type progressSample struct {
	elapsed time.Duration
	count   int
}

// timeAt interpolates when the count crossed target.
func timeAt(samples []progressSample, target int) time.Duration {
	for i, s := range samples {
		if s.count >= target {
			if i == 0 {
				return s.elapsed
			}
			prev := samples[i-1]
			if s.count == prev.count {
				return s.elapsed
			}
			frac := float64(target-prev.count) / float64(s.count-prev.count)
			return prev.elapsed + time.Duration(frac*float64(s.elapsed-prev.elapsed))
		}
	}
	return samples[len(samples)-1].elapsed
}

// ---- seeding ---------------------------------------------------------------

// seedLoadIntegration creates ONE generic integration (source on, nothing
// else): no github Integration means no issue projection and no fakegithub
// anywhere in the ingest path.
func seedLoadIntegration(t *testing.T, cl *cluster) {
	t.Helper()
	ctx := t.Context()
	if err := cl.client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "patchy-loadgen", Namespace: namespace},
		StringData: map[string]string{"webhookSecret": loadSecret},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cl.client.Create(ctx, &v1alpha1.Integration{
		ObjectMeta: metav1.ObjectMeta{Name: "loadgen", Namespace: namespace},
		Spec: v1alpha1.IntegrationSpec{
			Provider:  v1alpha1.IntegrationProviderGeneric,
			SecretRef: &v1alpha1.LocalSecretReference{Name: "patchy-loadgen"},
			Generic: &v1alpha1.GenericIntegration{
				Source: &v1alpha1.GenericSource{Enabled: true},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// bulkCreateFindings seeds n loadgen findings (create + status update) with
// eight writers — roughly an order of magnitude faster than driving them
// through webhooks, for the tests whose subject is not ingest.
func bulkCreateFindings(t *testing.T, cl *cluster, n int, o loadgen.Opts,
	mutate func(i int, fnd *v1alpha1.Finding)) {
	t.Helper()
	ctx := t.Context()
	start := time.Now()
	var next atomic.Int64
	var wg sync.WaitGroup
	errc := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				fnd := loadgen.Finding(i, o)
				if mutate != nil {
					mutate(i, fnd)
				}
				status := fnd.Status
				fnd.Status = v1alpha1.FindingStatus{}
				if err := cl.client.Create(ctx, fnd); err != nil {
					errc <- fmt.Errorf("create %s: %w", fnd.Name, err)
					return
				}
				fnd.Status = status
				if err := cl.client.Status().Update(ctx, fnd); err != nil {
					errc <- fmt.Errorf("status %s: %w", fnd.Name, err)
					return
				}
			}
		})
	}
	wg.Wait()
	select {
	case err := <-errc:
		t.Fatal(err)
	default:
	}
	t.Logf("LOAD seeded %d findings in %v (%.0f findings/s)",
		n, time.Since(start).Round(time.Second), float64(n)/time.Since(start).Seconds())
}

// ---- the tests -------------------------------------------------------------

// TestLoadIngest drives N unique findings through the generic webhook route
// into the real integration-controller and measures sustained alerts/s plus
// the first-vs-last-decile per-alert cost — the flatness proof for the
// key-hash family index (before it, per-alert cost grows with the backlog
// and the ratio blows past 2).
func TestLoadIngest(t *testing.T) {
	n, assert := loadParams(t)
	cl := startCluster(t)
	seedLoadIntegration(t, cl)

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	// A 24h window keeps every family foldable for the whole run.
	pid := cl.controller(t, "integration-controller",
		"--listen-addr", listen, "--accumulation-window", "24h")
	rssSampler(t, "integration-controller", pid)
	url := "http://" + listen + "/generic/loadgen/webhooks"

	const batch = 1000
	batches := (n + batch - 1) / batch
	var nextBatch atomic.Int64
	post := func() {
		for {
			b := int(nextBatch.Add(1)) - 1
			if b >= batches {
				return
			}
			payload := loadIngestPayload(b*batch, min((b+1)*batch, n))
			deliverLoad(t, url, fmt.Sprintf("load-batch-%d", b), payload)
		}
	}
	start := time.Now()
	var posters sync.WaitGroup
	for range 2 {
		posters.Go(post)
	}

	// Poll the finding count until every delivery landed.
	ctx := t.Context()
	var samples []progressSample
	deadline := time.Now().Add(45 * time.Minute)
	for {
		count, err := countFindings(ctx, cl.client)
		if err != nil {
			t.Fatalf("count findings: %v", err)
		}
		samples = append(samples, progressSample{elapsed: time.Since(start), count: count})
		if count >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ingest stalled at %d/%d findings", count, n)
		}
		time.Sleep(500 * time.Millisecond)
	}
	posters.Wait()
	total := time.Since(start)

	decile := n / 10
	firstCost := timeAt(samples, decile).Seconds() / float64(decile)
	lastCost := (timeAt(samples, n) - timeAt(samples, n-decile)).Seconds() / float64(decile)
	ratio := lastCost / firstCost
	t.Logf("LOAD ingest: %d findings in %v (%.0f alerts/s sustained)",
		n, total.Round(time.Second), float64(n)/total.Seconds())
	t.Logf("LOAD ingest per-alert cost: first decile %.2fms, last decile %.2fms, ratio %.2f",
		firstCost*1000, lastCost*1000, ratio)
	// The decile comparison needs a run long enough that startup ramp
	// (webhook workers warming, informer priming) stops dominating the first
	// decile; below ~20k the ratio is measurement noise.
	const assertFloor = 20_000
	switch {
	case assert && n < assertFloor:
		t.Logf("LOAD ingest: decile assertion skipped below n=%d (run too short for meaningful deciles)", assertFloor)
	case assert && ratio > 2.0:
		t.Errorf("per-alert ingest cost grew with the backlog: last/first decile = %.2f, want <= 2.0", ratio)
	}
}

// loadIngestPayload is one findings delivery for indices [start, end) —
// every finding its own accumulation family, so ingest exercises the
// family-List + create path n times.
func loadIngestPayload(start, end int) []byte {
	p := pkggeneric.Payload{Version: pkggeneric.Version, Event: pkggeneric.EventFindings}
	for i := start; i < end; i++ {
		p.Findings = append(p.Findings, pkggeneric.Finding{
			Repo:        &pkggeneric.Repo{Owner: "loadgen", Name: fmt.Sprintf("repo-%d", i%100)},
			AlertID:     fmt.Sprintf("load-%d", i),
			Advisories:  []string{fmt.Sprintf("CVE-2026-%07d", i)},
			RuleID:      fmt.Sprintf("rule/%d", i%50),
			Title:       fmt.Sprintf("Load finding %d", i),
			Description: strings.Repeat("synthetic finding body ", 22),
			Severity:    "high",
		})
	}
	out, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return out
}

// deliverLoad signs and posts one payload, retrying while the receiver's
// queue is full (503 asks the provider to redeliver, so the load generator
// behaves like one).
func deliverLoad(t *testing.T, url, deliveryID string, payload []byte) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(loadSecret))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(pkggeneric.SignatureHeader, sig)
		req.Header.Set(pkggeneric.DeliveryHeader, fmt.Sprintf("%s-%d", deliveryID, attempt))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("deliver %s: %v", deliveryID, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusAccepted:
			return
		case http.StatusServiceUnavailable:
			time.Sleep(100 * time.Millisecond) // queue full; redeliver
		default:
			t.Fatalf("deliver %s: status %d", deliveryID, resp.StatusCode)
		}
	}
}

// TestLoadStatusServer seeds N findings directly, then measures the real
// status-server: the trimmed GET /api/findings list cold and warm, response
// size raw and gzipped, and the per-finding detail route.
func TestLoadStatusServer(t *testing.T) {
	n, _ := loadParams(t)
	cl := startCluster(t)
	opts := loadgen.Opts{
		AlertsPerFinding: 2,
		PhaseMix: map[v1alpha1.Phase]int{
			v1alpha1.PhaseOpened: 10, v1alpha1.PhaseEnhanced: 10, v1alpha1.PhaseQueued: 5,
			v1alpha1.PhaseRemediated: 50, v1alpha1.PhaseDismissed: 15, v1alpha1.PhaseHandedOff: 10,
		},
	}
	bulkCreateFindings(t, cl, n, opts, nil)

	authCfg := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(authCfg, []byte("mode: none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	startedAt := time.Now()
	pid := cl.controller(t, "status-server", "--listen-addr", listen, "--auth-config", authCfg)
	rssSampler(t, "status-server", pid)
	t.Logf("LOAD status-server ready (informer sync) in %v", time.Since(startedAt).Round(time.Millisecond))

	get := func(encoding string) (time.Duration, int64) {
		req, err := http.NewRequest(http.MethodGet, "http://"+listen+"/api/findings", nil)
		if err != nil {
			t.Fatal(err)
		}
		if encoding != "" {
			req.Header.Set("Accept-Encoding", encoding)
		}
		tr := &http.Transport{DisableCompression: true}
		begin := time.Now()
		resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Minute}).Do(req)
		if err != nil {
			t.Fatalf("GET /api/findings: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/findings: status %d", resp.StatusCode)
		}
		bytes, err := io.Copy(io.Discard, resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return time.Since(begin), bytes
	}

	coldLatency, rawBytes := get("")
	t.Logf("LOAD status-server cold GET /api/findings: %v, %.1f MiB raw",
		coldLatency.Round(time.Millisecond), float64(rawBytes)/(1<<20))
	var warmSum time.Duration
	const warmRuns = 3
	for range warmRuns {
		d, _ := get("")
		warmSum += d
	}
	t.Logf("LOAD status-server warm GET /api/findings: %v avg over %d",
		(warmSum / warmRuns).Round(time.Millisecond), warmRuns)
	gzLatency, gzBytes := get("gzip")
	t.Logf("LOAD status-server gzip GET /api/findings: %v, %.1f MiB on the wire (%.1fx smaller)",
		gzLatency.Round(time.Millisecond), float64(gzBytes)/(1<<20), float64(rawBytes)/float64(gzBytes))

	// The per-finding detail route — the per-click cost the trimmed list
	// defers to. Averaged over a handful of distinct findings so a single
	// cache line does not flatter the number.
	const detailRuns = 5
	var detailSum time.Duration
	var detailBytes int64
	for i := range detailRuns {
		url := "http://" + listen + "/api/findings/" + loadgen.FindingName(i, opts)
		begin := time.Now()
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", url, resp.StatusCode)
		}
		nb, err := io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("read detail body: %v", err)
		}
		detailSum += time.Since(begin)
		detailBytes += nb
	}
	t.Logf("LOAD status-server GET /api/findings/{name}: %v avg, %.1f KiB avg over %d",
		(detailSum / detailRuns).Round(time.Millisecond),
		float64(detailBytes)/detailRuns/(1<<10), detailRuns)
}

// TestLoadRollups drives findings to terminal in waves and measures how fast
// the single `total` FindingRollup converges — the conflict-retry hotspot
// with its inherent serial ceiling (documented, not redesigned).
func TestLoadRollups(t *testing.T) {
	n, _ := loadParams(t)
	n = min(max(n/10, 1000), 5000) // the drain ceiling makes 5k plenty
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)

	// --finding-ttl 0 keeps the terminal findings around: loadgen stamps
	// completedAt in the past, and any positive TTL would start deleting
	// mid-measurement.
	pid := cl.controller(t, "remediation-controller",
		append([]string{"--finding-ttl", "0"}, runnerArgs...)...)
	rssSampler(t, "remediation-controller", pid)

	start := time.Now()
	bulkCreateFindings(t, cl, n, loadgen.Opts{
		PhaseMix: map[v1alpha1.Phase]int{v1alpha1.PhaseRemediated: 3, v1alpha1.PhaseDismissed: 1},
	}, nil)

	ctx := t.Context()
	deadline := time.Now().Add(30 * time.Minute)
	var counted int64
	for {
		var rollups v1alpha1.FindingRollupList
		if err := cl.client.List(ctx, &rollups, client.InNamespace(namespace)); err != nil {
			t.Fatalf("list rollups: %v", err)
		}
		counted = 0
		for _, fr := range rollups.Items {
			if fr.Spec.Scope.Type == v1alpha1.ScopeTotal {
				counted = fr.Status.Bucket.Findings
			}
		}
		if counted >= int64(n) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rollup stalled at %d/%d findings counted", counted, n)
		}
		time.Sleep(2 * time.Second)
	}
	total := time.Since(start)
	t.Logf("LOAD rollup: %d terminal findings drained into the total rollup in %v (%.0f findings/s)",
		n, total.Round(time.Second), float64(n)/total.Seconds())
}

// TestLoadProjection seeds findings pointed at the github tracker and
// measures the real projection against the fake GitHub — issues/s, and
// whether fakegithub itself ever becomes the bottleneck.
func TestLoadProjection(t *testing.T) {
	n, _ := loadParams(t)
	n = min(max(n/10, 1000), 10_000)
	cl := startCluster(t)
	gh := fakegithub.New()
	t.Cleanup(gh.Close)
	cl.githubCredentials(t, gh.URL)

	bulkCreateFindings(t, cl, n, loadgen.Opts{}, func(_ int, fnd *v1alpha1.Finding) {
		fnd.Spec.TrackingRef = &v1alpha1.LocalObjectReference{Name: "github"}
		fnd.Spec.Repository = &v1alpha1.FindingRepository{
			Type: v1alpha1.RepositoryTypeGitHub,
			URL:  "https://127.0.0.1/acme/shop", Name: "acme/shop", DefaultBranch: "main",
		}
	})

	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	start := time.Now()
	pid := cl.controller(t, "integration-controller", "--listen-addr", listen)
	rssSampler(t, "integration-controller", pid)

	deadline := time.Now().Add(30 * time.Minute)
	for {
		count := len(gh.Issues())
		if count >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("projection stalled at %d/%d issues", count, n)
		}
		time.Sleep(2 * time.Second)
	}
	total := time.Since(start)
	t.Logf("LOAD projection: %d tracking issues in %v (%.1f issues/s)",
		n, total.Round(time.Second), float64(n)/total.Seconds())
}
