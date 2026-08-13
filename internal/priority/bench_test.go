// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Tier-1 microbenchmark for the scheduling score — pure arithmetic that must
// stay O(1); it runs once per verdict routing and once per scheduler
// candidate build.

package priority

import (
	"testing"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// BenchmarkScore measures computing one finding's scheduling priority.
func BenchmarkScore(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		got := Score(v1alpha1.LevelHigh, v1alpha1.RatingHigh, v1alpha1.RatingMedium,
			v1alpha1.RatingCritical, DefaultWeights)
		if got == 0 {
			b.Fatal("score collapsed to zero")
		}
	}
}
