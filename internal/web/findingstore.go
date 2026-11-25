// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

// findingStore mirrors the informer cache's findings as pointers to the
// cached objects themselves, so a findings request reads them with no copy
// at all.
//
// A cache List, even with UnsafeDisableDeepCopy, still copies every
// Finding struct (744 bytes, through reflection) into the list it returns:
// about 75MB of garbage per request at 100k findings, 1.5GB at 2M. Reading
// the informer's indexer would avoid that, but the manager's cache is the
// multi-namespace kind, whose informers do not expose one. So an event
// handler on the Finding informer keeps this mirror instead, and a request
// copies only the pointers (8 bytes a finding).
//
// The objects are shared with the cache and every other reader: treat them
// as read-only. The informer never mutates a stored object; an update
// replaces the pointer, which the handler mirrors.
type findingStore struct {
	mu    sync.RWMutex
	items []*v1alpha1.Finding
	// at is each finding's position in items, by name, so a delete swaps
	// the last item into its place.
	at map[string]int
	// synced reports that the handler has delivered the informer's initial
	// list; until then the mirror is partial and requests list the cache.
	synced atomic.Pointer[func() bool]
}

func newFindingStore() *findingStore {
	return &findingStore{at: map[string]int{}}
}

func (st *findingStore) upsert(f *v1alpha1.Finding) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if i, ok := st.at[f.Name]; ok {
		st.items[i] = f
		return
	}
	st.at[f.Name] = len(st.items)
	st.items = append(st.items, f)
}

func (st *findingStore) remove(name string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	i, ok := st.at[name]
	if !ok {
		return
	}
	last := len(st.items) - 1
	if i != last {
		st.items[i] = st.items[last]
		st.at[st.items[i].Name] = i
	}
	st.items[last] = nil
	st.items = st.items[:last]
	delete(st.at, name)
}

// snapshot returns the mirrored findings, or false until the mirror has
// synced. The slice is the caller's; the objects are not.
func (st *findingStore) snapshot() ([]*v1alpha1.Finding, bool) {
	synced := st.synced.Load()
	if synced == nil || !(*synced)() {
		return nil, false
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]*v1alpha1.Finding, len(st.items))
	copy(out, st.items)
	return out, true
}

// handler mirrors one namespace's findings, then calls changed: after the
// mirror holds the change, so a refetch it prompts reads the new state.
// Resyncs (an update whose resourceVersion did not move) call nothing.
func (st *findingStore) handler(namespace string, changed func()) toolscache.ResourceEventHandlerFuncs {
	put := func(obj any) {
		if f, ok := obj.(*v1alpha1.Finding); ok && f.Namespace == namespace {
			st.upsert(f)
			changed()
		}
	}
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc: put,
		UpdateFunc: func(oldObj, obj any) {
			if resourceChanged(oldObj, obj) {
				put(obj)
			}
		},
		DeleteFunc: func(obj any) {
			if tomb, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				obj = tomb.Obj
			}
			if f, ok := obj.(*v1alpha1.Finding); ok && f.Namespace == namespace {
				st.remove(f.Name)
				changed()
			}
		},
	}
}

// start registers the mirror's handler on the Finding informer.
func (st *findingStore) start(ctx context.Context, c cache.Cache, namespace string, changed func()) error {
	informer, err := c.GetInformer(ctx, &v1alpha1.Finding{})
	if err != nil {
		return fmt.Errorf("mirror findings: %w", err)
	}
	reg, err := informer.AddEventHandler(st.handler(namespace, changed))
	if err != nil {
		return fmt.Errorf("mirror findings: %w", err)
	}
	synced := reg.HasSynced
	st.synced.Store(&synced)
	return nil
}

// listFindings returns every finding in the namespace, read-only: from the
// mirror once it has synced, otherwise (at startup, or with no watch
// running, as in tests) from a no-deep-copy cache List.
func (s *Server) listFindings(ctx context.Context) ([]*v1alpha1.Finding, error) {
	if items, ok := s.findings.snapshot(); ok {
		return items, nil
	}
	var list v1alpha1.FindingList
	if err := s.client.List(ctx, &list, client.InNamespace(s.namespace), client.UnsafeDisableDeepCopy); err != nil {
		return nil, fmt.Errorf("list findings: %w", err)
	}
	out := make([]*v1alpha1.Finding, len(list.Items))
	for i := range list.Items {
		out[i] = &list.Items[i]
	}
	return out, nil
}
