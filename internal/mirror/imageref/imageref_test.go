// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package imageref

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"nginx", "docker.io/library/nginx"},
		{"nginx:1.27", "docker.io/library/nginx:1.27"},
		{"grafana/grafana", "docker.io/grafana/grafana"},
		{"grafana/grafana:11.0.0", "docker.io/grafana/grafana:11.0.0"},
		{"docker.io/redis", "docker.io/library/redis"},
		{"docker.io/library/redis", "docker.io/library/redis"},
		{"ghcr.io/fluxcd/source-controller:v1.9.3", "ghcr.io/fluxcd/source-controller:v1.9.3"},
		{"registry.k8s.io/external-dns/external-dns:v0.15.0", "registry.k8s.io/external-dns/external-dns:v0.15.0"},
		{"localhost/app", "localhost/app"},
		{"localhost:5000/app", "localhost:5000/app"},
		{"quay.io/jetstack/cert-manager-controller", "quay.io/jetstack/cert-manager-controller"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Normalize(tt.in); got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		in      string
		want    Ref
		wantErr bool
	}{
		{in: "ghcr.io/acme/app", want: Ref{Repository: "ghcr.io/acme/app", Tag: "latest"}},
		{in: "ghcr.io/acme/app:v1.2.3", want: Ref{Repository: "ghcr.io/acme/app", Tag: "v1.2.3"}},
		{
			in:   "ghcr.io/acme/app:v1.2.3@sha256:abc123",
			want: Ref{Repository: "ghcr.io/acme/app", Tag: "v1.2.3", Digest: "sha256:abc123"},
		},
		{
			in:   "ghcr.io/acme/app@sha256:abc123",
			want: Ref{Repository: "ghcr.io/acme/app", Digest: "sha256:abc123"},
		},
		{
			in:   "localhost:5000/acme/app:v1",
			want: Ref{Repository: "localhost:5000/acme/app", Tag: "v1"},
		},
		{in: "localhost:5000/app", want: Ref{Repository: "localhost:5000/app", Tag: "latest"}},
		{in: "", wantErr: true},
		{in: "ghcr.io/acme/app:", wantErr: true},
		{in: "ghcr.io/acme/app@", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Parse(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseRoundTrip(t *testing.T) {
	for _, in := range []string{
		"ghcr.io/acme/app:v1.2.3",
		"ghcr.io/acme/app:v1.2.3@sha256:abc",
		"localhost:5000/app:v1",
	} {
		r, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got := r.String(); got != in {
			t.Errorf("round trip %q = %q", in, got)
		}
	}
}

func TestRewrite(t *testing.T) {
	rewrites := map[string]string{
		"docker.io": "mirror.example.com/dockerhub",
	}
	tests := []struct {
		in, want string
	}{
		{"docker.io/library/nginx:1.27", "mirror.example.com/dockerhub/library/nginx:1.27"},
		{"ghcr.io/acme/app:v1", "ghcr.io/acme/app:v1"},
		{"nohost", "nohost"},
	}
	for _, tt := range tests {
		if got := Rewrite(tt.in, rewrites); got != tt.want {
			t.Errorf("Rewrite(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := Rewrite("docker.io/library/nginx", nil); got != "docker.io/library/nginx" {
		t.Errorf("Rewrite with nil rewrites = %q", got)
	}
}

func TestPathMatch(t *testing.T) {
	tests := []struct {
		pattern, value string
		want           bool
	}{
		{"ghcr.io/fluxcd/*", "ghcr.io/fluxcd/source-controller", true},
		{"ghcr.io/fluxcd/*", "ghcr.io/fluxcd/source-controller:v1.9.3", true},
		// '*' stays within one segment.
		{"ghcr.io/fluxcd/*", "ghcr.io/fluxcd/team/app", false},
		{"ghcr.io/*", "ghcr.io/a/b/c", false},
		{"ghcr.io/acme*", "ghcr.io/acme-evil", true},
		{"ghcr.io/acme*/app", "ghcr.io/acme/evil/app", false},
		{"*", "anything/at/all", false},
		{"*", "docker.io", true},
		// '**' crosses segments.
		{"ghcr.io/**", "ghcr.io/a/b/c", true},
		{"ghcr.io/**", "ghcr.io/a", true},
		{"ghcr.io/**", "ghcr.io", false},
		{"**", "anything/at/all", true},
		{"ghcr.io/fluxcd/**", "ghcr.io/fluxcd-evil/app", false},
		// '**/' at a segment start also matches zero segments.
		{"ghcr.io/**/app", "ghcr.io/app", true},
		{"ghcr.io/**/app", "ghcr.io/a/b/app", true},
		{"ghcr.io/**/app", "ghcr.io/a/b/other", false},
		{"**/app", "app", true},
		{"**/app", "ghcr.io/x/app", true},
		// Mid-segment '**' does not get the zero-segment form.
		{"foo**/bar", "foobar", false},
		{"foo**/bar", "foo/bar", true},
		{"foo**/bar", "foo-x/y/bar", true},
		{"oci://ghcr.io/**", "oci://ghcr.io/acme/charts/app", true},
		{"https://charts.jetstack.io/*", "https://charts.jetstack.io/cert-manager", true},
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
		{"exact", "exact", true},
		{"exact", "exact2", false},
		{"", "", true},
		{"", "x", false},
	}
	for _, tt := range tests {
		if got := PathMatch(tt.pattern, tt.value); got != tt.want {
			t.Errorf("PathMatch(%q, %q) = %v, want %v", tt.pattern, tt.value, got, tt.want)
		}
	}
}
