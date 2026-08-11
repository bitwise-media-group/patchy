// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scan

import (
	"context"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

func TestNormalizeSeverity(t *testing.T) {
	tests := map[string]string{
		"Critical": "CRITICAL", "HIGH": "HIGH", "medium": "MEDIUM",
		"Low": "LOW", "Negligible": "NEGLIGIBLE", "": "UNKNOWN", "weird": "UNKNOWN",
	}
	for in, want := range tests {
		if got := NormalizeSeverity(in); got != want {
			t.Errorf("NormalizeSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSeverityFromScore(t *testing.T) {
	tests := map[string]string{
		"9.8": "CRITICAL", "9.0": "CRITICAL", "7.5": "HIGH", "5.0": "MEDIUM",
		"1.2": "LOW", "0": "NEGLIGIBLE", "": "UNKNOWN", "n/a": "UNKNOWN",
	}
	for in, want := range tests {
		if got := SeverityFromScore(in); got != want {
			t.Errorf("SeverityFromScore(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuppressMatchesAliases(t *testing.T) {
	findings := []Finding{
		{ID: "CVE-2026-1111", Aliases: []string{"GHSA-aaaa-bbbb"}, Severity: "CRITICAL"},
		{ID: "GO-2026-4970", Severity: "HIGH"},
		{ID: "CVE-2026-9999", Severity: "HIGH"},
	}
	allow := &spec.Allowlist{Vulnerabilities: []spec.AllowlistEntry{
		// Written against the GHSA family; must match the CVE finding.
		{ID: "GHSA-aaaa-bbbb"},
		{ID: "go-2026-4970"}, // case-insensitive
	}}
	kept, suppressed := Suppress(findings, allow)
	if len(kept) != 1 || kept[0].ID != "CVE-2026-9999" {
		t.Errorf("kept = %+v", kept)
	}
	if len(suppressed) != 2 {
		t.Errorf("suppressed = %+v", suppressed)
	}
}

// cannedRunner returns fixed stdout for a tool.
type cannedRunner struct {
	stdout []byte
	err    error
	args   []string
	envs   []string
	have   bool
}

func (r *cannedRunner) Run(_ context.Context, _ string, args, env []string) ([]byte, error) {
	r.args = args
	r.envs = env
	return r.stdout, r.err
}

func (r *cannedRunner) Look(string) bool { return r.have }

const grypeJSON = `{
  "matches": [
    {
      "vulnerability": {
        "id": "GHSA-aaaa-bbbb",
        "severity": "Critical",
        "fix": {"versions": ["1.0.1", "2.0.0"]}
      },
      "relatedVulnerabilities": [{"id": "CVE-2026-1111"}],
      "artifact": {"name": "libfoo", "version": "1.0.0"}
    },
    {
      "vulnerability": {"id": "CVE-2026-2222", "severity": "Low", "fix": {}},
      "relatedVulnerabilities": [],
      "artifact": {"name": "libbar", "version": "2.0.0"}
    }
  ]
}`

func TestGrypeParse(t *testing.T) {
	runner := &cannedRunner{stdout: []byte(grypeJSON), have: true}
	g := &Grype{Runner: runner}
	findings, err := g.ScanImage(context.Background(), "example.test/app@sha256:abc")
	if err != nil {
		t.Fatalf("ScanImage: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v", findings)
	}
	f := findings[0]
	if f.ID != "GHSA-aaaa-bbbb" || f.Severity != "CRITICAL" || f.Package != "libfoo" ||
		strings.Join(f.FixedIn, ",") != "1.0.1,2.0.0" ||
		strings.Join(f.Aliases, ",") != "CVE-2026-1111" || !f.Fixed() {
		t.Errorf("finding = %+v", f)
	}
	if findings[1].Fixed() {
		t.Error("no-fix finding reports Fixed")
	}
	if strings.Join(runner.args, " ") != "--quiet -o json registry:example.test/app@sha256:abc" {
		t.Errorf("args = %v", runner.args)
	}
}

func TestGrypeMissingBinary(t *testing.T) {
	g := &Grype{Runner: &cannedRunner{have: false}}
	if _, err := g.ScanImage(context.Background(), "x"); err == nil ||
		!strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("want missing-binary error, got %v", err)
	}
}

func TestKubescape(t *testing.T) {
	t.Run("absent binary skips", func(t *testing.T) {
		res, err := Kubescape(context.Background(), &cannedRunner{have: false}, "rendered.yaml", "warn")
		if err != nil || res.Skipped == "" || res.Failed {
			t.Errorf("res = %+v, %v", res, err)
		}
	})
	t.Run("warn mode never fails", func(t *testing.T) {
		runner := &cannedRunner{have: true, stdout: []byte("findings"), err: context.Canceled}
		res, err := Kubescape(context.Background(), runner, "rendered.yaml", "warn")
		if err != nil || res.Failed {
			t.Errorf("res = %+v, %v", res, err)
		}
		// The scan must not see a kubeconfig.
		if len(runner.envs) != 1 || !strings.HasPrefix(runner.envs[0], "KUBECONFIG=") {
			t.Errorf("env = %v", runner.envs)
		}
	})
	t.Run("fail mode blocks", func(t *testing.T) {
		runner := &cannedRunner{have: true, err: context.Canceled}
		res, err := Kubescape(context.Background(), runner, "rendered.yaml", "fail")
		if err != nil || !res.Failed {
			t.Errorf("res = %+v, %v", res, err)
		}
		if strings.Join(runner.args, " ") != "scan rendered.yaml --logger warning --severity-threshold low" {
			t.Errorf("args = %v", runner.args)
		}
	})
}
