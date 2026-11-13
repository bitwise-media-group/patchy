// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// policyEngine builds an engine over the fixture config with a source
// policy, collecting warn events.
func policyEngine(f *fixture, p *spec.MirrorPolicy, warns *[]string) *Engine {
	f.t.Helper()
	g := f.global()
	if p != nil {
		g.Policy = spec.SourcePolicy{Layers: []spec.PolicyLayer{p.Layer("test config")}}
		if err := g.Policy.Validate(); err != nil {
			f.t.Fatal(err)
		}
	}
	f.eng = New(Config{Root: f.root, Global: g, Now: func() time.Time { return testNow },
		OnEvent: func(ev Event) {
			if ev.Kind == "warn" && strings.Contains(ev.Message, "source policy") {
				*warns = append(*warns, ev.Message)
			}
		}})
	return f.eng
}

// demoStore lays down the demo chart and its image upstream, converged
// with no policy in force.
func demoStore(t *testing.T) (*fixture, spec.Entry) {
	t.Helper()
	f := newFixture(t)
	f.pushImage(f.host+"/apps/app:1.0.0", testNow.AddDate(0, -1, 0))
	f.pushChart(f.host+"/upstream", "demo", "1.0.0", f.chartTgz("demo", "1.0.0", "1.0.0", appCanonical+":1.0.0"))
	f.chartManifest("demo", "")
	f.write("charts/demo/values/discovery.yaml", "{}\n")
	var warns []string
	eng := policyEngine(f, nil, &warns)
	entry, err := eng.Entry("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Upgrade(context.Background(), entry, ""); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	return f, entry
}

func TestEngineSourcePolicy(t *testing.T) {
	f, entry := demoStore(t)
	chartAllow := spec.PolicyRules{Allow: []string{"oci://" + f.host + "/**"}}
	for _, tt := range []struct {
		name string
		run  func(context.Context, *testing.T, *fixture, spec.Entry, spec.PolicyRules)
	}{
		{"permitted sources pass every gate", policyPermitted},
		{"a rejected chart fails before the pull", policyRejectedChart},
		{"a rejected image blocks regen, lint and sync", policyRejectedImage},
		{"warn mode narrates once and lets everything run", policyWarnMode},
	} {
		t.Run(tt.name, func(t *testing.T) { tt.run(context.Background(), t, f, entry, chartAllow) })
	}
}

// policyPermitted: permitted sources pass every gate.
func policyPermitted(ctx context.Context, t *testing.T, f *fixture, entry spec.Entry, chartAllow spec.PolicyRules) {
	t.Helper()
	var warns []string
	eng := policyEngine(f, &spec.MirrorPolicy{
		Charts: chartAllow,
		Images: spec.PolicyRules{Allow: []string{"example.test/apps/*"}},
	}, &warns)
	if _, err := eng.Upgrade(ctx, entry, ""); err != nil {
		t.Errorf("Upgrade: %v", err)
	}
	if issues, err := eng.Lint(entry); err != nil || len(issues) != 0 {
		t.Errorf("Lint = %v, %v", issues, err)
	}
	if _, err := eng.Sync(ctx, entry, SyncOptions{DryRun: true}); err != nil {
		t.Errorf("Sync: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v", warns)
	}
}

// policyRejectedChart: a rejected chart fails before the pull.
func policyRejectedChart(ctx context.Context, t *testing.T, f *fixture, entry spec.Entry, chartAllow spec.PolicyRules) {
	t.Helper()
	var warns []string
	eng := policyEngine(f, &spec.MirrorPolicy{
		Charts: spec.PolicyRules{Allow: []string{"oci://ghcr.io/**"}},
	}, &warns)
	_, err := eng.Regenerate(ctx, entry, t.TempDir(), false)
	var pe *PolicyError
	if !errors.As(err, &pe) || len(pe.Violations) != 1 || pe.Violations[0].Kind != spec.SourceChart {
		t.Fatalf("Regenerate = %v, want a chart PolicyError", err)
	}
	// validate records it as the entry's failure and keeps going.
	res, err := eng.Validate(ctx, entry, nil)
	if err != nil || res.Err == "" || !res.Failed() {
		t.Errorf("Validate = %+v, %v; want the policy failure recorded", res, err)
	}
}

// policyRejectedImage: a rejected image blocks regen, lint and sync.
func policyRejectedImage(ctx context.Context, t *testing.T, f *fixture, entry spec.Entry, chartAllow spec.PolicyRules) {
	t.Helper()
	var warns []string
	eng := policyEngine(f, &spec.MirrorPolicy{
		Charts: chartAllow,
		Images: spec.PolicyRules{Deny: []string{"example.test/**"}},
	}, &warns)
	_, err := eng.Regenerate(ctx, entry, t.TempDir(), false)
	var pe *PolicyError
	if !errors.As(err, &pe) || pe.Violations[0].Value != appCanonical {
		t.Fatalf("Regenerate = %v, want an image PolicyError", err)
	}
	issues, err := eng.Lint(entry)
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0], `denied by "example.test/**"`) {
		t.Errorf("Lint = %v, %v", issues, err)
	}
	// A lock committed before the policy still never reaches a registry.
	if _, err := eng.Sync(ctx, entry, SyncOptions{DryRun: true}); !errors.As(err, &pe) {
		t.Errorf("Sync = %v, want a PolicyError", err)
	}
}

// policyWarnMode: warn mode narrates once and lets everything run.
func policyWarnMode(ctx context.Context, t *testing.T, f *fixture, entry spec.Entry, chartAllow spec.PolicyRules) {
	t.Helper()
	var warns []string
	eng := policyEngine(f, &spec.MirrorPolicy{
		Mode:   spec.PolicyWarn,
		Charts: chartAllow,
		Images: spec.PolicyRules{Allow: []string{"registry.k8s.io/**"}},
	}, &warns)
	if _, err := eng.Upgrade(ctx, entry, ""); err != nil {
		t.Errorf("Upgrade: %v", err)
	}
	if issues, err := eng.Lint(entry); err != nil || len(issues) != 0 {
		t.Errorf("Lint = %v, %v (warn mode reports no issues)", issues, err)
	}
	if _, err := eng.Sync(ctx, entry, SyncOptions{DryRun: true}); err != nil {
		t.Errorf("Sync: %v", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], appCanonical) {
		t.Errorf("warnings = %v, want exactly one for the image", warns)
	}
}

func TestEngineSourcePolicyArtifact(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.write("artifacts/bundle/manifest.yaml", `apiVersion: mirror.patchy.bitwisemedia.uk/v1alpha1
kind: Artifact
name: bundle
artifact:
  ref: example.test/bundles/policy
  version: v1.0.0
`)
	var warns []string
	eng := policyEngine(f, &spec.MirrorPolicy{
		Artifacts: spec.PolicyRules{Allow: []string{"ghcr.io/**"}},
	}, &warns)
	entry, err := eng.Entry("bundle")
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.Regenerate(ctx, entry, t.TempDir(), false)
	var pe *PolicyError
	if !errors.As(err, &pe) || pe.Violations[0].Kind != spec.SourceArtifact ||
		!strings.Contains(err.Error(), "artifact example.test/bundles/policy is not permitted") {
		t.Errorf("Regenerate = %v, want an artifact PolicyError", err)
	}
	if issues, _ := eng.Lint(entry); len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "source policy") {
		t.Errorf("Lint = %v, want the artifact rejected", issues)
	}
}
