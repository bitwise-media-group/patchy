// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package kube

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// testKubeconfig is a two-context file: dev declares a namespace, prod does
// not, which is the distinction ClientConfig has to get right.
const testKubeconfig = `apiVersion: v1
kind: Config
current-context: dev
clusters:
  - name: dev-cluster
    cluster:
      server: https://dev.example.test:6443
  - name: prod-cluster
    cluster:
      server: https://prod.example.test:6443
contexts:
  - name: dev
    context:
      cluster: dev-cluster
      user: dev-user
      namespace: patchy-dev
  - name: prod
    context:
      cluster: prod-cluster
      user: prod-user
users:
  - name: dev-user
    user:
      token: dev-token
  - name: prod-user
    user:
      token: prod-token
`

// writeKubeconfig drops the fixture in a temp dir and returns its path.
func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

func TestClientConfig(t *testing.T) {
	path := writeKubeconfig(t)

	cases := []struct {
		name      string
		context   string
		wantHost  string
		wantNS    string
		wantToken string
	}{
		{
			name:      "current context",
			wantHost:  "https://dev.example.test:6443",
			wantNS:    "patchy-dev",
			wantToken: "dev-token",
		},
		{
			name:      "explicit context override",
			context:   "prod",
			wantHost:  "https://prod.example.test:6443",
			wantToken: "prod-token",
			// kubectl reports "default" for a context declaring no namespace;
			// matching that keeps patchy and kubectl pointed at the same place.
			wantNS: "default",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ns, err := ClientConfig(path, tc.context)
			if err != nil {
				t.Fatalf("ClientConfig: %v", err)
			}
			if cfg.Host != tc.wantHost {
				t.Errorf("host = %q, want %q", cfg.Host, tc.wantHost)
			}
			if ns != tc.wantNS {
				t.Errorf("namespace = %q, want %q", ns, tc.wantNS)
			}
			if cfg.BearerToken != tc.wantToken {
				t.Errorf("token = %q, want %q", cfg.BearerToken, tc.wantToken)
			}
		})
	}
}

func TestClientConfigUnknownContext(t *testing.T) {
	if _, _, err := ClientConfig(writeKubeconfig(t), "nope"); err == nil {
		t.Fatal("ClientConfig accepted an undefined context")
	}
}

func TestClientConfigMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	if _, _, err := ClientConfig(missing, ""); err == nil {
		t.Fatal("ClientConfig accepted a missing kubeconfig path")
	}
}

// TestClientConfigHonoursKubeconfigEnv covers the no-explicit-path case: the
// CLI's --kubeconfig is optional, so $KUBECONFIG has to be what answers.
func TestClientConfigHonoursKubeconfigEnv(t *testing.T) {
	t.Setenv("KUBECONFIG", writeKubeconfig(t))
	cfg, ns, err := ClientConfig("", "")
	if err != nil {
		t.Fatalf("ClientConfig: %v", err)
	}
	if cfg.Host != "https://dev.example.test:6443" {
		t.Errorf("host = %q, want the KUBECONFIG cluster", cfg.Host)
	}
	if ns != "patchy-dev" {
		t.Errorf("namespace = %q, want patchy-dev", ns)
	}
}

// TestScheme covers the kinds the CLI builds a client for; a scheme missing
// one of them fails at first use with an unhelpful "no kind registered".
func TestScheme(t *testing.T) {
	s := Scheme()
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "patchy.bitwisemedia.uk", Version: "v1alpha1", Kind: "Finding"},
		{Group: "patchy.bitwisemedia.uk", Version: "v1alpha1", Kind: "Investigation"},
		{Group: "patchy.bitwisemedia.uk", Version: "v1alpha1", Kind: "Remediation"},
		{Group: "patchy.bitwisemedia.uk", Version: "v1alpha1", Kind: "FindingRollup"},
		{Group: "batch", Version: "v1", Kind: "Job"},
		{Version: "v1", Kind: "Secret"},
	} {
		if !s.Recognizes(gvk) {
			t.Errorf("scheme does not recognize %s", gvk)
		}
	}
}
