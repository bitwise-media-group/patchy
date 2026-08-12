// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package artifact

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Blob errors the upload API maps onto HTTP statuses.
var (
	// ErrDigestMismatch: the uploaded bytes do not hash to the digest they
	// were uploaded under.
	ErrDigestMismatch = errors.New("artifact: digest mismatch")
	// ErrBlobTooLarge: the upload exceeded the configured cap.
	ErrBlobTooLarge = errors.New("artifact: blob too large")
)

// blobEntry is one content-addressed workspace bundle. Unlike repository
// tarballs, blobs are NOT reproducible server-side (only the client can
// rebuild one), so the index is rebuilt from disk on restart and entries
// expire by last access rather than by owner.
type blobEntry struct {
	path       string
	size       int64
	lastAccess time.Time
}

// blobDigest reports whether id is a well-formed blob digest (64 hex chars —
// repository tarball ids are 32, so the length discriminates the two file
// families sharing the directory).
func blobDigest(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// rescanBlobs rebuilds the blob index from the files on disk, using each
// file's mtime as its last access. Called under no lock (construction only).
func (s *Store) rescanBlobs() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("artifact: rescan blobs: %w", err)
	}
	for _, de := range entries {
		digest, found := strings.CutSuffix(de.Name(), ".tar.gz")
		if !found || !blobDigest(digest) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		s.blobs[digest] = &blobEntry{
			path:       filepath.Join(s.dir, de.Name()),
			size:       info.Size(),
			lastAccess: info.ModTime(),
		}
	}
	return nil
}

// PutBlob stores the tarball read from r under its sha256 digest,
// idempotently: a digest already present is not re-read. The stream is
// hashed while writing and discarded on mismatch (ErrDigestMismatch) or when
// it exceeds maxBytes (ErrBlobTooLarge; 0 means unbounded). Returns whether
// the blob was newly created.
func (s *Store) PutBlob(digest string, r io.Reader, maxBytes int64) (bool, error) {
	if !blobDigest(digest) {
		return false, fmt.Errorf("artifact: malformed blob digest %q", digest)
	}
	if _, ok := s.StatBlob(digest); ok {
		return false, nil
	}

	f, err := os.CreateTemp(s.dir, "blob-*.tmp")
	if err != nil {
		return false, fmt.Errorf("artifact: blob temp file: %w", err)
	}
	tmp := f.Name()
	discard := func() { _ = os.Remove(tmp) }

	limited := r
	if maxBytes > 0 {
		limited = io.LimitReader(r, maxBytes+1)
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), limited)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		discard()
		return false, fmt.Errorf("artifact: write blob: %w", err)
	}
	if maxBytes > 0 && size > maxBytes {
		discard()
		return false, ErrBlobTooLarge
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != digest {
		discard()
		return false, fmt.Errorf("%w: got %s", ErrDigestMismatch, got)
	}

	path := filepath.Join(s.dir, digest+".tar.gz")
	if err := os.Rename(tmp, path); err != nil {
		discard()
		return false, fmt.Errorf("artifact: place blob: %w", err)
	}
	s.mu.Lock()
	s.blobs[digest] = &blobEntry{path: path, size: size, lastAccess: time.Now()}
	s.mu.Unlock()
	return true, nil
}

// StatBlob reports whether the digest is cached and its size. A hit counts
// as an access: anything that checks a blob (submission validation, the
// dedupe HEAD) is keeping it alive.
func (s *Store) StatBlob(digest string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.blobs[digest]
	if !ok {
		return 0, false
	}
	s.touchLocked(e)
	return e.size, true
}

// touchLocked refreshes the entry's last access, persisting it as the file
// mtime (best-effort) so the retention clock survives restarts.
func (s *Store) touchLocked(e *blobEntry) {
	now := time.Now()
	e.lastAccess = now
	_ = os.Chtimes(e.path, now, now)
}

// SweepBlobs removes every blob last accessed before the cutoff and returns
// how many were removed. A swept-but-needed blob resurfaces as a
// WorkspaceLost unit failure, prompting the client to re-upload.
func (s *Store) SweepBlobs(before time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for digest, e := range s.blobs {
		if e.lastAccess.Before(before) {
			delete(s.blobs, digest)
			_ = os.Remove(e.path)
			removed++
		}
	}
	return removed
}

// serveBlob answers GET /artifacts/<digest>.tar.gz for a 64-hex id from the
// blob index; the digest is the fetch capability.
func (s *Store) serveBlob(w http.ResponseWriter, r *http.Request, digest string) {
	s.mu.Lock()
	e, ok := s.blobs[digest]
	if ok {
		s.touchLocked(e)
	}
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(w, r, e.path)
}

// InternalHandler serves the NetworkPolicy-gated upload endpoint
// evaluation-controller streams workspace bundles to:
//
//	HEAD /internal/blobs/{digest} → 204 cached / 404
//	PUT  /internal/blobs/{digest} → 201 created / 200 already cached /
//	                                422 digest mismatch / 413 too large
//
// A non-empty token requires Authorization: Bearer <token> (constant-time
// compared) as defense-in-depth atop the NetworkPolicy; maxBytes caps
// uploads (0 = unbounded).
func (s *Store) InternalHandler(token string, maxBytes int64) http.Handler {
	authed := func(r *http.Request) bool {
		if token == "" {
			return true
		}
		got, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		return found && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
	}
	mux := http.NewServeMux()
	mux.HandleFunc("HEAD /internal/blobs/{digest}", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if _, ok := s.StatBlob(r.PathValue("digest")); !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /internal/blobs/{digest}", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		digest := r.PathValue("digest")
		if !blobDigest(digest) {
			http.Error(w, "malformed digest", http.StatusBadRequest)
			return
		}
		created, err := s.PutBlob(digest, r.Body, maxBytes)
		switch {
		case errors.Is(err, ErrDigestMismatch):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		case errors.Is(err, ErrBlobTooLarge):
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		case err != nil:
			http.Error(w, "store blob", http.StatusInternalServerError)
		case created:
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	return mux
}
