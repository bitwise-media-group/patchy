// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package archivetest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/bitwise-media-group/patchy/internal/archive"
)

// Object is one stored object.
type Object struct {
	Body        []byte
	ContentType string
}

// Store is an in-memory archive.Store with the real create-only semantics.
// The zero value is not usable; call New.
type Store struct {
	mu      sync.Mutex
	objects map[string]Object
	puts    []string

	// PutErr, when set, is returned by every Put before anything is stored,
	// simulating an unreachable bucket.
	PutErr error
	// Now tells time for presign expiry; nil means time.Now.
	Now func() time.Time
}

// New returns an empty store.
func New() *Store { return &Store{objects: map[string]Object{}} }

// Put stores body at key once; the same bytes again succeed and different
// bytes fail with archive.ErrConflict.
func (s *Store) Put(_ context.Context, key string, body []byte, contentType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.PutErr != nil {
		return s.PutErr
	}
	s.puts = append(s.puts, key)
	if prev, ok := s.objects[key]; ok {
		if !bytes.Equal(prev.Body, body) {
			return fmt.Errorf("%w: %s", archive.ErrConflict, key)
		}
		return nil
	}
	s.objects[key] = Object{Body: bytes.Clone(body), ContentType: contentType}
	return nil
}

// Get opens a stored object.
func (s *Store) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return nil, archive.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(o.Body)), nil
}

// Presign returns a fake https URL naming the key, the expiry and the
// attachment name, so tests can assert on all three.
func (s *Store) Presign(
	_ context.Context, key string, ttl time.Duration, attachment string,
) (string, time.Time, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	exp := now().Add(ttl)
	q := url.Values{"expires": {exp.UTC().Format(time.RFC3339)}}
	if attachment != "" {
		q.Set("attachment", attachment)
	}
	return "https://archive.test/" + key + "?" + q.Encode(), exp, nil
}

// Close does nothing.
func (s *Store) Close() error { return nil }

// Object returns a stored object and whether it exists.
func (s *Store) Object(key string) (Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	return o, ok
}

// Keys lists stored keys in first-write order.
func (s *Store) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, k := range s.puts {
		if _, ok := s.objects[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// Puts lists every Put attempt's key in order, retries included.
func (s *Store) Puts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.puts...)
}
