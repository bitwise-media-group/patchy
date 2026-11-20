// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package archive

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// gcsAPI is the slice of the storage client the backend uses. The SDK's
// handles are concrete chains (bucket → object → writer), so the seam is
// drawn one level up, at whole operations.
type gcsAPI interface {
	// create writes body with attrs, only if the object does not exist.
	create(ctx context.Context, bucket, key string, body []byte, attrs storage.ObjectAttrs) error
	attrs(ctx context.Context, bucket, key string) (*storage.ObjectAttrs, error)
	reader(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	signedURL(bucket, key string, opts *storage.SignedURLOptions) (string, error)
	close() error
}

// gcsBackend writes to one GCS bucket.
type gcsBackend struct {
	api    gcsAPI
	bucket string
	kmsKey string
}

// newGCS builds the backend from Application Default Credentials (workload
// identity in-cluster, gcloud ADC on a workstation).
func newGCS(ctx context.Context, cfg Config) (*gcsBackend, error) {
	var opts []option.ClientOption
	if cfg.Endpoint != "" {
		opts = append(opts, option.WithEndpoint(cfg.Endpoint))
	}
	c, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("archive: gcs client: %w", err)
	}
	return &gcsBackend{api: gcsClient{c}, bucket: cfg.Bucket, kmsKey: cfg.KMSKey}, nil
}

func (b *gcsBackend) create(ctx context.Context, key string, body []byte, contentType, sum string) error {
	attrs := storage.ObjectAttrs{
		ContentType: contentType,
		Metadata:    map[string]string{MetadataSHA256: sum},
		KMSKeyName:  b.kmsKey,
		// GCS verifies the body against this before committing the object.
		CRC32C: crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli)),
	}
	if err := b.api.create(ctx, b.bucket, key, body, attrs); err != nil {
		if gcsStatus(err) == http.StatusPreconditionFailed {
			return errExists
		}
		return fmt.Errorf("archive: put gs://%s/%s: %w", b.bucket, key, err)
	}
	return nil
}

func (b *gcsBackend) storedSHA256(ctx context.Context, key string) (string, error) {
	a, err := b.api.attrs(ctx, b.bucket, key)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("archive: stat gs://%s/%s: %w", b.bucket, key, err)
	}
	return a.Metadata[MetadataSHA256], nil
}

func (b *gcsBackend) get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := b.api.reader(ctx, b.bucket, key)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("archive: get gs://%s/%s: %w", b.bucket, key, err)
	}
	return rc, nil
}

// presign signs a V4 GET URL. With no key file (workload identity, ADC) the
// SDK signs through the IAM signBlob API, so the signing identity needs
// iam.serviceAccounts.signBlob on itself.
func (b *gcsBackend) presign(_ context.Context, key string, ttl time.Duration, attachment string) (string, error) {
	opts := &storage.SignedURLOptions{
		Scheme:  storage.SigningSchemeV4,
		Method:  http.MethodGet,
		Expires: time.Now().Add(ttl),
	}
	if cd := contentDisposition(attachment); cd != "" {
		opts.QueryParameters = url.Values{"response-content-disposition": {cd}}
	}
	u, err := b.api.signedURL(b.bucket, key, opts)
	if err != nil {
		return "", fmt.Errorf("archive: presign gs://%s/%s: %w", b.bucket, key, err)
	}
	return u, nil
}

func (b *gcsBackend) close() error { return b.api.close() }

// gcsStatus extracts the HTTP status from a storage error, or 0.
func gcsStatus(err error) int {
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		return ge.Code
	}
	return 0
}

// gcsClient is the real gcsAPI.
type gcsClient struct{ c *storage.Client }

func (g gcsClient) create(ctx context.Context, bucket, key string, body []byte, attrs storage.ObjectAttrs) error {
	obj := g.c.Bucket(bucket).Object(key).If(storage.Conditions{DoesNotExist: true})
	w := obj.NewWriter(ctx)
	w.ContentType = attrs.ContentType
	w.Metadata = attrs.Metadata
	w.KMSKeyName = attrs.KMSKeyName
	w.CRC32C = attrs.CRC32C
	w.SendCRC32C = true
	// One request: archive objects are bounded and small enough that a
	// resumable session buys nothing.
	w.ChunkSize = 0
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func (g gcsClient) attrs(ctx context.Context, bucket, key string) (*storage.ObjectAttrs, error) {
	return g.c.Bucket(bucket).Object(key).Attrs(ctx)
}

func (g gcsClient) reader(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	return g.c.Bucket(bucket).Object(key).NewReader(ctx)
}

func (g gcsClient) signedURL(bucket, key string, opts *storage.SignedURLOptions) (string, error) {
	return g.c.Bucket(bucket).SignedURL(key, opts)
}

func (g gcsClient) close() error { return g.c.Close() }
