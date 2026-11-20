// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

// Provider names an object-store backend.
type Provider string

// The supported providers.
const (
	ProviderS3  Provider = "s3"
	ProviderGCS Provider = "gcs"
)

// MaxPresignTTL is the longest a presigned URL may live. Both S3 SigV4 and
// GCS V4 signing cap expiry at seven days.
const MaxPresignTTL = 7 * 24 * time.Hour

// MetadataSHA256 is the user-metadata key every object carries its body's
// hex sha256 under (x-amz-meta-sha256 on S3, metadata.sha256 on GCS). It is
// what makes a create-only retry idempotent without re-reading the body.
const MetadataSHA256 = "sha256"

var (
	// ErrNotFound reports an object that does not exist.
	ErrNotFound = errors.New("archive: object not found")
	// ErrConflict reports a create-only write that found a different object
	// already at the key. It is never retried: the archive is immutable, so
	// a different body means the caller is not writing what it wrote before.
	ErrConflict = errors.New("archive: object exists with different content")

	// errExists is a backend's report that the create-only precondition
	// failed; Put turns it into success or ErrConflict.
	errExists = errors.New("archive: object exists")
)

// Store is a create-only object store.
type Store interface {
	// Put writes body at key exactly once. Writing the same bytes again
	// succeeds; writing different bytes fails with ErrConflict.
	Put(ctx context.Context, key string, body []byte, contentType string) error
	// Get opens an object for reading; ErrNotFound when it is absent.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Presign returns a URL that GETs key without credentials until it
	// expires. A non-empty attachment asks the browser to download the
	// object under that file name instead of displaying it.
	Presign(ctx context.Context, key string, ttl time.Duration,
		attachment string) (url string, expires time.Time, err error)
	// Close releases the client.
	Close() error
}

// Config locates the bucket and the key prefix runs are archived under. An
// empty Provider means archival is disabled.
type Config struct {
	// Provider is s3 or gcs; empty disables archival.
	Provider Provider
	// Bucket name.
	Bucket string
	// Prefix every key starts with (no leading or trailing slash needed).
	Prefix string
	// Region of an S3 bucket; empty uses the SDK default chain.
	Region string
	// Endpoint overrides the service endpoint, for S3-compatible stores and
	// tests. On S3 it also switches to path-style addressing.
	Endpoint string
	// KMSKey encrypts each write with this key (an S3 KMS key id or ARN, or
	// a GCS CryptoKey resource name). Empty leaves the bucket default.
	KMSKey string
}

// Enabled reports whether archival is configured.
func (c Config) Enabled() bool { return c.Provider != "" }

// Validate checks an enabled configuration is complete.
func (c Config) Validate() error {
	switch c.Provider {
	case "":
		return nil
	case ProviderS3, ProviderGCS:
	default:
		return fmt.Errorf("archive: provider %q is not s3 or gcs", c.Provider)
	}
	if c.Bucket == "" {
		return errors.New("archive: bucket is required")
	}
	if strings.ContainsAny(c.Bucket, "/ ") {
		return fmt.Errorf("archive: bucket %q is not a bucket name", c.Bucket)
	}
	return nil
}

// Key joins the configured prefix with parts into an object key.
func (c Config) Key(parts ...string) string {
	return Join(append([]string{c.Prefix}, parts...)...)
}

// Join builds an object key from parts, dropping empty parts and stray
// slashes so a prefix of "", "audit" or "/audit/" all behave alike.
func Join(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.Trim(p, "/"); p != "" {
			kept = append(kept, p)
		}
	}
	return path.Join(kept...)
}

// Location is everything needed to find one object without any patchy
// configuration: a CLI holding only the run's status can open the store and
// read the key with the caller's own credentials.
type Location struct {
	Provider Provider
	Bucket   string
	Region   string
	Endpoint string
	Key      string
}

// Config is the store configuration that reaches this location.
func (l Location) Config() Config {
	return Config{Provider: l.Provider, Bucket: l.Bucket, Region: l.Region, Endpoint: l.Endpoint}
}

// URI renders the location as s3://bucket/key or gs://bucket/key.
func (l Location) URI() string {
	scheme := string(l.Provider)
	if l.Provider == ProviderGCS {
		scheme = "gs"
	}
	return scheme + "://" + l.Bucket + "/" + l.Key
}

// Open connects to the configured store using the platform's default
// credential chain. It does not touch the bucket: a store that cannot be
// reached surfaces on the first write, where the caller's retry policy
// applies.
func Open(ctx context.Context, cfg Config) (Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch cfg.Provider {
	case ProviderS3:
		b, err := newS3(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return &store{b: b, now: time.Now}, nil
	case ProviderGCS:
		b, err := newGCS(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return &store{b: b, now: time.Now}, nil
	default:
		return nil, errors.New("archive: archival is disabled")
	}
}

// SHA256 is the hex digest the archive records for body.
func SHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// backend is what each provider implements; store adds the shared
// create-only and idempotency semantics on top.
type backend interface {
	// create writes body only if key is absent, returning errExists when
	// the precondition fails.
	create(ctx context.Context, key string, body []byte, contentType, sum string) error
	// storedSHA256 reads the sha256 metadata of an existing object; empty
	// when the object carries none. ErrNotFound when it is absent.
	storedSHA256(ctx context.Context, key string) (string, error)
	get(ctx context.Context, key string) (io.ReadCloser, error)
	presign(ctx context.Context, key string, ttl time.Duration, attachment string) (string, error)
	close() error
}

// store implements Store over a backend.
type store struct {
	b   backend
	now func() time.Time
}

func (s *store) Put(ctx context.Context, key string, body []byte, contentType string) error {
	sum := SHA256(body)
	err := s.b.create(ctx, key, body, contentType, sum)
	if !errors.Is(err, errExists) {
		return err
	}
	stored, err := s.b.storedSHA256(ctx, key)
	if err != nil {
		return fmt.Errorf("archive: compare existing %s: %w", key, err)
	}
	if stored == "" {
		// Written by something that did not record a digest: hash the body.
		rc, err := s.b.get(ctx, key)
		if err != nil {
			return fmt.Errorf("archive: compare existing %s: %w", key, err)
		}
		defer func() { _ = rc.Close() }()
		h := sha256.New()
		if _, err := io.Copy(h, rc); err != nil {
			return fmt.Errorf("archive: compare existing %s: %w", key, err)
		}
		stored = hex.EncodeToString(h.Sum(nil))
	}
	if stored != sum {
		return fmt.Errorf("%w: %s", ErrConflict, key)
	}
	return nil
}

func (s *store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.b.get(ctx, key)
}

func (s *store) Presign(
	ctx context.Context, key string, ttl time.Duration, attachment string,
) (string, time.Time, error) {
	if ttl <= 0 || ttl > MaxPresignTTL {
		return "", time.Time{}, fmt.Errorf("archive: presign ttl %s is outside (0, %s]", ttl, MaxPresignTTL)
	}
	exp := s.now().Add(ttl)
	u, err := s.b.presign(ctx, key, ttl, attachment)
	if err != nil {
		return "", time.Time{}, err
	}
	return u, exp, nil
}

func (s *store) Close() error { return s.b.close() }

// contentDisposition renders an attachment header value, or empty.
func contentDisposition(attachment string) string {
	if attachment == "" {
		return ""
	}
	return fmt.Sprintf("attachment; filename=%q", path.Base(attachment))
}
