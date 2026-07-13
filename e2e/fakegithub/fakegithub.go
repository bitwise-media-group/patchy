// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package fakegithub is an in-memory GitHub REST API good enough to run the
// patchy controllers against: code-scanning alerts, issues, labels,
// comments, and search. It exists so the e2e suite can drive the real
// binaries end to end with no network and no credentials.
package fakegithub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Issue is the fake's issue record.
type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	Labels    []label   `json:"labels"`
	Assignees []user    `json:"assignees"`
	CreatedAt time.Time `json:"created_at"`
	// RepositoryURL lets the client recover owner/name from search results.
	RepositoryURL string `json:"repository_url"`
}

type label struct {
	Name string `json:"name"`
}

type user struct {
	Login string `json:"login"`
}

type comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User user   `json:"user"`
}

// Server is the fake API.
type Server struct {
	*httptest.Server

	mu        sync.Mutex
	issues    map[int]*Issue
	comments  map[int][]comment
	dismissed map[int]string
	next      int
	// Now stamps created_at; tests override it to age issues instantly.
	Now func() time.Time
}

// New starts the fake. The returned URL is what a controller's
// --github-base-url should point at.
func New() *Server {
	s := &Server{
		issues:    make(map[int]*Issue),
		comments:  make(map[int][]comment),
		dismissed: make(map[int]string),
		next:      100,
		Now:       time.Now,
	}
	mux := http.NewServeMux()
	s.routes(mux)
	// go-github appends /api/v3 for a non-api.github.com base URL.
	s.Server = httptest.NewServer(http.StripPrefix("/api/v3", mux))
	return s
}

// Issues returns a snapshot of every issue, ordered by number.
func (s *Server) Issues() []Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Issue, 0, len(s.issues))
	for _, is := range s.issues {
		out = append(out, *is)
	}
	slices.SortFunc(out, func(a, b Issue) int { return a.Number - b.Number })
	return out
}

// Comments returns an issue's comment bodies.
func (s *Server) Comments(number int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.comments[number]))
	for _, c := range s.comments[number] {
		out = append(out, c.Body)
	}
	return out
}

// Dismissed returns the alert numbers dismissed so far.
func (s *Server) Dismissed() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, 0, len(s.dismissed))
	for n := range s.dismissed {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// LabelsOf returns one issue's label names.
func (s *Server) LabelsOf(number int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.issues[number]
	if !ok {
		return nil
	}
	out := make([]string, len(is.Labels))
	for i, l := range is.Labels {
		out[i] = l.Name
	}
	return out
}

// Age rewinds every issue's created_at by d, so time-gated transitions (the
// accumulation window, the remediation minimum age) fire immediately.
func (s *Server) Age(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.issues {
		is.CreatedAt = is.CreatedAt.Add(-d)
	}
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/code-scanning/alerts/{number}", s.getAlert)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/code-scanning/alerts/{number}", s.updateAlert)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues", s.listIssues)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues", s.createIssue)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/issues/{number}", s.editIssue)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}/comments", s.listComments)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/comments", s.createComment)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/labels", s.addLabels)
	mux.HandleFunc("DELETE /repos/{owner}/{repo}/issues/{number}/labels/{name}", s.removeLabel)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/assignees", s.addAssignees)
	mux.HandleFunc("GET /repos/{owner}/{repo}", s.getRepo)
	mux.HandleFunc("GET /search/issues", s.searchIssues)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, fmt.Sprintf("fakegithub: unhandled %s %s", r.Method, r.URL.Path), http.StatusNotFound)
	})
}

func (s *Server) getAlert(w http.ResponseWriter, r *http.Request) {
	number, _ := strconv.Atoi(r.PathValue("number"))
	writeJSON(w, map[string]any{
		"number":   number,
		"state":    "open",
		"html_url": fmt.Sprintf("https://github.com/acme/shop/security/code-scanning/%d", number),
		"rule": map[string]any{
			"id":                      "js/reflected-xss",
			"name":                    "js/reflected-xss",
			"description":             "Reflected cross-site scripting",
			"help":                    "Escape user input before writing it to the page.",
			"severity":                "error",
			"security_severity_level": "high",
			"tags":                    []string{"security", "external/cwe/cwe-079"},
		},
		"most_recent_instance": map[string]any{
			"commit_sha": "abc123",
			"message":    map[string]any{"text": "user input flows to a sink"},
			"location": map[string]any{
				"path": "src/render.js", "start_line": 42, "end_line": 44,
			},
		},
	})
}

func (s *Server) updateAlert(w http.ResponseWriter, r *http.Request) {
	number, _ := strconv.Atoi(r.PathValue("number"))
	var body struct {
		State           string `json:"state"`
		DismissedReason string `json:"dismissed_reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	s.mu.Lock()
	if body.State == "dismissed" {
		s.dismissed[number] = body.DismissedReason
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"number": number, "state": body.State})
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	want := splitLabels(r.URL.Query().Get("labels"))
	s.mu.Lock()
	defer s.mu.Unlock()

	out := []*Issue{}
	for _, is := range s.issues {
		if is.State == "open" && hasLabels(is, want) {
			out = append(out, is)
		}
	}
	slices.SortFunc(out, func(a, b *Issue) int { return a.Number - b.Number })
	writeJSON(w, out)
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) {
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	var body struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.next++
	is := &Issue{
		Number: s.next, Title: body.Title, Body: body.Body, State: "open",
		CreatedAt:     s.Now(),
		RepositoryURL: fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo),
	}
	for _, l := range body.Labels {
		is.Labels = append(is.Labels, label{Name: l})
	}
	s.issues[is.Number] = is
	s.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, is)
}

func (s *Server) editIssue(w http.ResponseWriter, r *http.Request) {
	number, _ := strconv.Atoi(r.PathValue("number"))
	var body struct {
		Body  *string `json:"body"`
		State *string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.issues[number]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if body.Body != nil {
		is.Body = *body.Body
	}
	if body.State != nil {
		is.State = *body.State
	}
	writeJSON(w, is)
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	number, _ := strconv.Atoi(r.PathValue("number"))
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.comments[number]
	if out == nil {
		out = []comment{}
	}
	writeJSON(w, out)
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	number, _ := strconv.Atoi(r.PathValue("number"))
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	c := comment{ID: int64(len(s.comments[number]) + 1), Body: body.Body, User: user{Login: "patchy[bot]"}}
	s.comments[number] = append(s.comments[number], c)
	s.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, c)
}

func (s *Server) addLabels(w http.ResponseWriter, r *http.Request) {
	number, _ := strconv.Atoi(r.PathValue("number"))
	names, err := decodeLabels(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.issues[number]
	if !ok {
		http.NotFound(w, r)
		return
	}
	for _, name := range names {
		if !slices.ContainsFunc(is.Labels, func(l label) bool { return l.Name == name }) {
			is.Labels = append(is.Labels, label{Name: name})
		}
	}
	writeJSON(w, is.Labels)
}

func (s *Server) removeLabel(w http.ResponseWriter, r *http.Request) {
	number, _ := strconv.Atoi(r.PathValue("number"))
	name := r.PathValue("name")

	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.issues[number]
	if !ok {
		http.NotFound(w, r)
		return
	}
	before := len(is.Labels)
	is.Labels = slices.DeleteFunc(is.Labels, func(l label) bool { return l.Name == name })
	if len(is.Labels) == before {
		// GitHub 404s an absent label; the client treats that as success.
		http.NotFound(w, r)
		return
	}
	writeJSON(w, is.Labels)
}

func (s *Server) addAssignees(w http.ResponseWriter, r *http.Request) {
	number, _ := strconv.Atoi(r.PathValue("number"))
	var body struct {
		Assignees []string `json:"assignees"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.issues[number]
	if !ok {
		http.NotFound(w, r)
		return
	}
	for _, login := range body.Assignees {
		is.Assignees = append(is.Assignees, user{Login: login})
	}
	writeJSON(w, is)
}

func (s *Server) getRepo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"default_branch": "main"})
}

var labelQualifier = regexp.MustCompile(`label:"([^"]+)"`)

func (s *Server) searchIssues(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	var want []string
	for _, m := range labelQualifier.FindAllStringSubmatch(query, -1) {
		want = append(want, m[1])
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	items := []*Issue{}
	for _, is := range s.issues {
		if is.State == "open" && hasLabels(is, want) {
			items = append(items, is)
		}
	}
	slices.SortFunc(items, func(a, b *Issue) int { return a.Number - b.Number })
	writeJSON(w, map[string]any{"total_count": len(items), "items": items})
}

// decodeLabels accepts both shapes GitHub's API documents for the add-labels
// endpoint: a bare array of names (what go-github sends) and an object with
// a "labels" key.
func decodeLabels(r *http.Request) ([]string, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err == nil {
		return names, nil
	}
	var wrapped struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("labels payload is neither an array nor {labels: [...]}: %w", err)
	}
	return wrapped.Labels, nil
}

func hasLabels(is *Issue, want []string) bool {
	for _, w := range want {
		if !slices.ContainsFunc(is.Labels, func(l label) bool { return l.Name == w }) {
			return false
		}
	}
	return true
}

func splitLabels(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
