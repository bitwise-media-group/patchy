// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestPutBlobVerifiesAndServes(t *testing.T) {
	s := newStore(t)
	body := []byte("workspace bundle bytes")
	digest := digestOf(body)

	created, err := s.PutBlob(digest, strings.NewReader(string(body)), 0)
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if !created {
		t.Error("PutBlob created = false, want true")
	}
	if size, ok := s.StatBlob(digest); !ok || size != int64(len(body)) {
		t.Errorf("StatBlob = (%d, %v), want (%d, true)", size, ok, len(body))
	}

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/artifacts/"+digest+".tar.gz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET blob = %d, want 200", rr.Code)
	}
	if got := rr.Body.String(); got != string(body) {
		t.Errorf("GET blob body = %q, want %q", got, body)
	}
}

func TestPutBlobMismatchIsRejected(t *testing.T) {
	s := newStore(t)
	digest := digestOf([]byte("expected"))
	if _, err := s.PutBlob(digest, strings.NewReader("something else"), 0); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("PutBlob(mismatch) = %v, want ErrDigestMismatch", err)
	}
	if _, ok := s.StatBlob(digest); ok {
		t.Error("StatBlob after mismatch = true, want false")
	}
	files, err := filepath.Glob(filepath.Join(s.dir, "*.tmp"))
	if err != nil || len(files) != 0 {
		t.Errorf("leftover temp files = %v (err %v), want none", files, err)
	}
}

func TestPutBlobIdempotent(t *testing.T) {
	s := newStore(t)
	body := []byte("same bytes")
	digest := digestOf(body)
	if _, err := s.PutBlob(digest, strings.NewReader(string(body)), 0); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	// The second put must not consume the reader at all.
	created, err := s.PutBlob(digest, failingReader{t}, 0)
	if err != nil {
		t.Fatalf("PutBlob(existing) = %v, want nil", err)
	}
	if created {
		t.Error("PutBlob(existing) created = true, want false")
	}
}

type failingReader struct{ t *testing.T }

func (f failingReader) Read([]byte) (int, error) {
	f.t.Error("existing blob's upload stream was read")
	return 0, errors.New("read of existing blob")
}

func TestPutBlobCap(t *testing.T) {
	s := newStore(t)
	body := []byte("0123456789")
	if _, err := s.PutBlob(digestOf(body), strings.NewReader(string(body)), 5); !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("PutBlob(over cap) = %v, want ErrBlobTooLarge", err)
	}
}

func TestBlobIndexRebuiltOnRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir, "http://arts.local")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	body := []byte("durable bundle")
	digest := digestOf(body)
	if _, err := s.PutBlob(digest, strings.NewReader(string(body)), 0); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}

	restarted, err := NewStore(dir, "http://arts.local")
	if err != nil {
		t.Fatalf("NewStore(restart): %v", err)
	}
	if size, ok := restarted.StatBlob(digest); !ok || size != int64(len(body)) {
		t.Errorf("StatBlob after restart = (%d, %v), want (%d, true)", size, ok, len(body))
	}
}

func TestSweepBlobsByLastAccess(t *testing.T) {
	s := newStore(t)
	stale := []byte("stale")
	fresh := []byte("fresh")
	for _, b := range [][]byte{stale, fresh} {
		if _, err := s.PutBlob(digestOf(b), strings.NewReader(string(b)), 0); err != nil {
			t.Fatalf("PutBlob: %v", err)
		}
	}
	// Age the stale blob both in memory and on disk.
	old := time.Now().Add(-8 * 24 * time.Hour)
	s.mu.Lock()
	e := s.blobs[digestOf(stale)]
	e.lastAccess = old
	s.mu.Unlock()
	if err := os.Chtimes(e.path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if removed := s.SweepBlobs(time.Now().Add(-7 * 24 * time.Hour)); removed != 1 {
		t.Fatalf("SweepBlobs removed = %d, want 1", removed)
	}
	if _, ok := s.StatBlob(digestOf(stale)); ok {
		t.Error("stale blob survived the sweep")
	}
	if _, ok := s.StatBlob(digestOf(fresh)); !ok {
		t.Error("fresh blob was swept")
	}
}

func TestInternalHandler(t *testing.T) {
	s := newStore(t)
	h := s.InternalHandler("sekrit", 1<<20)
	body := []byte("uploaded via internal endpoint")
	digest := digestOf(body)

	do := func(method, path, token string, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(payload))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	if rr := do("PUT", "/internal/blobs/"+digest, "", string(body)); rr.Code != http.StatusUnauthorized {
		t.Errorf("PUT without token = %d, want 401", rr.Code)
	}
	if rr := do("HEAD", "/internal/blobs/"+digest, "sekrit", ""); rr.Code != http.StatusNotFound {
		t.Errorf("HEAD absent blob = %d, want 404", rr.Code)
	}
	if rr := do("PUT", "/internal/blobs/"+digest, "sekrit", string(body)); rr.Code != http.StatusCreated {
		t.Errorf("PUT new blob = %d, want 201", rr.Code)
	}
	if rr := do("PUT", "/internal/blobs/"+digest, "sekrit", string(body)); rr.Code != http.StatusOK {
		t.Errorf("PUT existing blob = %d, want 200", rr.Code)
	}
	if rr := do("HEAD", "/internal/blobs/"+digest, "sekrit", ""); rr.Code != http.StatusNoContent {
		t.Errorf("HEAD cached blob = %d, want 204", rr.Code)
	}
	wrong := digestOf([]byte("other content"))
	if rr := do("PUT", "/internal/blobs/"+wrong, "sekrit", string(body)); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("PUT mismatched digest = %d, want 422", rr.Code)
	}
	if rr := do("PUT", "/internal/blobs/nothex", "sekrit", string(body)); rr.Code != http.StatusBadRequest {
		t.Errorf("PUT malformed digest = %d, want 400", rr.Code)
	}
}

func TestRepoTarballIDsStillServe(t *testing.T) {
	s := newStore(t)
	info, err := s.Put("default/repo", strings.NewReader("repo tarball"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	path := strings.TrimPrefix(info.URL, "http://artifacts.local")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
	if rr.Code != http.StatusOK {
		t.Errorf("GET repo tarball = %d, want 200", rr.Code)
	}
}
