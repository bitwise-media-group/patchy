// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package webhook

import (
	"sync"
	"time"
)

// dedup remembers delivery IDs for a TTL, holding at most cap entries
// (evicting oldest-first). The window absorbs GitHub's duplicate submissions
// of a single delivery burst, but must stay short: a redelivery — manual or
// requested by the Integration's sweep/replay — reuses the original delivery
// GUID, and an ID remembered past the TTL would silently swallow it.
// Handlers are idempotent, so an expired duplicate slipping through is safe.
type dedup struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	seen  map[string]time.Time
	order []string
	next  int
}

func newDedup(cap int, ttl time.Duration) *dedup {
	return &dedup{
		ttl:   ttl,
		now:   time.Now,
		seen:  make(map[string]time.Time, cap),
		order: make([]string, cap),
	}
}

// add records the ID, reporting false when it was already present and
// younger than the TTL. An expired entry is refreshed in place.
func (d *dedup) add(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if at, ok := d.seen[id]; ok {
		if now.Sub(at) < d.ttl {
			return false
		}
		// Refresh the expired entry; it keeps its ring slot, so it may be
		// evicted before its new TTL elapses — the bound wins over the
		// window, and idempotent handlers make that harmless.
		d.seen[id] = now
		return true
	}
	if evict := d.order[d.next]; evict != "" {
		delete(d.seen, evict)
	}
	d.order[d.next] = id
	d.next = (d.next + 1) % len(d.order)
	d.seen[id] = now
	return true
}

// remove forgets the ID (used when an accepted delivery could not be queued,
// so its redelivery must not look like a duplicate).
func (d *dedup) remove(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, id)
}

// reset forgets every ID, so any redelivery is handled as new.
func (d *dedup) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	clear(d.seen)
	clear(d.order)
	d.next = 0
}
