// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"encoding/base64"
	"net/http"
	"reflect"
	"testing"
)

func TestHeadSHA(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("GET /repos/o/r/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":"abc123"}}`)
	})

	got, err := c.HeadSHA(context.Background(), testRepo, "main")
	if err != nil {
		t.Fatalf("HeadSHA() error = %v", err)
	}
	if got != "abc123" {
		t.Errorf("HeadSHA() = %q, want abc123", got)
	}
}

// pushRequest is a scripted PushBranch fixture: binary-ish content, an
// executable, and a deletion, on top of base000.
var pushRequest = BranchPush{
	Branch:  "patchy/issue-9",
	BaseSHA: "base000",
	Message: "fix(security): escape sink",
	Files: []CommitFile{
		{Path: "app/handler.go", Mode: "100644", Content: []byte{0x00, 0xff, 0x0a}},
		{Path: "tools/run.sh", Mode: "100755", Content: []byte("#!/bin/sh\n")},
	},
	Deletes: []string{"app/legacy.go"},
}

// pushFake wires the four Git Data endpoints, recording call order and
// payloads for assertions.
func pushFake(t *testing.T, mux *http.ServeMux) (calls *[]string, trees *[]map[string]any, refs *[]map[string]any) {
	t.Helper()
	var order []string
	var treeBodies, refBodies []map[string]any
	blobCount := 0

	mux.HandleFunc("POST /repos/o/r/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "blob")
		body := decodeBody[map[string]any](t, r)
		if body["encoding"] != "base64" {
			t.Errorf("blob encoding = %v, want base64", body["encoding"])
		}
		blobCount++
		if blobCount == 1 {
			want := base64.StdEncoding.EncodeToString([]byte{0x00, 0xff, 0x0a})
			if body["content"] != want {
				t.Errorf("blob content = %v, want %v", body["content"], want)
			}
		}
		writeJSON(t, w, `{"sha":"blob`+string(rune('0'+blobCount))+`"}`)
	})
	mux.HandleFunc("POST /repos/o/r/git/trees", func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "tree")
		treeBodies = append(treeBodies, decodeBody[map[string]any](t, r))
		writeJSON(t, w, `{"sha":"tree1"}`)
	})
	mux.HandleFunc("POST /repos/o/r/git/commits", func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "commit")
		body := decodeBody[map[string]any](t, r)
		if body["message"] != "fix(security): escape sink" {
			t.Errorf("commit message = %v", body["message"])
		}
		// go-github flattens the create-commit request: tree and parents are
		// plain SHA strings.
		if body["tree"] != "tree1" {
			t.Errorf("commit tree = %v, want tree1", body["tree"])
		}
		parents, _ := body["parents"].([]any)
		if len(parents) != 1 || parents[0] != "base000" {
			t.Errorf("commit parents = %v, want [base000]", body["parents"])
		}
		writeJSON(t, w, `{"sha":"newcommit"}`)
	})
	mux.HandleFunc("POST /repos/o/r/git/refs", func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "create-ref")
		refBodies = append(refBodies, decodeBody[map[string]any](t, r))
		writeJSON(t, w, `{"ref":"refs/heads/patchy/issue-9","object":{"sha":"newcommit"}}`)
	})
	return &order, &treeBodies, &refBodies
}

func TestPushBranch(t *testing.T) {
	mux, c := newFakeClient(t)
	order, trees, refs := pushFake(t, mux)

	if err := c.PushBranch(context.Background(), testRepo, pushRequest); err != nil {
		t.Fatalf("PushBranch() error = %v", err)
	}

	wantOrder := []string{"blob", "blob", "tree", "commit", "create-ref"}
	if !reflect.DeepEqual(*order, wantOrder) {
		t.Errorf("call order = %v, want %v", *order, wantOrder)
	}

	tree := (*trees)[0]
	if tree["base_tree"] != "base000" {
		t.Errorf("base_tree = %v, want base000", tree["base_tree"])
	}
	entries, _ := tree["tree"].([]any)
	if len(entries) != 3 {
		t.Fatalf("tree entries = %d, want 3", len(entries))
	}
	first := entries[0].(map[string]any)
	if first["path"] != "app/handler.go" || first["mode"] != "100644" || first["sha"] != "blob1" {
		t.Errorf("entry 0 = %v", first)
	}
	second := entries[1].(map[string]any)
	if second["mode"] != "100755" || second["sha"] != "blob2" {
		t.Errorf("entry 1 = %v, want the executable at blob2", second)
	}
	// The deletion must serialize an explicit "sha": null.
	del := entries[2].(map[string]any)
	if del["path"] != "app/legacy.go" {
		t.Errorf("entry 2 path = %v, want app/legacy.go", del["path"])
	}
	if sha, present := del["sha"]; !present || sha != nil {
		t.Errorf(`entry 2 sha = %v (present=%v), want explicit null`, sha, present)
	}

	ref := (*refs)[0]
	if ref["ref"] != "refs/heads/patchy/issue-9" || ref["sha"] != "newcommit" {
		t.Errorf("create-ref body = %v", ref)
	}
}

func TestPushBranchForceUpdatesExistingRef(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("POST /repos/o/r/git/blobs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"sha":"blob1"}`)
	})
	mux.HandleFunc("POST /repos/o/r/git/trees", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"sha":"tree1"}`)
	})
	mux.HandleFunc("POST /repos/o/r/git/commits", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"sha":"newcommit"}`)
	})
	mux.HandleFunc("POST /repos/o/r/git/refs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		writeJSON(t, w, `{"message":"Reference already exists"}`)
	})
	var patched map[string]any
	mux.HandleFunc("PATCH /repos/o/r/git/refs/heads/patchy/issue-9", func(w http.ResponseWriter, r *http.Request) {
		patched = decodeBody[map[string]any](t, r)
		writeJSON(t, w, `{"ref":"refs/heads/patchy/issue-9","object":{"sha":"newcommit"}}`)
	})

	if err := c.PushBranch(context.Background(), testRepo, pushRequest); err != nil {
		t.Fatalf("PushBranch() error = %v", err)
	}
	if patched == nil {
		t.Fatal("existing ref was never force-updated")
	}
	if patched["sha"] != "newcommit" || patched["force"] != true {
		t.Errorf("update-ref body = %v, want sha=newcommit force=true", patched)
	}
}

func TestPushBranchBlobErrorSurfaces(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("POST /repos/o/r/git/blobs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(t, w, `{"message":"nope"}`)
	})

	if err := c.PushBranch(context.Background(), testRepo, pushRequest); err == nil {
		t.Fatal("PushBranch() error = nil, want the blob failure")
	}
}
