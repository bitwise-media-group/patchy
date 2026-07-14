// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghpush

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
)

var testChangeset = &envelope.Changeset{
	BaseSHA:       "base000",
	CommitMessage: "fix(security): escape sink",
	Upserts: []envelope.FileChange{
		{Path: "app.js", Mode: "100644", ContentB64: base64.StdEncoding.EncodeToString([]byte("escaped();\n"))},
	},
	Deletes: []string{"legacy.js"},
}

// TestPushTranslatesChangeset drives Push against a fake API and asserts the
// changeset reaches the Git Data endpoints decoded and intact. Full HTTP
// behavior (ordering, force-update fallback) is covered in ghclient.
func TestPushTranslatesChangeset(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.StripPrefix("/api/v3", mux))
	t.Cleanup(srv.Close)

	var blobContent, refSHA string
	mux.HandleFunc("POST /repos/o/r/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer write-token" {
			t.Errorf("Authorization = %q, want the push token", got)
		}
		var body struct{ Content string }
		readJSON(t, r, &body)
		blobContent = body.Content
		writeJSON(w, `{"sha":"blob1"}`)
	})
	mux.HandleFunc("POST /repos/o/r/git/trees", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"sha":"tree1"}`)
	})
	mux.HandleFunc("POST /repos/o/r/git/commits", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Message string }
		readJSON(t, r, &body)
		if body.Message != "fix(security): escape sink" {
			t.Errorf("commit message = %q", body.Message)
		}
		writeJSON(w, `{"sha":"commit1"}`)
	})
	mux.HandleFunc("POST /repos/o/r/git/refs", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ref, SHA string }
		readJSON(t, r, &body)
		refSHA = body.SHA
		if body.Ref != "refs/heads/patchy/issue-7" {
			t.Errorf("ref = %q", body.Ref)
		}
		writeJSON(w, `{"ref":"refs/heads/patchy/issue-7","object":{"sha":"commit1"}}`)
	})

	p := New(srv.URL)
	repo := ghclient.Repo{Owner: "o", Name: "r"}
	if err := p.Push(context.Background(), repo, "write-token", "patchy/issue-7", testChangeset); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	// ghclient re-encodes the decoded content; the round-trip must be exact.
	want := base64.StdEncoding.EncodeToString([]byte("escaped();\n"))
	if blobContent != want {
		t.Errorf("blob content = %q, want %q", blobContent, want)
	}
	if refSHA != "commit1" {
		t.Errorf("ref sha = %q, want commit1", refSHA)
	}
}

func TestPushRejectsCorruptContent(t *testing.T) {
	p := New("http://127.0.0.1:0")
	cs := &envelope.Changeset{
		BaseSHA: "base000",
		Upserts: []envelope.FileChange{{Path: "a", Mode: "100644", ContentB64: "not base64!"}},
	}
	err := p.Push(context.Background(), ghclient.Repo{Owner: "o", Name: "r"}, "t", "b", cs)
	if err == nil || !strings.Contains(err.Error(), "decode a") {
		t.Errorf("Push() error = %v, want a decode failure naming the path", err)
	}
}

// readJSON parses a request body on a server goroutine, so failures are
// t.Errorf, never t.Fatalf.
func readJSON(t *testing.T, r *http.Request, v any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Errorf("decode request: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}
