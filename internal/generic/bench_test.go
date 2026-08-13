// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Tier-1 microbenchmark for decoding and validating a generic findings
// delivery — the per-batch cost in front of ingest when an external process
// bulk-delivers a brownfield backlog.

package generic

import (
	"encoding/json"
	"fmt"
	"testing"

	pkggeneric "github.com/bitwise-media-group/patchy/pkg/generic"
)

// benchPayload is a findings delivery of n findings, marshalled once.
func benchPayload(tb testing.TB, n int) []byte {
	tb.Helper()
	p := pkggeneric.Payload{Version: pkggeneric.Version, Event: pkggeneric.EventFindings}
	for i := range n {
		p.Findings = append(p.Findings, pkggeneric.Finding{
			Repo:        &pkggeneric.Repo{Owner: "loadgen", Name: fmt.Sprintf("repo-%d", i%100)},
			AlertID:     fmt.Sprintf("lg-%d", i),
			Advisories:  []string{fmt.Sprintf("CVE-2026-%06d", i)},
			RuleID:      fmt.Sprintf("rule/%d", i%50),
			Title:       fmt.Sprintf("Synthetic finding %d", i),
			Description: "A synthetic finding description of plausible length for a scanner batch delivery.",
			Severity:    "high",
			HTMLURL:     fmt.Sprintf("https://scanner.example/alerts/%d", i),
		})
	}
	out, err := json.Marshal(p)
	if err != nil {
		tb.Fatal(err)
	}
	return out
}

// BenchmarkGenericDecode measures validating and mapping one findings
// delivery across batch sizes (the 25MB body cap admits ~10k findings).
func BenchmarkGenericDecode(b *testing.B) {
	src := NewSource("loadgen", Options{})
	for _, n := range []int{100, 1_000, 10_000} {
		payload := benchPayload(b, n)
		b.Run(fmt.Sprintf("findings=%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			for b.Loop() {
				out, err := src.Findings(b.Context(), EventFindings, payload)
				if err != nil || len(out) != n {
					b.Fatalf("decoded %d findings, err %v", len(out), err)
				}
			}
		})
	}
}
