// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package archive

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// fakeS3 is a path-style S3 endpoint holding objects in memory, honouring
// If-None-Match: * the way S3 does.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]fakeObject
	headers []http.Header // every PUT's headers
}

type fakeObject struct {
	body []byte
	meta string
}

func newFakeS3(t *testing.T) (*fakeS3, *httptest.Server) {
	t.Helper()
	f := &fakeS3{objects: map[string]fakeObject{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/")
	switch r.Method {
	case http.MethodPut:
		f.headers = append(f.headers, r.Header.Clone())
		body, _ := io.ReadAll(r.Body)
		if _, ok := f.objects[key]; ok && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code><Message>exists</Message></Error>`)
			return
		}
		f.objects[key] = fakeObject{body: body, meta: r.Header.Get("X-Amz-Meta-Sha256")}
		w.WriteHeader(http.StatusOK)
	case http.MethodHead, http.MethodGet:
		o, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
			}
			return
		}
		if o.meta != "" {
			w.Header().Set("X-Amz-Meta-Sha256", o.meta)
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(o.body)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newTestS3Store(srvURL string, cfg Config) *store {
	sdk := aws.Config{
		Region:      "eu-west-2",
		Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
	}
	cfg.Endpoint = srvURL
	return &store{b: newS3FromConfig(sdk, cfg), now: time.Now}
}

func TestS3PutCreateOnly(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		seed    *fakeObject
		body    string
		wantErr error
		want    string
	}{
		{name: "new object", body: "hello", want: "hello"},
		{name: "retry with the same bytes", seed: &fakeObject{body: []byte("hello"), meta: SHA256([]byte("hello"))},
			body: "hello", want: "hello"},
		{name: "same bytes without digest metadata", seed: &fakeObject{body: []byte("hello")},
			body: "hello", want: "hello"},
		{name: "different bytes", seed: &fakeObject{body: []byte("other"), meta: SHA256([]byte("other"))},
			body: "hello", wantErr: ErrConflict, want: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, srv := newFakeS3(t)
			if tt.seed != nil {
				f.objects["bucket/a/b.txt"] = *tt.seed
			}
			s := newTestS3Store(srv.URL, Config{Provider: ProviderS3, Bucket: "bucket"})
			err := s.Put(ctx, "a/b.txt", []byte(tt.body), "text/plain")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Put() error = %v, want %v", err, tt.wantErr)
			}
			if got := string(f.objects["bucket/a/b.txt"].body); got != tt.want {
				t.Errorf("stored = %q, want %q", got, tt.want)
			}
			if h := f.headers[0]; h.Get("If-None-Match") != "*" {
				t.Errorf("If-None-Match = %q, want *", h.Get("If-None-Match"))
			}
		})
	}
}

func TestS3PutHeaders(t *testing.T) {
	f, srv := newFakeS3(t)
	s := newTestS3Store(srv.URL, Config{Provider: ProviderS3, Bucket: "bucket", KMSKey: "arn:aws:kms:eu-west-2:1:key/k"})
	if err := s.Put(context.Background(), "k.gz", []byte("body"), "application/gzip"); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	h := f.headers[0]
	for name, want := range map[string]string{
		"Content-Type":                                "application/gzip",
		"X-Amz-Meta-Sha256":                           SHA256([]byte("body")),
		"X-Amz-Server-Side-Encryption":                "aws:kms",
		"X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id": "arn:aws:kms:eu-west-2:1:key/k",
		"X-Amz-Checksum-Sha256":                       checksumB64("body"),
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if h.Get("Content-Encoding") != "" && h.Get("Content-Encoding") != "aws-chunked" {
		t.Errorf("Content-Encoding = %q, want none (objects are stored as gzip, not transcoded)", h.Get("Content-Encoding"))
	}
}

// checksumB64 is S3's x-amz-checksum-sha256 rendering of body.
func checksumB64(body string) string {
	sum := sha256.Sum256([]byte(body))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func TestS3PutNoKMSByDefault(t *testing.T) {
	f, srv := newFakeS3(t)
	s := newTestS3Store(srv.URL, Config{Provider: ProviderS3, Bucket: "bucket"})
	if err := s.Put(context.Background(), "k", []byte("x"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if got := f.headers[0].Get("X-Amz-Server-Side-Encryption"); got != "" {
		t.Errorf("X-Amz-Server-Side-Encryption = %q, want the bucket default", got)
	}
}

func TestS3Get(t *testing.T) {
	f, srv := newFakeS3(t)
	f.objects["bucket/k"] = fakeObject{body: []byte("data")}
	s := newTestS3Store(srv.URL, Config{Provider: ProviderS3, Bucket: "bucket"})
	rc, err := s.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "data" {
		t.Errorf("Get() = %q", got)
	}
	if _, err := s.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
}

func TestS3Presign(t *testing.T) {
	_, srv := newFakeS3(t)
	s := newTestS3Store(srv.URL, Config{Provider: ProviderS3, Bucket: "bucket"})
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	raw, exp, err := s.Presign(context.Background(), "run/patch.mbox", 5*time.Minute, "patch.mbox")
	if err != nil {
		t.Fatalf("Presign() error = %v", err)
	}
	if !exp.Equal(now.Add(5 * time.Minute)) {
		t.Errorf("expires = %v", exp)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Path != "/bucket/run/patch.mbox" {
		t.Errorf("path = %q", u.Path)
	}
	if q.Get("X-Amz-Expires") != "300" || q.Get("X-Amz-Signature") == "" {
		t.Errorf("query = %v, want a 300s SigV4 signature", q)
	}
	if q.Get("response-content-disposition") != `attachment; filename="patch.mbox"` {
		t.Errorf("response-content-disposition = %q", q.Get("response-content-disposition"))
	}
	for _, ttl := range []time.Duration{0, MaxPresignTTL + time.Second} {
		if _, _, err := s.Presign(context.Background(), "k", ttl, ""); err == nil {
			t.Errorf("Presign(ttl=%s) error = nil, want out of range", ttl)
		}
	}
}
