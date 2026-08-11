// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package scan

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/osv-scanner/v2/pkg/models"
	"github.com/google/osv-scanner/v2/pkg/osvscanner"
	osvschema "github.com/ossf/osv-schema/bindings/go/osvschema"
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

func TestOSVScanImageTriage(t *testing.T) {
	ref := pushTestImage(t)
	vulnResults := models.VulnerabilityResults{Results: []models.PackageSource{{
		Packages: []models.PackageVulns{{
			Package:         models.PackageInfo{Name: "libfoo", Version: "1.0.0"},
			Vulnerabilities: []*osvschema.Vulnerability{{Id: "CVE-2026-1"}},
		}},
	}}}
	tests := []struct {
		name    string
		results models.VulnerabilityResults
		err     error
		want    int
		wantErr bool
	}{
		{"clean scan", models.VulnerabilityResults{}, nil, 0, false},
		{"no packages is clean", models.VulnerabilityResults{}, osvscanner.ErrNoPackagesFound, 0, false},
		{"vulnerabilities sentinel keeps findings", vulnResults, osvscanner.ErrVulnerabilitiesFound, 1, false},
		{"hard failure", models.VulnerabilityResults{}, errors.New("api down"), 0, true},
		{"partial results do not mask the failure", vulnResults, errors.New("api down"), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got osvscanner.ScannerActions
			o := &OSV{doScan: func(a osvscanner.ScannerActions) (models.VulnerabilityResults, error) {
				got = a
				return tt.results, tt.err
			}}
			findings, err := o.ScanImage(context.Background(), ref)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ScanImage error = %v, wantErr %v", err, tt.wantErr)
			}
			if len(findings) != tt.want {
				t.Errorf("findings = %+v, want %d", findings, tt.want)
			}
			// The archive must reach the scanner as a local image tarball;
			// anything else and osv-scanner scans nothing.
			if !got.IsImageArchive || got.Image == "" {
				t.Errorf("scanner actions = %+v, want a local image archive", got)
			}
		})
	}
}

func TestOSVFindings(t *testing.T) {
	results := models.VulnerabilityResults{Results: []models.PackageSource{{
		Packages: []models.PackageVulns{{
			Package: models.PackageInfo{Name: "libfoo", Version: "1.0.0"},
			Groups: []models.GroupInfo{{
				IDs:         []string{"CVE-2026-1", "GHSA-aaaa"},
				Aliases:     []string{"CVE-2026-1", "GHSA-aaaa", "GO-2026-0001"},
				MaxSeverity: "9.8",
			}},
			Vulnerabilities: []*osvschema.Vulnerability{
				{Id: "GHSA-aaaa", Aliases: []string{"CVE-2026-1"}},
				// No group carries this one: severity must stay UNKNOWN
				// rather than silently degrading to a passing level.
				{Id: "CVE-2026-9", Aliases: nil},
			},
		}},
	}}}
	findings := osvFindings(results)
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
	if ungrouped := findings[0]; ungrouped.Severity != "UNKNOWN" {
		t.Errorf("ungrouped severity = %q, want UNKNOWN", ungrouped.Severity)
	}
}

func TestOSVFixedVersions(t *testing.T) {
	vuln := &osvschema.Vulnerability{
		Id: "CVE-2026-1",
		Affected: []*osvschema.Affected{
			{
				Package: &osvschema.Package{Name: "libfoo"},
				Ranges: []*osvschema.Range{
					{Events: []*osvschema.Event{{Introduced: "0"}, {Fixed: "1.0.1"}}},
					{Events: []*osvschema.Event{{Fixed: "1.2.0"}, {Fixed: "1.0.1"}}},
				},
			},
			// A different package's fix must not leak into libfoo's.
			{
				Package: &osvschema.Package{Name: "libbar"},
				Ranges:  []*osvschema.Range{{Events: []*osvschema.Event{{Fixed: "9.9.9"}}}},
			},
			// An advisory without a package name applies to every package.
			{
				Ranges: []*osvschema.Range{{Events: []*osvschema.Event{{Fixed: "2.0.0"}}}},
			},
		},
	}
	got := osvFixedVersions(vuln, "libfoo")
	want := []string{"1.0.1", "1.2.0", "2.0.0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("osvFixedVersions = %v, want %v", got, want)
	}
}
