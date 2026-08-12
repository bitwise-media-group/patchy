// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package artifact stores and serves the SHA-pinned repository tarballs that
// source-controller produces and agent jobs fetch. Paths embed a 128-bit
// random id, so a URL is unguessable by other workloads that can reach the
// serving port; the fetching init container verifies the sha256 digest
// end-to-end. The store is in-memory-indexed over plain files — artifacts
// are reproducible (a restart just re-fetches), so no durable index exists.
package artifact

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Info describes one stored artifact.
type Info struct {
	// URL the artifact is served at.
	URL string
	// Digest is the hex sha256 of the tarball.
	Digest string
	// Size in bytes.
	Size int64
}

type entry struct {
	id     string
	path   string
	digest string
	size   int64
}

// Store keeps artifacts on disk keyed by owner name (the Repository object's
// namespaced name) and serves them over HTTP.
type Store struct {
	dir     string
	baseURL string

	mu      sync.Mutex
	entries map[string]entry      // owner key → entry
	byID    map[string]entry      // random id → entry
	blobs   map[string]*blobEntry // sha256 digest → workspace bundle
}

// NewStore builds a Store rooted at dir, minting URLs under baseURL (the
// cluster-internal address agents reach, e.g.
// http://patchy-source-controller.patchy.svc:9790).
func NewStore(dir, baseURL string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("artifact dir: %w", err)
	}
	s := &Store{
		dir:     dir,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		entries: make(map[string]entry),
		byID:    make(map[string]entry),
		blobs:   make(map[string]*blobEntry),
	}
	// Repository tarballs are reproducible (a restart re-fetches), but
	// workspace blobs are not — only the client can rebuild one — so their
	// index is recovered from disk.
	if err := s.rescanBlobs(); err != nil {
		return nil, err
	}
	return s, nil
}

// Put stores the tarball read from r under the owner key, replacing any
// previous artifact for that key, and returns its serving info.
func (s *Store) Put(key string, r io.Reader) (*Info, error) {
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, fmt.Errorf("artifact id: %w", err)
	}
	id := hex.EncodeToString(idBytes[:])

	path := filepath.Join(s.dir, id+".tar.gz")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return nil, fmt.Errorf("artifact file: %w", err)
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("write artifact: %w", err)
	}

	e := entry{id: id, path: path, digest: hex.EncodeToString(h.Sum(nil)), size: size}
	s.mu.Lock()
	if old, ok := s.entries[key]; ok {
		delete(s.byID, old.id)
		_ = os.Remove(old.path)
	}
	s.entries[key] = e
	s.byID[id] = e
	s.mu.Unlock()

	return &Info{URL: s.baseURL + "/artifacts/" + id + ".tar.gz", Digest: e.digest, Size: size}, nil
}

// Get returns the stored artifact info for the owner key, if present.
func (s *Store) Get(key string) (*Info, bool) {
	s.mu.Lock()
	e, ok := s.entries[key]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	return &Info{URL: s.baseURL + "/artifacts/" + e.id + ".tar.gz", Digest: e.digest, Size: e.size}, true
}

// Delete removes the owner key's artifact, if present.
func (s *Store) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok {
		delete(s.entries, key)
		delete(s.byID, e.id)
		_ = os.Remove(e.path)
	}
}

// Handler serves GET /artifacts/<id>.tar.gz: 32-hex ids are repository
// tarballs, 64-hex ids are content-addressed workspace blobs.
func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /artifacts/{file}", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(r.PathValue("file"), ".tar.gz")
		if blobDigest(id) {
			s.serveBlob(w, r, id)
			return
		}
		s.mu.Lock()
		e, ok := s.byID[id]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		http.ServeFile(w, r, e.path)
	})
	return mux
}
