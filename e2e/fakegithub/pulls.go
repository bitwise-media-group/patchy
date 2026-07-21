// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package fakegithub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// pull is the fake's pull-request record.
type pull struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Head    ref    `json:"head"`
	Base    ref    `json:"base"`
}

type ref struct {
	Ref string `json:"ref"`
}

// Pulls returns a snapshot of every pull request, ordered by number.
func (s *Server) Pulls() []struct {
	Number int
	Head   string
	State  string
} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]struct {
		Number int
		Head   string
		State  string
	}, 0, len(s.pulls))
	for _, p := range s.pulls {
		out = append(out, struct {
			Number int
			Head   string
			State  string
		}{p.Number, p.Head.Ref, p.State})
	}
	return out
}

// createPull answers POST /repos/{o}/{r}/pulls.
func (s *Server) createPull(w http.ResponseWriter, r *http.Request) {
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	var body struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.next++
	p := &pull{
		Number:  s.next,
		HTMLURL: fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, s.next),
		State:   "open",
		Title:   body.Title,
		Body:    body.Body,
		Head:    ref{Ref: body.Head},
		Base:    ref{Ref: body.Base},
	}
	s.pulls[p.Number] = p
	s.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, p)
}

// listPulls answers GET /repos/{o}/{r}/pulls — enough of the list API for
// FindPRByHead (state + head filters; head arrives as "owner:branch").
func (s *Server) listPulls(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	head := r.URL.Query().Get("head")
	if _, branch, ok := strings.Cut(head, ":"); ok {
		head = branch
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*pull, 0, len(s.pulls))
	for _, p := range s.pulls {
		if state != "" && state != "all" && p.State != state {
			continue
		}
		if head != "" && p.Head.Ref != head {
			continue
		}
		out = append(out, p)
	}
	writeJSON(w, out)
}
