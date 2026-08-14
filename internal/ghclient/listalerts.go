// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-github/v89/github"
)

// DefaultMaxAlerts bounds one backfill enumeration (100 alerts per page,
// so ~100 API pages). An estate that exceeds it reports itself incomplete
// rather than paging through an unbounded alert inventory; the operator
// re-runs with a narrower repository prefix.
const DefaultMaxAlerts = 10000

// ListOrgAlerts walks the organization's open code-scanning alerts,
// calling yield with each alert and the repository it was raised in,
// until yield returns false or the listing is exhausted. complete is
// false when yield ended the walk early.
func (c *Client) ListOrgAlerts(
	ctx context.Context, org string, yield func(Repo, *Alert) bool,
) (complete bool, err error) {
	opts := &github.AlertListOptions{State: "open", ListOptions: github.ListOptions{PerPage: listPageSize}}
	for {
		alerts, resp, err := c.gh.CodeScanning.ListAlertsForOrg(ctx, org, opts)
		if err != nil {
			return false, fmt.Errorf("ghclient: list %s org alerts: %w", org, err)
		}
		for _, ga := range alerts {
			repo := Repo{Owner: ga.GetRepository().GetOwner().GetLogin(), Name: ga.GetRepository().GetName()}
			if !yield(repo, alertFromGitHub(ga)) {
				return false, nil
			}
		}
		if resp.NextPage == 0 {
			return true, nil
		}
		opts.ListOptions.Page = resp.NextPage
	}
}

// ListRepoAlerts walks one repository's open code-scanning alerts,
// calling yield until it returns false or the listing is exhausted.
// complete is false when yield ended the walk early.
func (c *Client) ListRepoAlerts(
	ctx context.Context, repo Repo, yield func(*Alert) bool,
) (complete bool, err error) {
	opts := &github.AlertListOptions{State: "open", ListOptions: github.ListOptions{PerPage: listPageSize}}
	for {
		alerts, resp, err := c.gh.CodeScanning.ListAlertsForRepo(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			return false, fmt.Errorf("ghclient: list %s alerts: %w", repo, err)
		}
		for _, ga := range alerts {
			if !yield(alertFromGitHub(ga)) {
				return false, nil
			}
		}
		if resp.NextPage == 0 {
			return true, nil
		}
		opts.ListOptions.Page = resp.NextPage
	}
}

// repoPageCap bounds one InstallationRepos walk (100 repositories per
// page, so 20K repositories). The same truncation discipline as the alert
// budget: a repository inventory that exceeds it reports itself
// incomplete rather than paging without bound.
const repoPageCap = 200

// AppAlertEnumerator walks the open code-scanning alerts of every account
// the App is installed on, picking the walk whose cost matches the filter:
//
//   - an account none of the repos entries name is skipped outright;
//   - an account whose entries all carry a name segment ("owner/name",
//     "owner/name-prefix-") is walked by enumerating its repository
//     inventory and listing only matching repositories — cost scales with
//     repository count, not the org's alert count;
//   - a bare "owner/" entry (or an empty filter) keeps the full walk: the
//     single org-wide listing for an organization account, the
//     installation-repository fan-out for a user account, which has no
//     org-wide listing.
//
// The per-repository filter here is also the caller's safety net in ghas;
// applying it at both levels keeps the enumerator free to choose the
// cheap walk without changing what comes out.
type AppAlertEnumerator struct {
	App *App
	// MaxAlerts bounds the walk; <= 0 means DefaultMaxAlerts.
	MaxAlerts int
}

// Enumerate implements the backfill enumeration over the App's
// installations. complete is false when the alert budget, a repository
// page cap, or the caller's yield ended the walk before the estate was
// exhausted.
func (e *AppAlertEnumerator) Enumerate(
	ctx context.Context, repos []string, yield func(Repo, *Alert) bool,
) (complete bool, err error) {
	accounts, err := e.App.InstallationAccounts(ctx)
	if err != nil {
		return false, err
	}
	budget := e.MaxAlerts
	if budget <= 0 {
		budget = DefaultMaxAlerts
	}
	metered := func(r Repo, a *Alert) bool {
		if budget <= 0 {
			return false
		}
		budget--
		return yield(r, a)
	}
	for _, acct := range accounts {
		entries := entriesForOwner(repos, acct.Account)
		if len(repos) > 0 && len(entries) == 0 {
			continue
		}
		var c bool
		switch {
		case len(entries) > 0 && !hasBareOwner(entries):
			c, err = listMatchingRepoAlerts(ctx, acct.Client, entries, metered)
		case acct.Org:
			c, err = acct.Client.ListOrgAlerts(ctx, acct.Account, metered)
		default:
			c, err = listMatchingRepoAlerts(ctx, acct.Client, nil, metered)
		}
		if err != nil {
			return false, err
		}
		if !c {
			return false, nil
		}
	}
	return true, nil
}

// listMatchingRepoAlerts enumerates the installation's repository
// inventory and walks the alerts of the repositories matching entries
// (all of them when entries is empty) — the walk whose cost scales with
// repository count rather than alert count.
func listMatchingRepoAlerts(
	ctx context.Context, c *Client, entries []string, yield func(Repo, *Alert) bool,
) (complete bool, err error) {
	repos, allRepos, err := c.InstallationRepos(ctx)
	if err != nil {
		return false, err
	}
	for _, repo := range repos {
		if len(entries) > 0 && !RepoMatches(entries, repo) {
			continue
		}
		c2, err := c.ListRepoAlerts(ctx, repo, func(a *Alert) bool { return yield(repo, a) })
		if err != nil {
			return false, err
		}
		if !c2 {
			return false, nil
		}
	}
	// A truncated repository inventory means unmatched repositories may
	// exist beyond the cap; the walk is incomplete even though every
	// listed repository was covered.
	return allRepos, nil
}

// InstallationRepos lists the repositories the installation's token can
// see (GET /installation/repositories). complete is false when the page
// cap ended the walk before the inventory was exhausted.
func (c *Client) InstallationRepos(ctx context.Context) (repos []Repo, complete bool, err error) {
	opts := &github.ListOptions{PerPage: listPageSize}
	for page := 0; page < repoPageCap; page++ {
		list, resp, err := c.gh.Apps.ListRepos(ctx, opts)
		if err != nil {
			return nil, false, fmt.Errorf("ghclient: list installation repositories: %w", err)
		}
		for _, r := range list.Repositories {
			repos = append(repos, Repo{Owner: r.GetOwner().GetLogin(), Name: r.GetName()})
		}
		if resp.NextPage == 0 {
			return repos, true, nil
		}
		opts.Page = resp.NextPage
	}
	return repos, false, nil
}

// PATAlertEnumerator walks the open code-scanning alerts of an explicit
// repository list with a PAT client. A PAT sees no installation
// inventory, so it cannot discover repositories: every entry must be an
// exact "owner/name" — a bare "owner/" prefix is an error, not an empty
// result.
type PATAlertEnumerator struct {
	Client *Client
	// MaxAlerts bounds the walk; <= 0 means DefaultMaxAlerts.
	MaxAlerts int
}

// Enumerate implements the backfill enumeration over the exact
// repositories named. complete is false when the alert budget (or the
// caller's yield) ended the walk early.
func (e *PATAlertEnumerator) Enumerate(
	ctx context.Context, repos []string, yield func(Repo, *Alert) bool,
) (complete bool, err error) {
	if len(repos) == 0 {
		return false, errors.New(
			"ghclient: a PAT backfill cannot discover repositories; list exact owner/name entries")
	}
	budget := e.MaxAlerts
	if budget <= 0 {
		budget = DefaultMaxAlerts
	}
	for _, entry := range repos {
		owner, name, ok := strings.Cut(entry, "/")
		if !ok || owner == "" || name == "" {
			return false, fmt.Errorf(
				"ghclient: a PAT backfill cannot expand prefix %q; list exact owner/name entries", entry)
		}
		repo := Repo{Owner: owner, Name: name}
		c, err := e.Client.ListRepoAlerts(ctx, repo, func(a *Alert) bool {
			if budget <= 0 {
				return false
			}
			budget--
			return yield(repo, a)
		})
		if err != nil {
			return false, err
		}
		if !c {
			return false, nil
		}
	}
	return true, nil
}

// RepoMatches reports whether repo matches any filter entry: an entry is
// a prefix of "owner/name" — "owner/", "owner/name" (which also matches
// "owner/name-suffix"), or "owner/name-prefix-" — and a slash-less entry
// is normalized to an owner prefix, so "acme" cannot match "acmex/..".
// Matching is case-insensitive, as GitHub owner and repository names are.
// An empty filter matches everything.
func RepoMatches(entries []string, repo Repo) bool {
	if len(entries) == 0 {
		return true
	}
	full := strings.ToLower(repo.String())
	for _, entry := range entries {
		e := strings.ToLower(entry)
		if !strings.Contains(e, "/") {
			e += "/"
		}
		if strings.HasPrefix(full, e) {
			return true
		}
	}
	return false
}

// entriesForOwner returns the filter entries whose owner segment is
// account, case-insensitively.
func entriesForOwner(repos []string, account string) []string {
	var out []string
	for _, entry := range repos {
		owner := entry
		if i := strings.IndexByte(entry, '/'); i >= 0 {
			owner = entry[:i]
		}
		if strings.EqualFold(owner, account) {
			out = append(out, entry)
		}
	}
	return out
}

// hasBareOwner reports whether any entry names a whole owner ("acme" or
// "acme/") rather than a repository name or name prefix.
func hasBareOwner(entries []string) bool {
	for _, entry := range entries {
		i := strings.IndexByte(entry, '/')
		if i < 0 || i == len(entry)-1 {
			return true
		}
	}
	return false
}
