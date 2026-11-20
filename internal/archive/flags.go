// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package archive

import "github.com/spf13/pflag"

// RegisterFlags declares the store-location flags every binary that reads or
// writes the archive shares (PATCHY_ARCHIVE_* in the environment).
func RegisterFlags(f *pflag.FlagSet) {
	f.String("archive-provider", "", "run archive object store: s3 or gcs (empty disables archival)")
	f.String("archive-bucket", "", "run archive bucket")
	f.String("archive-prefix", "", "key prefix every archived run is written under")
	f.String("archive-region", "", "region of an S3 archive bucket (default: the SDK chain)")
	f.String("archive-endpoint", "", "archive service endpoint override, for S3-compatible stores")
	f.String("archive-kms-key", "", "KMS key every archive write is encrypted with (default: the bucket's)")
}

// StringGetter reads a resolved flag value.
type StringGetter interface {
	String(key string) string
}

// ConfigFrom reads the flags RegisterFlags declared.
func ConfigFrom(o StringGetter) Config {
	return Config{
		Provider: Provider(o.String("archive-provider")),
		Bucket:   o.String("archive-bucket"),
		Prefix:   o.String("archive-prefix"),
		Region:   o.String("archive-region"),
		Endpoint: o.String("archive-endpoint"),
		KMSKey:   o.String("archive-kms-key"),
	}
}
