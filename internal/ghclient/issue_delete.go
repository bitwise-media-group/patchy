// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/go-github/v89/github"
)

// ErrDeleteUnauthorized reports the credential cannot delete issues:
// GitHub restricts the deleteIssue mutation to admin-permission user
// tokens — App installation tokens and fine-grained PATs are always
// refused ("Viewer not authorized to delete"). Callers may fall back
// to closing the issue.
var ErrDeleteUnauthorized = errors.New("credential not authorized to delete issues")

// DeleteIssue permanently deletes an issue. The REST API cannot delete
// issues at all — only the GraphQL deleteIssue mutation can, and it needs
// the issue's node ID, so this fetches the issue first. Deleting requires
// admin-level repository permission; it exists for the demo reset, which
// removes the tracking issues a replayed demo would otherwise duplicate.
// A missing issue is success: deletion is idempotent.
func (c *Client) DeleteIssue(ctx context.Context, repo Repo, number int) error {
	is, _, err := c.gh.Issues.Get(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("ghclient: get issue %s#%d: %w", repo, number, err)
	}

	payload, err := json.Marshal(map[string]any{
		"query":     `mutation($id: ID!) { deleteIssue(input: {issueId: $id}) { clientMutationId } }`,
		"variables": map[string]any{"id": is.GetNodeID()},
	})
	if err != nil {
		return fmt.Errorf("ghclient: delete issue %s#%d: %w", repo, number, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlURL(c.gh), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("ghclient: delete issue %s#%d: %w", repo, number, err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.gh.Client().Do(req)
	if err != nil {
		return fmt.Errorf("ghclient: delete issue %s#%d: %w", repo, number, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("ghclient: delete issue %s#%d: %w", repo, number, err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("ghclient: delete issue %s#%d: graphql status %d", repo, number, res.StatusCode)
	}
	// GraphQL reports failure as 200 + errors; surface the first message.
	var out struct {
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("ghclient: delete issue %s#%d: decode graphql response: %w", repo, number, err)
	}
	if len(out.Errors) > 0 {
		e := out.Errors[0]
		if e.Type == "FORBIDDEN" || strings.Contains(strings.ToLower(e.Message), "not authorized") {
			return fmt.Errorf("ghclient: delete issue %s#%d: %s: %w", repo, number, e.Message, ErrDeleteUnauthorized)
		}
		return fmt.Errorf("ghclient: delete issue %s#%d: %s", repo, number, e.Message)
	}
	return nil
}

// graphqlURL derives the GraphQL endpoint from the client's REST root:
// https://api.github.com -> https://api.github.com/graphql, and the GHES
// convention https://ghes/api/v3 -> https://ghes/api/graphql.
func graphqlURL(gh *github.Client) string {
	root := apiRoot(gh)
	if strings.HasSuffix(root, "/api/v3") {
		return strings.TrimSuffix(root, "/v3") + "/graphql"
	}
	return root + "/graphql"
}
