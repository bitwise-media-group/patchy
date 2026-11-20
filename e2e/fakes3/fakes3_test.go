// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package fakes3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/archive"
)

// useCredentials points the AWS SDK default chain at the fake's key, and
// away from any real profile on the machine running the tests.
func useCredentials(t *testing.T, secret string) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", AccessKeyID)
	t.Setenv("AWS_SECRET_ACCESS_KEY", secret)
	t.Setenv("AWS_REGION", Region)
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/none")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/none")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

func open(t *testing.T, s *Server) archive.Store {
	t.Helper()
	store, err := archive.Open(context.Background(), archive.Config{
		Provider: archive.ProviderS3, Bucket: "audit", Region: Region, Endpoint: s.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestFakeAgainstArchiveClient proves the fake agrees with patchy's real S3
// client: signatures verify, create-only writes behave, presigned URLs work
// and tampering with one does not.
func TestFakeAgainstArchiveClient(t *testing.T) {
	s := Start()
	defer s.Close()
	useCredentials(t, SecretAccessKey)
	store := open(t, s)
	ctx := context.Background()

	if err := store.Put(ctx, "run/report.md", []byte("# report"), "text/markdown"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Put(ctx, "run/report.md", []byte("# report"), "text/markdown"); err != nil {
		t.Fatalf("idempotent re-Put: %v", err)
	}
	if err := store.Put(ctx, "run/report.md", []byte("# other"), "text/markdown"); !errors.Is(err, archive.ErrConflict) {
		t.Fatalf("conflicting Put error = %v, want ErrConflict", err)
	}
	if o, _ := s.Object("audit", "run/report.md"); string(o.Body) != "# report" || o.Metadata["sha256"] == "" {
		t.Errorf("stored = %+v", o)
	}

	rc, err := store.Get(ctx, "run/report.md")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(body) != "# report" {
		t.Errorf("Get = %q", body)
	}

	u, _, err := store.Presign(ctx, "run/report.md", time.Minute, "report.md")
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "# report" ||
		!strings.Contains(res.Header.Get("Content-Disposition"), "report.md") {
		t.Errorf("presigned GET = %d %q (%s)", res.StatusCode, body, res.Header.Get("Content-Disposition"))
	}

	// The signature covers the key: pointing the URL elsewhere fails.
	res, err = http.Get(strings.Replace(u, "run/report.md", "run/patch.mbox", 1))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("tampered presigned GET = %d, want 403", res.StatusCode)
	}
}

func TestFakeRejectsWrongSecret(t *testing.T) {
	s := Start()
	defer s.Close()
	useCredentials(t, "not-the-secret")
	err := open(t, s).Put(context.Background(), "k", []byte("x"), "text/plain")
	if err == nil {
		t.Fatal("Put with the wrong secret succeeded")
	}
	if got := s.Requests(); len(got) == 0 || got[0].Status != http.StatusForbidden {
		t.Errorf("requests = %+v, want a 403", got)
	}
}
