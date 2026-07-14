// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package fakegithub

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func decodeJSON(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

// BaseSHA is the fixed head of every branch the fake has not been pushed to;
// controllers resolving a clone base get this.
const BaseSHA = "00000000000000000000000000000000000000ba"

// gitData is the Git Data API surface: enough blob/tree/commit/ref state for
// the remediation-controller's API push (internal/ghpush) to run against the
// fake, and for tests to read back what was pushed.
type gitData struct {
	blobs   map[string][]byte            // blob sha -> raw content
	trees   map[string]map[string]string // tree sha -> path -> blob sha ("" = delete)
	commits map[string]commitRec
	refs    map[string]string // "heads/<branch>" -> commit sha
	next    int
}

type commitRec struct {
	Message string
	Tree    string
	Parents []string
}

func newGitData() gitData {
	return gitData{
		blobs:   map[string][]byte{},
		trees:   map[string]map[string]string{},
		commits: map[string]commitRec{},
		refs:    map[string]string{},
	}
}

func (g *gitData) sha(kind string) string {
	g.next++
	return fmt.Sprintf("%s%07d", strings.Repeat(kind[:1], 33), g.next)
}

// BranchHead returns the pushed head of "heads/<branch>", or BaseSHA if the
// branch was never pushed.
func (s *Server) BranchHead(branch string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sha, ok := s.git.refs["heads/"+branch]; ok {
		return sha
	}
	return BaseSHA
}

// BranchFiles returns the file contents committed to a pushed branch (deleted
// paths map to nil), plus the commit message; ok is false when the branch was
// never pushed.
func (s *Server) BranchFiles(branch string) (files map[string][]byte, message string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sha, ok := s.git.refs["heads/"+branch]
	if !ok {
		return nil, "", false
	}
	commit := s.git.commits[sha]
	files = map[string][]byte{}
	for path, blob := range s.git.trees[commit.Tree] {
		if blob == "" {
			files[path] = nil
			continue
		}
		files[path] = s.git.blobs[blob]
	}
	return files, commit.Message, true
}

func (s *Server) gitRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/ref/{ref...}", s.getRef)
	mux.HandleFunc("POST /repos/{owner}/{repo}/git/blobs", s.createBlob)
	mux.HandleFunc("POST /repos/{owner}/{repo}/git/trees", s.createTree)
	mux.HandleFunc("POST /repos/{owner}/{repo}/git/commits", s.createCommit)
	mux.HandleFunc("POST /repos/{owner}/{repo}/git/refs", s.createRef)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/git/refs/{ref...}", s.updateRef)
}

func (s *Server) getRef(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	s.mu.Lock()
	sha, ok := s.git.refs[ref]
	s.mu.Unlock()
	if !ok {
		if !strings.HasPrefix(ref, "heads/") {
			http.NotFound(w, r)
			return
		}
		sha = BaseSHA // every un-pushed branch sits at the fixed base
	}
	writeJSON(w, map[string]any{
		"ref":    "refs/" + ref,
		"object": map[string]any{"type": "commit", "sha": sha},
	})
}

func (s *Server) createBlob(w http.ResponseWriter, r *http.Request) {
	var body struct{ Content, Encoding string }
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw := []byte(body.Content)
	if body.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		raw = decoded
	}
	s.mu.Lock()
	sha := s.git.sha("b")
	s.git.blobs[sha] = raw
	s.mu.Unlock()
	writeJSON(w, map[string]any{"sha": sha})
}

func (s *Server) createTree(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseTree string `json:"base_tree"`
		Tree     []struct {
			Path string  `json:"path"`
			Mode string  `json:"mode"`
			SHA  *string `json:"sha"`
		} `json:"tree"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	sha := s.git.sha("t")
	entries := map[string]string{}
	for _, e := range body.Tree {
		if e.SHA == nil {
			entries[e.Path] = "" // "sha": null — a deletion
			continue
		}
		entries[e.Path] = *e.SHA
	}
	s.git.trees[sha] = entries
	s.mu.Unlock()
	writeJSON(w, map[string]any{"sha": sha})
}

func (s *Server) createCommit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	sha := s.git.sha("c")
	s.git.commits[sha] = commitRec{Message: body.Message, Tree: body.Tree, Parents: body.Parents}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"sha": sha})
}

func (s *Server) createRef(w http.ResponseWriter, r *http.Request) {
	var body struct{ Ref, SHA string }
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ref := strings.TrimPrefix(body.Ref, "refs/")
	s.mu.Lock()
	_, exists := s.git.refs[ref]
	if !exists {
		s.git.refs[ref] = body.SHA
	}
	s.mu.Unlock()
	if exists {
		http.Error(w, `{"message":"Reference already exists"}`, http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]any{"ref": body.Ref, "object": map[string]any{"type": "commit", "sha": body.SHA}})
}

func (s *Server) updateRef(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	var body struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	_, exists := s.git.refs[ref]
	if exists {
		s.git.refs[ref] = body.SHA
	}
	s.mu.Unlock()
	if !exists {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{"ref": "refs/" + ref, "object": map[string]any{"type": "commit", "sha": body.SHA}})
}
