// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/go-github/v93/github"
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
//
// A 404 on the first page is an empty listing, not an error: GitHub
// answers "no analysis found" for a repository code scanning has never
// analyzed, which has no alerts to backfill. Failing there would wedge a
// whole backfill on one repository without scanning.
func (c *Client) ListRepoAlerts(
	ctx context.Context, repo Repo, yield func(*Alert) bool,
) (complete bool, err error) {
	opts := &github.AlertListOptions{State: "open", ListOptions: github.ListOptions{PerPage: listPageSize}}
	for {
		alerts, resp, err := c.gh.CodeScanning.ListAlertsForRepo(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			if opts.ListOptions.Page == 0 && IsNotFound(err) {
				return true, nil
			}
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
//     inventory and listing only matching repositories, so cost scales with
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
	return appWalk(ctx, e.App, e.MaxAlerts, repos, codeScanningLists, yield)
}

// alertLists are one alert family's two listings: organization-wide and
// per repository. The enumeration walks are generic over them, so code
// scanning and secret scanning share one budget, filter and fan-out.
type alertLists[A any] struct {
	org  func(c *Client, ctx context.Context, org string, yield func(Repo, A) bool) (bool, error)
	repo func(c *Client, ctx context.Context, repo Repo, yield func(A) bool) (bool, error)
}

// codeScanningLists are the code-scanning alert listings.
var codeScanningLists = alertLists[*Alert]{
	org:  (*Client).ListOrgAlerts,
	repo: (*Client).ListRepoAlerts,
}

// appWalk is the App enumeration (AppAlertEnumerator's contract) over one
// alert family.
func appWalk[A any](
	ctx context.Context, app *App, maxAlerts int, repos []string, lists alertLists[A], yield func(Repo, A) bool,
) (complete bool, err error) {
	accounts, err := app.InstallationAccounts(ctx)
	if err != nil {
		return false, err
	}
	budget := maxAlerts
	if budget <= 0 {
		budget = DefaultMaxAlerts
	}
	metered := func(r Repo, a A) bool {
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
			c, err = listMatchingRepos(ctx, acct.Client, entries, lists, metered)
		case acct.Org:
			c, err = lists.org(acct.Client, ctx, acct.Account, metered)
		default:
			c, err = listMatchingRepos(ctx, acct.Client, nil, lists, metered)
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

// listMatchingRepos enumerates the installation's repository inventory
// and walks the alerts of the repositories matching entries (all of them
// when entries is empty). This walk's cost scales with repository count
// rather than alert count.
func listMatchingRepos[A any](
	ctx context.Context, c *Client, entries []string, lists alertLists[A], yield func(Repo, A) bool,
) (complete bool, err error) {
	repos, allRepos, err := c.InstallationRepos(ctx)
	if err != nil {
		return false, err
	}
	for _, repo := range repos {
		if len(entries) > 0 && !RepoMatches(entries, repo) {
			continue
		}
		c2, err := lists.repo(c, ctx, repo, func(a A) bool { return yield(repo, a) })
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
	for range repoPageCap {
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
// exact "owner/name". A bare "owner/" prefix is an error, not an empty
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
	return patWalk(ctx, e.Client, e.MaxAlerts, repos, codeScanningLists, yield)
}

// patWalk is the PAT enumeration (PATAlertEnumerator's contract) over one
// alert family.
func patWalk[A any](
	ctx context.Context, c *Client, maxAlerts int, repos []string, lists alertLists[A], yield func(Repo, A) bool,
) (complete bool, err error) {
	if len(repos) == 0 {
		return false, errors.New(
			"ghclient: a PAT backfill cannot discover repositories; list exact owner/name entries",
		)
	}
	budget := maxAlerts
	if budget <= 0 {
		budget = DefaultMaxAlerts
	}
	for _, entry := range repos {
		owner, name, ok := strings.Cut(entry, "/")
		if !ok || owner == "" || name == "" {
			return false, fmt.Errorf(
				"ghclient: a PAT backfill cannot expand prefix %q; list exact owner/name entries", entry,
			)
		}
		repo := Repo{Owner: owner, Name: name}
		done, err := lists.repo(c, ctx, repo, func(a A) bool {
			if budget <= 0 {
				return false
			}
			budget--
			return yield(repo, a)
		})
		if err != nil {
			return false, err
		}
		if !done {
			return false, nil
		}
	}
	return true, nil
}

// RepoMatches reports whether repo matches any filter entry. An entry is
// a prefix of "owner/name" ("owner/", "owner/name", which also matches
// "owner/name-suffix", or "owner/name-prefix-"), and a slash-less entry
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
		if before, _, ok := strings.Cut(entry, "/"); ok {
			owner = before
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

// secretScanningLists are the secret-scanning alert listings.
var secretScanningLists = alertLists[*SecretAlert]{
	org:  (*Client).ListOrgSecretAlerts,
	repo: (*Client).ListRepoSecretAlerts,
}

// AppSecretAlertEnumerator is AppAlertEnumerator for open secret-scanning
// alerts. Every listing asks GitHub to hide the secret values.
type AppSecretAlertEnumerator struct {
	App *App
	// MaxAlerts bounds the walk; <= 0 means DefaultMaxAlerts.
	MaxAlerts int
}

// Enumerate walks the App's installations' open secret-scanning alerts.
func (e *AppSecretAlertEnumerator) Enumerate(
	ctx context.Context, repos []string, yield func(Repo, *SecretAlert) bool,
) (complete bool, err error) {
	return appWalk(ctx, e.App, e.MaxAlerts, repos, secretScanningLists, yield)
}

// PATSecretAlertEnumerator is PATAlertEnumerator for open secret-scanning
// alerts: exact owner/name entries only.
type PATSecretAlertEnumerator struct {
	Client *Client
	// MaxAlerts bounds the walk; <= 0 means DefaultMaxAlerts.
	MaxAlerts int
}

// Enumerate walks the named repositories' open secret-scanning alerts.
func (e *PATSecretAlertEnumerator) Enumerate(
	ctx context.Context, repos []string, yield func(Repo, *SecretAlert) bool,
) (complete bool, err error) {
	return patWalk(ctx, e.Client, e.MaxAlerts, repos, secretScanningLists, yield)
}

// AlertKind names one alert family a repository's access is probed for.
type AlertKind string

// Alert families.
const (
	AlertKindCodeScanning   AlertKind = "code-scanning"
	AlertKindSecretScanning AlertKind = "secret-scanning"
)

// AccessStatus classifies whether a credential can read a repository's
// alerts.
type AccessStatus string

// Access statuses. NotInstalled is the App installation lookup's 404;
// ProbeAlerts itself never returns it, but ClassifyAccess does for that
// lookup's error.
const (
	// AccessOK: the listing answered, so a backfill can read the alerts.
	AccessOK AccessStatus = "ok"
	// AccessNotInstalled: the GitHub App is not installed on the repository.
	AccessNotInstalled AccessStatus = "not_installed"
	// AccessForbidden: the credential lacks security_events (or the
	// secret-scanning permission), or GitHub Advanced Security is off.
	AccessForbidden AccessStatus = "forbidden"
	// AccessNotFound: the repository is invisible to the credential, or the
	// alert family is disabled on it.
	AccessNotFound AccessStatus = "not_found"
	// AccessNoAnalysis: code scanning has never analysed the repository, so
	// there are no alerts to read yet.
	AccessNoAnalysis AccessStatus = "no_analysis"
	// AccessError: anything else (rate limits, outages); worth retrying.
	AccessError AccessStatus = "error"
)

// ProbeAlerts asks for one open alert of the given family on repo, to learn
// whether the client can read that family's alerts at all. It costs one API
// request and never returns an error: the outcome is the status, with
// GitHub's own message as the detail.
func (c *Client) ProbeAlerts(ctx context.Context, repo Repo, kind AlertKind) (AccessStatus, string) {
	var err error
	switch kind {
	case AlertKindCodeScanning:
		opts := &github.AlertListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 1}}
		_, _, err = c.gh.CodeScanning.ListAlertsForRepo(ctx, repo.Owner, repo.Name, opts)
	case AlertKindSecretScanning:
		base := fmt.Sprintf("repos/%s/%s/secret-scanning/alerts", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
		var alerts []*secretAlertJSON
		_, err = c.getJSON(ctx, base+"?state=open&per_page=1&"+hideSecret, &alerts)
	default:
		return AccessError, fmt.Sprintf("unknown alert kind %q", kind)
	}
	return ClassifyAccess(err)
}

// ClassifyAccess maps a GitHub API error onto an AccessStatus and a
// caller-safe detail. A nil error is AccessOK. A 404 from the App's
// installation lookup (GET /repos/{o}/{r}/installation) is AccessNotInstalled;
// any other 404 is AccessNotFound, or AccessNoAnalysis when code scanning
// says it has no analysis.
func ClassifyAccess(err error) (AccessStatus, string) {
	if err == nil {
		return AccessOK, ""
	}
	var rle *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &rle) || errors.As(err, &abuse) {
		return AccessError, "GitHub API rate limit exceeded"
	}
	var ger *github.ErrorResponse
	if !errors.As(err, &ger) || ger.Response == nil {
		return AccessError, err.Error()
	}
	msg := ger.Message
	switch ger.Response.StatusCode {
	case http.StatusNotFound:
		if ger.Response.Request != nil && strings.HasSuffix(ger.Response.Request.URL.Path, "/installation") {
			return AccessNotInstalled, msg
		}
		if strings.Contains(strings.ToLower(msg), "no analysis found") {
			return AccessNoAnalysis, msg
		}
		return AccessNotFound, msg
	case http.StatusForbidden, http.StatusUnauthorized:
		return AccessForbidden, msg
	default:
		if msg == "" {
			msg = ger.Response.Status
		}
		return AccessError, msg
	}
}
