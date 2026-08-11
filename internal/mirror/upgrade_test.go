// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"reflect"
	"testing"
)

func TestDiffSnapshots(t *testing.T) {
	tests := []struct {
		name          string
		before, after map[string]string
		want          []string
	}{
		{"identical", map[string]string{"a": "1"}, map[string]string{"a": "1"}, nil},
		{"added", map[string]string{}, map[string]string{"a": "1"}, []string{"a"}},
		{"rewritten", map[string]string{"a": "1"}, map[string]string{"a": "2"}, []string{"a"}},
		// A removal is a change: an upgrade that deletes a vendored file
		// must never report "already converged".
		{"removed", map[string]string{"a": "1", "b": "2"}, map[string]string{"a": "1"}, []string{"b"}},
		{
			"mixed and sorted",
			map[string]string{"z": "1", "m": "2"},
			map[string]string{"m": "3", "a": "4"},
			[]string{"a", "m", "z"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diffSnapshots(tt.before, tt.after)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("diffSnapshots = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestResolveImagesSortedBySource pins the lock's deterministic ordering:
// resolution fans out concurrently, and the sort is the only thing keeping
// the committed lock reproducible — which the validate gate byte-compares.
func TestResolveImagesSortedBySource(t *testing.T) {
	f := newFixture(t)
	f.pushImage(f.host+"/apps/bbb:1.0.0", testNow.AddDate(0, -1, 0))
	f.pushImage(f.host+"/apps/aaa:1.0.0", testNow.AddDate(0, -1, 0))
	eng := f.engine()

	images, err := eng.resolveImages(context.Background(), "demo",
		[]string{"example.test/apps/bbb:1.0.0", "example.test/apps/aaa:1.0.0"})
	if err != nil {
		t.Fatalf("resolveImages: %v", err)
	}
	if len(images) != 2 || images[0].Source != "example.test/apps/aaa:1.0.0" ||
		images[1].Source != "example.test/apps/bbb:1.0.0" {
		t.Errorf("lock order = %+v, want sorted by source", images)
	}
}
