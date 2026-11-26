// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/go-github/v93/github"
)

func TestRoleOf(t *testing.T) {
	yes, no := new(bool), new(bool)
	*yes = true
	cases := []struct {
		p    *github.RepositoryPermissions
		want string
	}{
		{nil, RoleNone},
		{&github.RepositoryPermissions{Admin: yes, Maintain: yes, Push: yes, Pull: yes}, RoleAdmin},
		{&github.RepositoryPermissions{Admin: no, Maintain: yes, Push: yes}, RoleMaintain},
		{&github.RepositoryPermissions{Push: yes, Pull: yes}, RoleWrite},
		{&github.RepositoryPermissions{Triage: yes, Pull: yes}, RoleTriage},
		{&github.RepositoryPermissions{Pull: yes}, RoleRead},
		{&github.RepositoryPermissions{}, RoleNone},
	}
	for _, tc := range cases {
		if got := RoleOf(tc.p); got != tc.want {
			t.Errorf("RoleOf(%+v) = %s, want %s", tc.p, got, tc.want)
		}
	}
}

func TestUserMaintainedRepositoriesLimit(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"total_count":1,"installations":[{"id":3}]}`)
	})
	mux.HandleFunc("GET /user/installations/3/repositories", func(w http.ResponseWriter, _ *http.Request) {
		body := `{"total_count":5,"repositories":[`
		for i := range 5 {
			if i > 0 {
				body += ","
			}
			body += fmt.Sprintf(`{"full_name":"acme/r%d","permissions":{"admin":true}}`, i)
		}
		writeJSON(t, w, body+`]}`)
	})
	repos, truncated, err := c.UserMaintainedRepositories(t.Context(), 3)
	if err != nil || len(repos) != 3 || !truncated || repos[0].Role != RoleAdmin {
		t.Fatalf("UserMaintainedRepositories(limit 3) = %+v, %v, %v; want 3 and truncated", repos, truncated, err)
	}
	repos, truncated, err = c.UserMaintainedRepositories(t.Context(), 10)
	if err != nil || len(repos) != 5 || truncated {
		t.Fatalf("UserMaintainedRepositories(limit 10) = %d, %v, %v; want all 5", len(repos), truncated, err)
	}
}

func TestUserRepositoryRoleNotFound(t *testing.T) {
	mux, c := newFakeClient(t)
	mux.HandleFunc("GET /repos/acme/hidden", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(t, w, `{"message":"Not Found"}`)
	})
	role, err := c.UserRepositoryRole(t.Context(), Repo{Owner: "acme", Name: "hidden"})
	if err != nil || role != RoleNone {
		t.Errorf("UserRepositoryRole(invisible) = %q, %v; want none", role, err)
	}
}
