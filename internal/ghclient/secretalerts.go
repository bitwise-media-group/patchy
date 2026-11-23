// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/google/go-github/v93/github"
)

// maxSecretLocations bounds the locations fetched for one secret alert: a
// single API page. A credential pasted into hundreds of files is still one
// finding, and the tracking issue and the agent need a representative set,
// not an inventory.
const maxSecretLocations = 100

// secretAlertJSON is the part of a secret-scanning alert patchy decodes. It
// has no field for the secret: encoding/json drops keys a struct does not
// name, so even a response that ignored hide_secret=true cannot put the
// value in memory this package keeps.
type secretAlertJSON struct {
	Number                 int                 `json:"number"`
	State                  string              `json:"state"`
	HTMLURL                string              `json:"html_url"`
	SecretType             string              `json:"secret_type"`
	SecretTypeDisplayName  string              `json:"secret_type_display_name"`
	Provider               string              `json:"provider"`
	Validity               string              `json:"validity"`
	PubliclyLeaked         bool                `json:"publicly_leaked"`
	MultiRepo              bool                `json:"multi_repo"`
	PushProtectionBypassed bool                `json:"push_protection_bypassed"`
	FirstLocationDetected  *secretLocationJSON `json:"first_location_detected"`
	Repository             *struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

// secretLocationJSON is one location's details. Commit locations carry the
// file coordinates; every other kind carries one "<kind>_url" link, which
// URLs collects.
type secretLocationJSON struct {
	Path        string `json:"path"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	StartColumn int    `json:"start_column"`
	EndColumn   int    `json:"end_column"`
	CommitSHA   string `json:"commit_sha"`
	BlobURL     string `json:"blob_url"`
	CommitURL   string `json:"commit_url"`
	// The non-commit location kinds' links, by their documented names.
	IssueTitleURL               string `json:"issue_title_url"`
	IssueBodyURL                string `json:"issue_body_url"`
	IssueCommentURL             string `json:"issue_comment_url"`
	DiscussionTitleURL          string `json:"discussion_title_url"`
	DiscussionBodyURL           string `json:"discussion_body_url"`
	DiscussionCommentURL        string `json:"discussion_comment_url"`
	PullRequestTitleURL         string `json:"pull_request_title_url"`
	PullRequestBodyURL          string `json:"pull_request_body_url"`
	PullRequestCommentURL       string `json:"pull_request_comment_url"`
	PullRequestReviewURL        string `json:"pull_request_review_url"`
	PullRequestReviewCommentURL string `json:"pull_request_review_comment_url"`
}

// link is the location's one URL: the blob for a commit, the issue,
// discussion or pull request element otherwise.
func (d *secretLocationJSON) link() string {
	for _, u := range []string{
		d.BlobURL, d.CommitURL,
		d.IssueTitleURL, d.IssueBodyURL, d.IssueCommentURL,
		d.DiscussionTitleURL, d.DiscussionBodyURL, d.DiscussionCommentURL,
		d.PullRequestTitleURL, d.PullRequestBodyURL, d.PullRequestCommentURL,
		d.PullRequestReviewURL, d.PullRequestReviewCommentURL,
	} {
		if u != "" {
			return u
		}
	}
	return ""
}

// location maps the details onto a SecretLocation of kind typ. Only the
// commit kinds name a file; the coordinates of any other kind are dropped
// because they would point into a repository file that does not exist.
func (d *secretLocationJSON) location(typ string) SecretLocation {
	loc := SecretLocation{Type: typ, URL: d.link()}
	if typ == "commit" || typ == "wiki_commit" {
		loc.Path = d.Path
		loc.StartLine = d.StartLine
		loc.EndLine = d.EndLine
		loc.StartColumn = d.StartColumn
		loc.EndColumn = d.EndColumn
		loc.CommitSHA = d.CommitSHA
	}
	return loc
}

// toSecretAlert maps the decoded alert. A list response carries no
// locations, only the first one detected; that is the alert's location
// until a GetSecretAlert replaces it with the full set.
func (j *secretAlertJSON) toSecretAlert() *SecretAlert {
	a := &SecretAlert{
		Number:                 j.Number,
		SecretType:             j.SecretType,
		SecretTypeDisplayName:  j.SecretTypeDisplayName,
		Provider:               j.Provider,
		Validity:               j.Validity,
		PubliclyLeaked:         j.PubliclyLeaked,
		MultiRepo:              j.MultiRepo,
		PushProtectionBypassed: j.PushProtectionBypassed,
		State:                  j.State,
		HTMLURL:                j.HTMLURL,
	}
	if first := j.FirstLocationDetected; first != nil && first.Path != "" {
		a.Locations = []SecretLocation{first.location("commit")}
	}
	return a
}

// repo is the alert's repository, from an organization listing.
func (j *secretAlertJSON) repo() Repo {
	if j.Repository == nil {
		return Repo{}
	}
	return Repo{Owner: j.Repository.Owner.Login, Name: j.Repository.Name}
}

// hideSecret asks GitHub to omit the secret value from the response.
// go-github's SecretScanningService cannot pass it, which is why every
// secret-scanning read here builds its own request.
const hideSecret = "hide_secret=true"

// getJSON issues one GET for path (relative to the API root, query
// included) and decodes the response into v.
func (c *Client) getJSON(ctx context.Context, path string, v any) (*github.Response, error) {
	req, err := c.gh.NewRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	return c.gh.Do(req, v)
}

// GetSecretAlert fetches one secret-scanning alert, without its secret, and
// up to maxSecretLocations of its locations.
func (c *Client) GetSecretAlert(ctx context.Context, repo Repo, number int) (*SecretAlert, error) {
	var j secretAlertJSON
	path := fmt.Sprintf("repos/%s/%s/secret-scanning/alerts/%d?%s",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number, hideSecret)
	if _, err := c.getJSON(ctx, path, &j); err != nil {
		return nil, fmt.Errorf("ghclient: get secret alert %s#%d: %w", repo, number, err)
	}
	a := j.toSecretAlert()
	locs, err := c.secretLocations(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	if len(locs) > 0 {
		a.Locations = locs
	}
	return a, nil
}

// secretLocations lists one page of an alert's locations.
func (c *Client) secretLocations(ctx context.Context, repo Repo, number int) ([]SecretLocation, error) {
	var raw []struct {
		Type    string             `json:"type"`
		Details secretLocationJSON `json:"details"`
	}
	path := fmt.Sprintf("repos/%s/%s/secret-scanning/alerts/%d/locations?per_page=%d&%s",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number, maxSecretLocations, hideSecret)
	if _, err := c.getJSON(ctx, path, &raw); err != nil {
		return nil, fmt.Errorf("ghclient: list secret alert %s#%d locations: %w", repo, number, err)
	}
	out := make([]SecretLocation, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.Details.location(r.Type))
	}
	return out, nil
}

// Secret-scanning resolutions patchy writes.
const (
	SecretResolutionFalsePositive = "false_positive"
	SecretResolutionUsedInTests   = "used_in_tests"
	SecretResolutionWontFix       = "wont_fix"
)

// ResolveSecretAlert closes a secret-scanning alert with resolution, one of
// GitHub's false_positive, used_in_tests, wont_fix or revoked.
func (c *Client) ResolveSecretAlert(ctx context.Context, repo Repo, number int, resolution, comment string) error {
	body := github.UpdateSecretScanningAlertRequest{
		State:             new("resolved"),
		Resolution:        new(resolution),
		ResolutionComment: new(comment),
	}
	if err := c.patchSecretAlert(ctx, repo, number, body); err != nil {
		return fmt.Errorf("ghclient: resolve secret alert %s#%d: %w", repo, number, err)
	}
	return nil
}

// patchSecretAlert issues the alert update. The response echoes the alert,
// secret included, so it is discarded unread rather than decoded.
func (c *Client) patchSecretAlert(
	ctx context.Context, repo Repo, number int, body github.UpdateSecretScanningAlertRequest,
) error {
	path := fmt.Sprintf("repos/%s/%s/secret-scanning/alerts/%d",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
	req, err := c.gh.NewRequest(ctx, "PATCH", path, body)
	if err != nil {
		return err
	}
	_, err = c.gh.Do(req, nil)
	return err
}

// secretStateResolved is the state a triage decision leaves a secret alert
// in, and the only one a reopen is legal from.
const secretStateResolved = "resolved"

// ReopenSecretAlert reopens a resolved secret-scanning alert, with the same
// convergent semantics as OpenAlert: an alert that is already open, or gone,
// has no resolution left to undo, so both are success.
func (c *Client) ReopenSecretAlert(ctx context.Context, repo Repo, number int) error {
	var j secretAlertJSON
	path := fmt.Sprintf("repos/%s/%s/secret-scanning/alerts/%d?%s",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number, hideSecret)
	if _, err := c.getJSON(ctx, path, &j); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("ghclient: get secret alert %s#%d: %w", repo, number, err)
	}
	if j.State != secretStateResolved {
		return nil
	}
	body := github.UpdateSecretScanningAlertRequest{State: new("open")}
	if err := c.patchSecretAlert(ctx, repo, number, body); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("ghclient: reopen secret alert %s#%d: %w", repo, number, err)
	}
	return nil
}

// secretListQuery is the query of one secret-alert list page: open alerts,
// secrets hidden, a full page, continuing from the previous page's cursor
// (github.com) or page number (GHES paginates by index).
func secretListQuery(after string, page int) string {
	q := url.Values{}
	q.Set("state", "open")
	q.Set("hide_secret", "true")
	q.Set("per_page", strconv.Itoa(listPageSize))
	if after != "" {
		q.Set("after", after)
	} else if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	return q.Encode()
}

// listSecretPages walks a secret-alert listing at base, calling each for
// every alert until it returns false or the listing is exhausted.
func (c *Client) listSecretPages(
	ctx context.Context, base, what string, each func(*secretAlertJSON) bool,
) (complete bool, err error) {
	after, page := "", 0
	for {
		var alerts []*secretAlertJSON
		resp, err := c.getJSON(ctx, base+"?"+secretListQuery(after, page), &alerts)
		if err != nil {
			return false, fmt.Errorf("ghclient: list %s secret alerts: %w", what, err)
		}
		for _, a := range alerts {
			if !each(a) {
				return false, nil
			}
		}
		switch {
		case resp.After != "" && resp.After != after:
			after = resp.After
		case resp.After == "" && resp.NextPage != 0:
			page = resp.NextPage
		default:
			return true, nil
		}
	}
}

// ListOrgSecretAlerts walks the organization's open secret-scanning
// alerts, with ListOrgAlerts' contract.
func (c *Client) ListOrgSecretAlerts(
	ctx context.Context, org string, yield func(Repo, *SecretAlert) bool,
) (complete bool, err error) {
	base := fmt.Sprintf("orgs/%s/secret-scanning/alerts", url.PathEscape(org))
	return c.listSecretPages(ctx, base, org+" org", func(j *secretAlertJSON) bool {
		return yield(j.repo(), j.toSecretAlert())
	})
}

// ListRepoSecretAlerts walks one repository's open secret-scanning alerts,
// with ListRepoAlerts' contract.
func (c *Client) ListRepoSecretAlerts(
	ctx context.Context, repo Repo, yield func(*SecretAlert) bool,
) (complete bool, err error) {
	base := fmt.Sprintf("repos/%s/%s/secret-scanning/alerts", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	return c.listSecretPages(ctx, base, repo.String(), func(j *secretAlertJSON) bool {
		return yield(j.toSecretAlert())
	})
}
