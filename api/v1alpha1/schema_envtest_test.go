// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package v1alpha1_test

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	patchyv1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// startEnv boots an envtest API server with the generated CRDs installed and
// returns a client against it. Skipped without KUBEBUILDER_ASSETS (mise x --
// setup-envtest use 1.36.x -p path).
func startEnv(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; skipping envtest schema smoke")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../deploy/kustomize/base/crds"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	scheme := runtime.NewScheme()
	if err := patchyv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c
}

func TestSchemaValidation(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	t.Run("integration one-of rejects missing provider block", func(t *testing.T) {
		bad := &patchyv1.Integration{
			ObjectMeta: metav1.ObjectMeta{Name: "no-block", Namespace: "default"},
			Spec: patchyv1.IntegrationSpec{
				Provider:  patchyv1.IntegrationProviderGitHub,
				SecretRef: &patchyv1.LocalSecretReference{Name: "s"},
			},
		}
		if err := c.Create(ctx, bad); err == nil {
			t.Error("Create(integration without github block) = nil, want CEL rejection")
		}
	})

	t.Run("integration with matching block is accepted", func(t *testing.T) {
		good := &patchyv1.Integration{
			ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "default"},
			Spec: patchyv1.IntegrationSpec{
				Provider:  patchyv1.IntegrationProviderGitHub,
				SecretRef: &patchyv1.LocalSecretReference{Name: "s"},
				GitHub: &patchyv1.GitHubIntegration{
					Issues:             &patchyv1.GitHubIssues{Enabled: true},
					CodeScanningAlerts: &patchyv1.GitHubCodeScanningAlerts{Enabled: true},
				},
			},
		}
		if err := c.Create(ctx, good); err != nil {
			t.Errorf("Create(valid integration) = %v, want nil", err)
		}
	})

	t.Run("integration one-of rejects a mismatched provider block", func(t *testing.T) {
		bad := &patchyv1.Integration{
			ObjectMeta: metav1.ObjectMeta{Name: "gcp-with-github-block", Namespace: "default"},
			Spec: patchyv1.IntegrationSpec{
				Provider: patchyv1.IntegrationProviderGoogleCloud,
				GitHub:   &patchyv1.GitHubIntegration{},
			},
		}
		if err := c.Create(ctx, bad); err == nil {
			t.Error("Create(google-cloud integration with a github block) = nil, want CEL rejection")
		}
	})

	t.Run("google-cloud integration needs no secretRef", func(t *testing.T) {
		good := &patchyv1.Integration{
			ObjectMeta: metav1.ObjectMeta{Name: "gcp", Namespace: "default"},
			Spec: patchyv1.IntegrationSpec{
				Provider: patchyv1.IntegrationProviderGoogleCloud,
				GoogleCloud: &patchyv1.GoogleCloudIntegration{
					SecurityCommandCenter: &patchyv1.GoogleCloudSCC{
						Enabled:        true,
						Audience:       "https://patchy.example/google-cloud/webhooks",
						ServiceAccount: "scc-push@x.iam.gserviceaccount.com",
					},
				},
			},
		}
		if err := c.Create(ctx, good); err != nil {
			t.Errorf("Create(credential-less google-cloud integration) = %v, want nil", err)
		}
	})

	t.Run("github integration still requires a secretRef", func(t *testing.T) {
		bad := &patchyv1.Integration{
			ObjectMeta: metav1.ObjectMeta{Name: "gh-no-secret", Namespace: "default"},
			Spec: patchyv1.IntegrationSpec{
				Provider: patchyv1.IntegrationProviderGitHub,
				GitHub:   &patchyv1.GitHubIntegration{},
			},
		}
		if err := c.Create(ctx, bad); err == nil {
			t.Error("Create(github integration without secretRef) = nil, want CEL rejection")
		}
	})

	t.Run("finding rejects an unknown phase and accepts a legal one", func(t *testing.T) {
		f := &patchyv1.Finding{
			ObjectMeta: metav1.ObjectMeta{Name: "finding-abc123-1", Namespace: "default"},
			Spec: patchyv1.FindingSpec{
				IntegrationRef: patchyv1.LocalObjectReference{Name: "gh"},
				Source:         "github-code-scanning",
				Advisories:     []string{"CVE-2026-0001"},
				Severity:       patchyv1.LevelHigh,
			},
		}
		if err := c.Create(ctx, f); err != nil {
			t.Fatalf("Create(finding) = %v, want nil", err)
		}
		f.Status.Phase = "Bogus"
		if err := c.Status().Update(ctx, f); err == nil {
			t.Error("Status().Update(phase=Bogus) = nil, want enum rejection")
		}
		f.Status.Phase = patchyv1.PhaseOpened
		if err := c.Status().Update(ctx, f); err != nil {
			t.Errorf("Status().Update(phase=Opened) = %v, want nil", err)
		}
	})

	t.Run("investigation spec is immutable", func(t *testing.T) {
		inv := &patchyv1.Investigation{
			ObjectMeta: metav1.ObjectMeta{Name: "finding-abc123-1-inv-1", Namespace: "default"},
			Spec: patchyv1.InvestigationSpec{
				FindingRef: patchyv1.ObjectReference{Name: "finding-abc123-1"},
				Attempt:    1,
			},
		}
		if err := c.Create(ctx, inv); err != nil {
			t.Fatalf("Create(investigation) = %v, want nil", err)
		}
		inv.Spec.Attempt = 2
		if err := c.Update(ctx, inv); err == nil {
			t.Error("Update(investigation spec) = nil, want immutability rejection")
		}
	})

	t.Run("rollup scope is immutable", func(t *testing.T) {
		fr := &patchyv1.FindingRollup{
			ObjectMeta: metav1.ObjectMeta{Name: "total", Namespace: "default"},
			Spec: patchyv1.FindingRollupSpec{
				Scope: patchyv1.RollupScope{Type: patchyv1.ScopeTotal},
			},
		}
		if err := c.Create(ctx, fr); err != nil {
			t.Fatalf("Create(rollup) = %v, want nil", err)
		}
		fr.Spec.Scope.Type = patchyv1.ScopeRepository
		if err := c.Update(ctx, fr); err == nil {
			t.Error("Update(rollup scope) = nil, want immutability rejection")
		}
	})

	t.Run("usage cost pattern rejects non-decimal strings", func(t *testing.T) {
		inv := &patchyv1.Investigation{}
		if err := c.Get(ctx, client.ObjectKey{Name: "finding-abc123-1-inv-1", Namespace: "default"}, inv); err != nil {
			t.Fatalf("Get(investigation) = %v", err)
		}
		inv.Status.Confidence = "1.5"
		if err := c.Status().Update(ctx, inv); err == nil {
			t.Error("Status().Update(confidence=1.5) = nil, want pattern rejection")
		}
	})
}
