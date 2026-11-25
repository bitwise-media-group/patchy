// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
)

func storeFinding(ns, name, rv string) *v1alpha1.Finding {
	return &v1alpha1.Finding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, ResourceVersion: rv}}
}

func storeNames(t *testing.T, st *findingStore) []string {
	t.Helper()
	items, ok := st.snapshot()
	if !ok {
		t.Fatal("snapshot not synced")
	}
	out := make([]string, 0, len(items))
	for _, f := range items {
		out = append(out, f.Name)
	}
	slices.Sort(out)
	return out
}

// The handler mirrors one namespace's adds, updates and deletes (tombstones
// included), signalling after each real change and not on resyncs.
func TestFindingStoreMirrors(t *testing.T) {
	st := newFindingStore()
	if _, ok := st.snapshot(); ok {
		t.Fatal("snapshot before start must report unsynced")
	}
	synced := func() bool { return true }
	st.synced.Store(&synced)

	changes := 0
	h := st.handler("patchy", func() { changes++ })
	a1, b1, c1 := storeFinding("patchy", "a", "1"), storeFinding("patchy", "b", "1"), storeFinding("patchy", "c", "1")
	h.OnAdd(a1, true)
	h.OnAdd(b1, true)
	h.OnAdd(c1, true)
	h.OnAdd(storeFinding("elsewhere", "x", "1"), true)
	if got := storeNames(t, st); !slices.Equal(got, []string{"a", "b", "c"}) || changes != 3 {
		t.Fatalf("after adds = %v (%d changes), want [a b c] (3)", got, changes)
	}

	h.OnUpdate(b1, storeFinding("patchy", "b", "1")) // resync: same resourceVersion
	if changes != 3 {
		t.Errorf("a resync signalled a change")
	}
	b2 := storeFinding("patchy", "b", "2")
	h.OnUpdate(b1, b2)
	items, _ := st.snapshot()
	if i := slices.IndexFunc(items, func(f *v1alpha1.Finding) bool { return f.Name == "b" }); i < 0 || items[i] != b2 {
		t.Errorf("update did not replace the pointer")
	}

	// Deleting from the front swaps the last item in; the index follows it.
	h.OnDelete(a1)
	h.OnDelete(toolscache.DeletedFinalStateUnknown{Key: "patchy/c", Obj: c1})
	h.OnDelete(storeFinding("patchy", "never-added", "1"))
	if got := storeNames(t, st); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("after deletes = %v, want [b]", got)
	}
	h.OnAdd(storeFinding("patchy", "d", "1"), false)
	h.OnDelete(b2)
	if got := storeNames(t, st); !slices.Equal(got, []string{"d"}) {
		t.Fatalf("after re-add and delete = %v, want [d]", got)
	}
	if len(st.at) != 1 || st.at["d"] != 0 {
		t.Errorf("index = %v, want map[d:0]", st.at)
	}
}

// A snapshot is the caller's to keep: later changes do not show through.
func TestFindingStoreSnapshotIsStable(t *testing.T) {
	st := newFindingStore()
	st.fill([]v1alpha1.Finding{*storeFinding("patchy", "a", "1"), *storeFinding("patchy", "b", "1")})
	before, _ := st.snapshot()
	st.remove("a")
	if len(before) != 2 || before[0] == nil || before[1] == nil {
		t.Errorf("snapshot changed under its holder: %v", before)
	}
}

// Before the mirror syncs, and with it synced, the queries answer the same.
func TestFindingSourcesAgree(t *testing.T) {
	listed, mirrored := listedFindingServer(3000), cachedFindingServer(3000)
	for _, raw := range []string{"", "limit=200", "severity=high&phase=Queued", "q=repo-1", "repoPrefix=repo-2"} {
		a, b := mustPage(t, listed, raw), mustPage(t, mirrored, raw)
		if a.Matched == 0 {
			t.Errorf("%q matches nothing; the comparison proves little", raw)
		}
		if !slices.Equal(names(a), names(b)) || a.Matched != b.Matched || a.NextCursor != b.NextCursor {
			t.Errorf("%q: listed and mirrored pages differ (matched %d vs %d)", raw, a.Matched, b.Matched)
		}
	}
}

func mustPage(t *testing.T, s *Server, raw string) *FindingsPage {
	t.Helper()
	p, err := s.queryFindings(t.Context(), mustQuery(t, raw), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
