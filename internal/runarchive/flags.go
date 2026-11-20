// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package runarchive

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/pflag"

	"github.com/bitwise-media-group/patchy/internal/archive"
)

// RegisterFlags declares the run controllers' archive flags: the shared store
// location plus the failure policy.
func RegisterFlags(f *pflag.FlagSet) {
	archive.RegisterFlags(f)
	f.Bool("archive-required", false,
		"fail a run whose archive cannot be written (default: fall back to the ConfigMap transcript)")
	f.Duration("archive-retry-window", DefaultRetryWindow,
		"how long after a Job finishes a failed archive upload is retried; must be below --job-ttl")
}

// Options reads resolved flag values.
type Options interface {
	String(key string) string
	Bool(key string) bool
	Duration(key string) time.Duration
}

// FromOptions opens the configured archive, or returns nil when archival is
// disabled. The retry window must close before the Job TTL removes the pod
// log the retry re-reads, so a window at or beyond jobTTL is refused.
func FromOptions(ctx context.Context, o Options, jobTTL time.Duration) (*Archiver, error) {
	cfg := archive.ConfigFrom(o)
	if !cfg.Enabled() {
		return nil, nil
	}
	window := o.Duration("archive-retry-window")
	if window <= 0 {
		window = DefaultRetryWindow
	}
	if jobTTL > 0 && window >= jobTTL {
		return nil, fmt.Errorf("--archive-retry-window %s must be below --job-ttl %s: the retry re-reads the "+
			"Job's log, which the TTL removes", window, jobTTL)
	}
	store, err := archive.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Archiver{Store: store, Config: cfg, Required: o.Bool("archive-required"), RetryWindow: window}, nil
}
