// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
)

// pushTestImage stands up an in-memory registry with one random image and
// returns its tagged reference.
func pushTestImage(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	img, err := random.Image(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref := u.Host + "/apps/app:1.0.0"
	if err := crane.Push(img, ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

// exitErr fabricates a genuine *exec.ExitError carrying the given code.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("could not fabricate exit code %d: %v", code, err)
	}
	return err
}

const osvVulnJSON = `{
  "results": [
    {
      "packages": [
        {
          "package": {"name": "libfoo", "version": "1.0.0"},
          "vulnerabilities": [{"id": "CVE-2026-1"}]
        }
      ]
    }
  ]
}`

func TestOSVScanImageTriage(t *testing.T) {
	ref := pushTestImage(t)
	tests := []struct {
		name    string
		stdout  string
		err     error
		want    int
		wantErr bool
	}{
		{"clean scan", `{"results": []}`, nil, 0, false},
		{"no packages is clean", "", exitErr(t, 128), 0, false},
		{"vulnerabilities sentinel keeps findings", osvVulnJSON, exitErr(t, 1), 1, false},
		{"hard failure", "", exitErr(t, 127), 0, true},
		{"api failure", "", exitErr(t, 129), 0, true},
		{"non-exec failure", "", errors.New("api down"), 0, true},
		{"partial results do not mask the failure", osvVulnJSON, exitErr(t, 130), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &cannedRunner{stdout: []byte(tt.stdout), err: tt.err, have: true}
			o := &OSV{Runner: runner}
			findings, err := o.ScanImage(context.Background(), ref)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ScanImage error = %v, wantErr %v", err, tt.wantErr)
			}
			if len(findings) != tt.want {
				t.Errorf("findings = %+v, want %d", findings, tt.want)
			}
			// The archive must reach the scanner as a local image tarball;
			// anything else and osv-scanner scans nothing.
			if len(runner.args) < 6 || runner.args[0] != "scan" || runner.args[1] != "image" ||
				runner.args[2] != "--archive" || runner.args[3] != "--format" || runner.args[4] != "json" ||
				!strings.HasSuffix(runner.args[len(runner.args)-1], "image.tar") {
				t.Errorf("args = %q, want scan image --archive --format json <archive>", runner.args)
			}
		})
	}
}

func TestOSVScanImageMissingBinary(t *testing.T) {
	runner := &cannedRunner{have: false}
	o := &OSV{Runner: runner}
	_, err := o.ScanImage(context.Background(), "example.test/app@sha256:abc")
	if err == nil || !strings.Contains(err.Error(), "not on PATH") ||
		!strings.Contains(err.Error(), "scan.scanners.osv.enabled") {
		t.Fatalf("ScanImage error = %v, want the missing-binary hint naming the config toggle", err)
	}
}

const osvFindingsJSON = `{
  "results": [
    {
      "packages": [
        {
          "package": {"name": "libfoo", "version": "1.0.0"},
          "groups": [
            {
              "ids": ["CVE-2026-1", "GHSA-aaaa"],
              "aliases": ["CVE-2026-1", "GHSA-aaaa", "GO-2026-0001"],
              "max_severity": "9.8"
            }
          ],
          "vulnerabilities": [
            {"id": "GHSA-aaaa", "aliases": ["CVE-2026-1"]},
            {"id": "CVE-2026-9"}
          ]
        }
      ]
    }
  ]
}`

func TestOSVFindings(t *testing.T) {
	var out osvOutput
	if err := json.Unmarshal([]byte(osvFindingsJSON), &out); err != nil {
		t.Fatal(err)
	}
	findings := osvFindings(out)
	if len(findings) != 2 {
		t.Fatalf("findings = %+v", findings)
	}
	// Sorted by ID: CVE-2026-9 before GHSA-aaaa.
	if findings[0].ID != "CVE-2026-9" || findings[1].ID != "GHSA-aaaa" {
		t.Errorf("order = %s, %s", findings[0].ID, findings[1].ID)
	}
	grouped := findings[1]
	if grouped.Severity != "CRITICAL" {
		t.Errorf("grouped severity = %q, want CRITICAL (from group MaxSeverity)", grouped.Severity)
	}
	wantAliases := []string{"CVE-2026-1", "GO-2026-0001"}
	if !reflect.DeepEqual(grouped.Aliases, wantAliases) {
		t.Errorf("aliases = %v, want %v (deduped, own ID excluded)", grouped.Aliases, wantAliases)
	}
	// No group carries CVE-2026-9: severity must stay UNKNOWN rather than
	// silently degrading to a passing level.
	if ungrouped := findings[0]; ungrouped.Severity != "UNKNOWN" {
		t.Errorf("ungrouped severity = %q, want UNKNOWN", ungrouped.Severity)
	}
}

const osvFixedJSON = `{
  "id": "CVE-2026-1",
  "affected": [
    {
      "package": {"name": "libfoo"},
      "ranges": [
        {"events": [{"introduced": "0"}, {"fixed": "1.0.1"}]},
        {"events": [{"fixed": "1.2.0"}, {"fixed": "1.0.1"}]}
      ]
    },
    {
      "package": {"name": "libbar"},
      "ranges": [{"events": [{"fixed": "9.9.9"}]}]
    },
    {
      "ranges": [{"events": [{"fixed": "2.0.0"}]}]
    }
  ]
}`

func TestOSVFixedVersions(t *testing.T) {
	var vuln osvVulnerability
	if err := json.Unmarshal([]byte(osvFixedJSON), &vuln); err != nil {
		t.Fatal(err)
	}
	// A different package's fix must not leak into libfoo's; an advisory
	// without a package name applies to every package.
	got := osvFixedVersions(vuln, "libfoo")
	want := []string{"1.0.1", "1.2.0", "2.0.0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("osvFixedVersions = %v, want %v", got, want)
	}
}
