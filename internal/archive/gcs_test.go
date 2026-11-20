// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

// fakeGCS is a gcsAPI over a map, honouring DoesNotExist like GCS.
type fakeGCS struct {
	objects map[string][]byte
	attrsOf map[string]storage.ObjectAttrs
	writes  []storage.ObjectAttrs
	signed  *storage.SignedURLOptions
}

func newFakeGCS() *fakeGCS {
	return &fakeGCS{objects: map[string][]byte{}, attrsOf: map[string]storage.ObjectAttrs{}}
}

func (f *fakeGCS) create(_ context.Context, bucket, key string, body []byte, attrs storage.ObjectAttrs) error {
	f.writes = append(f.writes, attrs)
	if _, ok := f.objects[bucket+"/"+key]; ok {
		return &googleapi.Error{Code: http.StatusPreconditionFailed}
	}
	f.objects[bucket+"/"+key] = bytes.Clone(body)
	f.attrsOf[bucket+"/"+key] = attrs
	return nil
}

func (f *fakeGCS) attrs(_ context.Context, bucket, key string) (*storage.ObjectAttrs, error) {
	a, ok := f.attrsOf[bucket+"/"+key]
	if !ok {
		return nil, storage.ErrObjectNotExist
	}
	return &a, nil
}

func (f *fakeGCS) reader(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	b, ok := f.objects[bucket+"/"+key]
	if !ok {
		return nil, storage.ErrObjectNotExist
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeGCS) signedURL(bucket, key string, opts *storage.SignedURLOptions) (string, error) {
	f.signed = opts
	return "https://storage.googleapis.com/" + bucket + "/" + key + "?X-Goog-Signature=sig", nil
}

func (f *fakeGCS) close() error { return nil }

func newTestGCSStore(f *fakeGCS, kms string) *store {
	return &store{b: &gcsBackend{api: f, bucket: "bucket", kmsKey: kms}, now: time.Now}
}

func TestGCSPutCreateOnly(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		seed    []byte
		seedSum string
		wantErr error
	}{
		{name: "new object"},
		{name: "retry with the same bytes", seed: []byte("hello"), seedSum: SHA256([]byte("hello"))},
		{name: "same bytes without digest metadata", seed: []byte("hello")},
		{name: "different bytes", seed: []byte("other"), seedSum: SHA256([]byte("other")), wantErr: ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeGCS()
			if tt.seed != nil {
				f.objects["bucket/k"] = tt.seed
				f.attrsOf["bucket/k"] = storage.ObjectAttrs{Metadata: map[string]string{MetadataSHA256: tt.seedSum}}
			}
			err := newTestGCSStore(f, "").Put(ctx, "k", []byte("hello"), "text/plain")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Put() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestGCSPutAttrs(t *testing.T) {
	f := newFakeGCS()
	kms := "projects/p/locations/l/keyRings/r/cryptoKeys/k"
	if err := newTestGCSStore(f, kms).Put(context.Background(), "k", []byte("body"), "application/gzip"); err != nil {
		t.Fatal(err)
	}
	a := f.writes[0]
	if a.ContentType != "application/gzip" || a.ContentEncoding != "" {
		t.Errorf("content type/encoding = %q/%q, want application/gzip with no transcoding",
			a.ContentType, a.ContentEncoding)
	}
	if a.KMSKeyName != kms {
		t.Errorf("KMSKeyName = %q", a.KMSKeyName)
	}
	if a.Metadata[MetadataSHA256] != SHA256([]byte("body")) || a.CRC32C == 0 {
		t.Errorf("integrity attrs = %v / %d", a.Metadata, a.CRC32C)
	}
}

func TestGCSGetAndPresign(t *testing.T) {
	f := newFakeGCS()
	f.objects["bucket/k"] = []byte("data")
	s := newTestGCSStore(f, "")
	if _, err := s.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
	u, _, err := s.Presign(context.Background(), "k", time.Minute, "report.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := url.Parse(u); err != nil {
		t.Errorf("url %q: %v", u, err)
	}
	if f.signed.Method != http.MethodGet || f.signed.Scheme != storage.SigningSchemeV4 {
		t.Errorf("signed options = %+v, want a V4 GET", f.signed)
	}
	if got := f.signed.QueryParameters.Get("response-content-disposition"); got != `attachment; filename="report.md"` {
		t.Errorf("response-content-disposition = %q", got)
	}
}

func TestConfig(t *testing.T) {
	tests := []struct {
		cfg     Config
		wantErr bool
	}{
		{cfg: Config{}},
		{cfg: Config{Provider: ProviderS3, Bucket: "b"}},
		{cfg: Config{Provider: ProviderGCS, Bucket: "b"}},
		{cfg: Config{Provider: "azure", Bucket: "b"}, wantErr: true},
		{cfg: Config{Provider: ProviderS3}, wantErr: true},
		{cfg: Config{Provider: ProviderS3, Bucket: "a/b"}, wantErr: true},
	}
	for _, tt := range tests {
		if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
			t.Errorf("Validate(%+v) error = %v, wantErr %v", tt.cfg, err, tt.wantErr)
		}
	}
	if got := (Config{Prefix: "/audit/"}).Key("ns", "", "run/"); got != "audit/ns/run" {
		t.Errorf("Key() = %q", got)
	}
	if got := (Location{Provider: ProviderGCS, Bucket: "b", Key: "k"}).URI(); got != "gs://b/k" {
		t.Errorf("URI() = %q", got)
	}
}
