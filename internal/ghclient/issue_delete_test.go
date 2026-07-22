// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newFakeGraphQLClient serves both the GHES-style REST prefix (/api/v3) and
// the GraphQL endpoint (/api/graphql) — newFakeClient's mux strips only the
// REST prefix, which would 404 the mutation.
func newFakeGraphQLClient(t *testing.T, graphql http.HandlerFunc) (*http.ServeMux, *Client) {
	t.Helper()
	rest := http.NewServeMux()
	root := http.NewServeMux()
	root.Handle("/api/v3/", http.StripPrefix("/api/v3", rest))
	root.HandleFunc("POST /api/graphql", graphql)
	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	c, err := NewToken("pat-token", srv.URL)
	if err != nil {
		t.Fatalf("NewToken() error = %v", err)
	}
	return rest, c
}

func TestDeleteIssue(t *testing.T) {
	var deleted bool
	rest, c := newFakeGraphQLClient(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody[struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}](t, r)
		if !strings.Contains(body.Query, "deleteIssue") {
			t.Errorf("query = %q, want a deleteIssue mutation", body.Query)
		}
		if got := body.Variables["id"]; got != "I_node7" {
			t.Errorf("issue id variable = %v, want I_node7", got)
		}
		deleted = true
		writeJSON(t, w, `{"data":{"deleteIssue":{"clientMutationId":null}}}`)
	})
	rest.HandleFunc("GET /repos/o/r/issues/7", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"number":7,"node_id":"I_node7"}`)
	})

	if err := c.DeleteIssue(context.Background(), testRepo, 7); err != nil {
		t.Fatalf("DeleteIssue() error = %v", err)
	}
	if !deleted {
		t.Error("deleteIssue mutation never reached the server")
	}
}

func TestDeleteIssueMissingIsSuccess(t *testing.T) {
	rest, c := newFakeGraphQLClient(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("graphql called for a missing issue")
	})
	rest.HandleFunc("GET /repos/o/r/issues/7", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})

	if err := c.DeleteIssue(context.Background(), testRepo, 7); err != nil {
		t.Errorf("DeleteIssue() error = %v, want nil for a missing issue", err)
	}
}

func TestDeleteIssueGraphQLError(t *testing.T) {
	rest, c := newFakeGraphQLClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"data":null,"errors":[{"message":"must have admin rights"}]}`)
	})
	rest.HandleFunc("GET /repos/o/r/issues/7", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"number":7,"node_id":"I_node7"}`)
	})

	err := c.DeleteIssue(context.Background(), testRepo, 7)
	if err == nil || !strings.Contains(err.Error(), "admin rights") {
		t.Errorf("DeleteIssue() error = %v, want the graphql error surfaced", err)
	}
}
