// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package spec

import (
	"strings"
	"testing"
)

func TestSourcePolicyCheck(t *testing.T) {
	mirror := &MirrorPolicy{
		Charts: PolicyRules{Allow: []string{"oci://ghcr.io/fluxcd-community/**", "https://charts.jetstack.io/*"}},
		Images: PolicyRules{
			Allow: []string{"registry.k8s.io/**", "docker.io/library/*", "ghcr.io/fluxcd/*"},
			Deny:  []string{"docker.io/library/busybox"},
		},
	}
	p := SourcePolicy{Layers: []PolicyLayer{mirror.Layer("test")}}
	tests := []struct {
		kind, ref string
		want      string // "" permitted, else a substring of the reason
	}{
		{SourceImage, "registry.k8s.io/pause:3.10", ""},
		{SourceImage, "registry.k8s.io/sig-storage/csi-attacher@sha256:abc", ""},
		// Shorthand normalizes before matching.
		{SourceImage, "nginx:1.27", ""},
		{SourceImage, "busybox", `denied by "docker.io/library/busybox"`},
		{SourceImage, "bitnami/nginx:1", "matches no allow pattern"},
		// '*' stays within a segment.
		{SourceImage, "ghcr.io/fluxcd/source-controller:v1", ""},
		{SourceImage, "ghcr.io/fluxcd/team/app:v1", "matches no allow pattern"},
		{SourceImage, "ghcr.io/fluxcd-evil/app:v1", "matches no allow pattern"},
		{SourceChart, ChartSource("oci://ghcr.io/fluxcd-community/charts", "flux2"), ""},
		{SourceChart, ChartSource("https://charts.jetstack.io/", "cert-manager"), ""},
		{SourceChart, ChartSource("https://charts.example.com", "app"), "matches no allow pattern"},
		// No artifact rules: unrestricted.
		{SourceArtifact, "anywhere.example.com/bundle", ""},
	}
	for _, tt := range tests {
		v := p.Check(tt.kind, tt.ref)
		switch {
		case tt.want == "" && v != nil:
			t.Errorf("Check(%s, %s) = %s, want permitted", tt.kind, tt.ref, v)
		case tt.want != "" && v == nil:
			t.Errorf("Check(%s, %s) permitted, want %q", tt.kind, tt.ref, tt.want)
		case tt.want != "" && !strings.Contains(v.Reason, tt.want):
			t.Errorf("Check(%s, %s) reason = %q, want %q", tt.kind, tt.ref, v.Reason, tt.want)
		}
	}
}

// TestSourcePolicyLayers: every layer must permit a source, and a source
// several layers reject is reported once — by a fail layer over a warn one.
func TestSourcePolicyLayers(t *testing.T) {
	enforced := (&BuildPolicy{Mode: PolicyWarn, Enforced: true,
		Images: PolicyRules{Allow: []string{"registry.example.com/**"}}}).Layer("toolchain, enforced")
	merged := (&BuildPolicy{Images: PolicyRules{
		Allow: []string{"**"}, Deny: []string{"docker.io/**"},
	}}).Layer("patchy config")
	p := SourcePolicy{Layers: []PolicyLayer{enforced, merged}}

	if v := p.Check(SourceImage, "registry.example.com/images/base:v1"); v != nil {
		t.Errorf("permitted by both layers, got %s", v)
	}
	// Merged allows everything not on docker.io, but the enforced layer
	// still narrows it (as a warning).
	v := p.Check(SourceImage, "ghcr.io/acme/base:v1")
	if v == nil || v.Source != "toolchain, enforced" || v.Blocking() {
		t.Errorf("ghcr.io: got %+v, want a warning from the enforced layer", v)
	}
	// Rejected by both: the fail (merged) layer wins over the warn one.
	v = p.Check(SourceImage, "alpine")
	if v == nil || v.Source != "patchy config" || !v.Blocking() {
		t.Errorf("alpine: got %+v, want a blocking violation from the merged layer", v)
	}
	if s := v.String(); !strings.Contains(s, "docker.io/library/alpine (alpine)") ||
		!strings.Contains(s, "images.allow/deny in patchy config") {
		t.Errorf("String() = %q", s)
	}

	if v := p.Reject(SourceImage, "${BASE}", "cannot be resolved"); v == nil || !v.Blocking() {
		t.Errorf("Reject = %+v, want blocking (the strictest restricting layer)", v)
	}
	if v := (SourcePolicy{}).Reject(SourceImage, "${BASE}", "x"); v != nil {
		t.Errorf("Reject with no policy = %+v, want nil", v)
	}
	if (SourcePolicy{}).Restricts(SourceImage) || !p.Restricts(SourceImage) || p.Restricts(SourceChart) {
		t.Error("Restricts mismatch")
	}
}

func TestSourcePolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		policy  MirrorPolicy
		wantErr string
	}{
		{name: "valid", policy: MirrorPolicy{
			Mode:   PolicyWarn,
			Charts: PolicyRules{Allow: []string{"oci://ghcr.io/**", "https://charts.example.com/*", "**"}},
			Images: PolicyRules{Allow: []string{"localhost:5000/**", "*.example.com/**", "**"},
				Deny: []string{"docker.io/**"}},
		}},
		{name: "mode", policy: MirrorPolicy{Mode: "block"}, wantErr: "mode must be fail or warn"},
		{name: "empty", policy: MirrorPolicy{Images: PolicyRules{Allow: []string{" "}}},
			wantErr: "images.allow[0]: empty pattern"},
		{name: "lone star", policy: MirrorPolicy{Images: PolicyRules{Deny: []string{"*"}}},
			wantErr: `images.deny[0]: "*" matches a single path segment`},
		{name: "tag", policy: MirrorPolicy{Images: PolicyRules{Allow: []string{"docker.io/library/nginx:1.27"}}},
			wantErr: "carries a tag"},
		{name: "digest", policy: MirrorPolicy{Artifacts: PolicyRules{Allow: []string{"ghcr.io/x@sha256:abc"}}},
			wantErr: "artifacts.allow[0]"},
		{name: "no host", policy: MirrorPolicy{Images: PolicyRules{Allow: []string{"library/nginx"}}},
			wantErr: "has no registry host"},
		{name: "image scheme", policy: MirrorPolicy{Images: PolicyRules{Allow: []string{"oci://ghcr.io/**"}}},
			wantErr: "drop the oci:// scheme"},
		{name: "schemeless chart", policy: MirrorPolicy{Charts: PolicyRules{Allow: []string{"ghcr.io/**"}}},
			wantErr: "must start with oci:// or https://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SourcePolicy{Layers: []PolicyLayer{tt.policy.Layer("layer.yaml")}}.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("Validate: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("Validate = %v, want %q", err, tt.wantErr)
			case err != nil && !strings.Contains(err.Error(), "policy (layer.yaml)"):
				t.Errorf("Validate error %q does not name the layer", err)
			}
		})
	}
}
