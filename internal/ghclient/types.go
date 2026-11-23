// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import "time"

// Issue is patchy's thin view of a GitHub issue, decoupled from go-github.
type Issue struct {
	// Repo is the issue's repository, populated from the list/search
	// response so cross-repository sweeps can act on their results.
	Repo      Repo
	Number    int
	Title     string
	Body      string
	State     string
	Labels    []string
	Assignees []string
	CreatedAt time.Time
}

// Comment is one issue comment.
type Comment struct {
	ID                int64
	Body              string
	UserLogin         string
	AuthorAssociation string
}

// PR is a freshly created pull request.
type PR struct {
	Number  int
	HTMLURL string
}

// IssueRequest is the payload for creating an issue.
type IssueRequest struct {
	Title  string
	Body   string
	Labels []string
}

// PRRequest is the payload for creating a pull request.
type PRRequest struct {
	Title string
	Head  string
	Base  string
	Body  string
}

// Alert is patchy's view of a code-scanning alert: the rule, the severity
// (security_severity_level falling back to the rule severity), and the most
// recent instance's location, message snippet, and commit.
type Alert struct {
	Number          int
	RuleID          string
	RuleName        string
	RuleDescription string
	RuleHelp        string
	Tags            []string
	Severity        string
	State           string
	HTMLURL         string
	Path            string
	StartLine       int
	EndLine         int
	Snippet         string
	MostRecentSHA   string
}

// SecretAlert is patchy's view of a secret-scanning alert. It deliberately
// has no field for the secret value: the client asks GitHub to hide it
// (hide_secret=true) and never reads it even if one comes back, so the
// value cannot reach a Finding, an issue, a prompt or a log through here.
type SecretAlert struct {
	Number                 int
	SecretType             string
	SecretTypeDisplayName  string
	Provider               string
	Validity               string
	PubliclyLeaked         bool
	MultiRepo              bool
	PushProtectionBypassed bool
	State                  string
	HTMLURL                string
	Locations              []SecretLocation
}

// SecretLocation is one place a secret was found. Only commit and
// wiki-commit locations name a file; the other kinds (an issue body, a pull
// request comment, a discussion) carry just the URL of where it was seen.
type SecretLocation struct {
	// Type is GitHub's location kind ("commit", "issue_body", ...).
	Type        string
	Path        string
	StartLine   int
	EndLine     int
	StartColumn int
	EndColumn   int
	CommitSHA   string
	// URL links to the location on GitHub: the blob for a commit location,
	// the issue, comment or discussion otherwise.
	URL string
}
