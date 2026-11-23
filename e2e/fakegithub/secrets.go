// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package fakegithub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
)

// SecretAlertType is the secret type every fake secret-scanning alert
// reports.
const SecretAlertType = "patchy_e2e_fixture_token"

// secretState is the fake's secret-scanning record: the resolution log and
// the seeded alerts the list endpoints serve.
type secretState struct {
	resolved map[int]string
	alerts   []seededAlert
}

// secretRoutes registers the secret-scanning surface. Every read rejects a
// request without hide_secret=true, so an e2e run proves patchy never asks
// for the value; with it, the value is omitted as GitHub omits it.
func (s *Server) secretRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/secret-scanning/alerts/{number}", s.getSecretAlert)
	mux.HandleFunc("GET /repos/{owner}/{repo}/secret-scanning/alerts/{number}/locations", s.secretLocations)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/secret-scanning/alerts/{number}", s.updateSecretAlert)
	mux.HandleFunc("GET /repos/{owner}/{repo}/secret-scanning/alerts", s.listRepoSecretAlerts)
	mux.HandleFunc("GET /orgs/{org}/secret-scanning/alerts", s.listOrgSecretAlerts)
}

// requireHidden answers 400 unless the request asked for the secret to be
// hidden.
func requireHidden(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Get("hide_secret") != "true" {
		http.Error(w, `{"message":"fakegithub: secret-scanning reads must pass hide_secret=true"}`,
			http.StatusBadRequest)
		return false
	}
	return true
}

// SeedSecretAlert records a pre-existing open secret-scanning alert for a
// backfill to find.
func (s *Server) SeedSecretAlert(owner, repo string, number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets.alerts = append(s.secrets.alerts, seededAlert{owner: owner, repo: repo, number: number})
}

// ResolvedSecrets returns each resolved secret alert's resolution, by
// alert number.
func (s *Server) ResolvedSecrets() map[int]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int]string, len(s.secrets.resolved))
	for n, r := range s.secrets.resolved {
		out[n] = r
	}
	return out
}

// secretAlertBody is the alert as GitHub serves it with hide_secret=true:
// no "secret" key.
func secretAlertBody(owner, repo string, number int, state string, embedRepo bool) map[string]any {
	body := map[string]any{
		"number":                   number,
		"state":                    state,
		"html_url":                 fmt.Sprintf("https://github.com/%s/%s/security/secret-scanning/%d", owner, repo, number),
		"secret_type":              SecretAlertType,
		"secret_type_display_name": "Patchy E2E Fixture Token",
		"provider":                 "Patchy",
		"validity":                 "unknown",
		"publicly_leaked":          false,
		"multi_repo":               false,
		"push_protection_bypassed": false,
		"first_location_detected": map[string]any{
			"path": "config/app.yaml", "start_line": 4, "end_line": 4, "start_column": 10, "end_column": 46,
		},
	}
	if embedRepo {
		body["repository"] = map[string]any{"name": repo, "owner": map[string]any{"login": owner}}
	}
	return body
}

func (s *Server) secretAlertState(number int) string {
	if _, ok := s.secrets.resolved[number]; ok {
		return "resolved"
	}
	return "open"
}

func (s *Server) getSecretAlert(w http.ResponseWriter, r *http.Request) {
	if !requireHidden(w, r) {
		return
	}
	number, _ := strconv.Atoi(r.PathValue("number"))
	s.mu.Lock()
	state := s.secretAlertState(number)
	s.mu.Unlock()
	writeJSON(w, secretAlertBody(r.PathValue("owner"), r.PathValue("repo"), number, state, false))
}

func (s *Server) secretLocations(w http.ResponseWriter, r *http.Request) {
	if !requireHidden(w, r) {
		return
	}
	writeJSON(w, []map[string]any{{
		"type": "commit",
		"details": map[string]any{
			"path": "config/app.yaml", "start_line": 4, "end_line": 4, "start_column": 10, "end_column": 46,
			"commit_sha": "abc123",
		},
	}})
}

func (s *Server) updateSecretAlert(w http.ResponseWriter, r *http.Request) {
	number, _ := strconv.Atoi(r.PathValue("number"))
	var body struct {
		State      string `json:"state"`
		Resolution string `json:"resolution"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	switch body.State {
	case "resolved":
		s.secrets.resolved[number] = body.Resolution
	case "open":
		delete(s.secrets.resolved, number)
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"number": number, "state": body.State})
}

func (s *Server) listSecretAlerts(w http.ResponseWriter, r *http.Request, keep func(seededAlert) bool, embedRepo bool) {
	if !requireHidden(w, r) {
		return
	}
	wantState := r.URL.Query().Get("state")
	if wantState == "" {
		wantState = "open"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	for _, a := range s.secrets.alerts {
		if !keep(a) {
			continue
		}
		if state := s.secretAlertState(a.number); state == wantState {
			out = append(out, secretAlertBody(a.owner, a.repo, a.number, state, embedRepo))
		}
	}
	slices.SortFunc(out, func(a, b map[string]any) int { return a["number"].(int) - b["number"].(int) })
	writeJSON(w, out)
}

func (s *Server) listRepoSecretAlerts(w http.ResponseWriter, r *http.Request) {
	owner, repo := r.PathValue("owner"), r.PathValue("repo")
	s.listSecretAlerts(w, r, func(a seededAlert) bool { return a.owner == owner && a.repo == repo }, false)
}

func (s *Server) listOrgSecretAlerts(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("org")
	s.listSecretAlerts(w, r, func(a seededAlert) bool { return a.owner == org }, true)
}
