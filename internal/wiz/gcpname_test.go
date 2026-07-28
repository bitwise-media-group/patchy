// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package wiz

import "testing"

func TestNormalizeGCPName(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{
			"asset name passes through",
			"//storage.googleapis.com/projects/p/buckets/b",
			"//storage.googleapis.com/projects/p/buckets/b",
		},
		{
			"www self-link rewrites service and drops version",
			"https://www.googleapis.com/compute/v1/projects/p/zones/z/instances/i",
			"//compute.googleapis.com/projects/p/zones/z/instances/i",
		},
		{
			"service-host self-link drops version",
			"https://container.googleapis.com/v1/projects/p/locations/l/clusters/c",
			"//container.googleapis.com/projects/p/locations/l/clusters/c",
		},
		{
			"beta version segment is dropped",
			"https://www.googleapis.com/compute/v1beta1/projects/p/global/networks/n",
			"//compute.googleapis.com/projects/p/global/networks/n",
		},
		{
			"storage bucket shorthand loses the collection segment",
			"https://www.googleapis.com/storage/v1/b/acme-artifacts",
			"//storage.googleapis.com/acme-artifacts",
		},
		{
			"no version segment is tolerated",
			"https://sqladmin.googleapis.com/projects/p/instances/db",
			"//sqladmin.googleapis.com/projects/p/instances/db",
		},
		{
			"non-google url is untouched",
			"arn:aws:s3:::my-bucket",
			"arn:aws:s3:::my-bucket",
		},
		{
			"non-google https url is untouched",
			"https://example.com/v1/thing",
			"https://example.com/v1/thing",
		},
		{
			"bare id is untouched",
			"projects/p/instances/i",
			"projects/p/instances/i",
		},
		{
			"empty path is untouched",
			"https://www.googleapis.com/compute",
			"https://www.googleapis.com/compute",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeGCPName(tt.in); got != tt.want {
				t.Errorf("NormalizeGCPName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
