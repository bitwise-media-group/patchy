// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package repoaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The endpoint's fixed contract.
const (
	// Path is the endpoint's route on the internal listener.
	Path = "/internal/v1/repository-access"
	// TokenHeader carries the caller's projected ServiceAccount token.
	TokenHeader = "X-Patchy-Caller-Token"
	// Audience is the projected-token audience the endpoint accepts.
	Audience = "patchy-integration-controller"
	// MaxRepositories bounds one request: each repository costs a few GitHub
	// API requests per Integration.
	MaxRepositories = 20
	// maxRepositoryLength matches the backfill filter's entry limit.
	maxRepositoryLength = 200
)

// Status is whether one alert family of one repository is readable through
// one Integration. The values match internal/ghclient's AccessStatus.
type Status string

// Alert access statuses.
const (
	StatusOK           Status = "ok"
	StatusNotInstalled Status = "not_installed"
	StatusForbidden    Status = "forbidden"
	StatusNotFound     Status = "not_found"
	StatusNoAnalysis   Status = "no_analysis"
	StatusError        Status = "error"
)

// ForgeStatus is whether a Forge can remediate the repository.
type ForgeStatus string

// Forge coverage statuses.
const (
	// ForgeOK: a Forge covers the repository, and its credential can write
	// contents (or is a PAT, whose scope patchy cannot see).
	ForgeOK ForgeStatus = "ok"
	// ForgeMissing: no Forge covers the repository.
	ForgeMissing ForgeStatus = "missing"
	// ForgeAmbiguous: several Forges cover it equally.
	ForgeAmbiguous ForgeStatus = "ambiguous"
	// ForgeNoWrite: the covering Forge's App cannot write contents.
	ForgeNoWrite ForgeStatus = "no_write"
	// ForgeError: the check itself failed.
	ForgeError ForgeStatus = "error"
)

// Contents permission levels a Forge credential reports.
const (
	ContentsNone    = "none"
	ContentsRead    = "read"
	ContentsWrite   = "write"
	ContentsUnknown = "unknown" // a PAT: patchy cannot see its scopes
)

// Request is the endpoint's body.
type Request struct {
	// Repositories are exact "owner/name" entries, at most MaxRepositories.
	Repositories []string `json:"repositories"`
}

// Response is the endpoint's answer: one result per requested repository,
// in request order.
type Response struct {
	Results []Result `json:"results"`
}

// Result is one repository's access.
type Result struct {
	Repo string `json:"repo"`
	// Integrations are the eligible github Integrations (not suspended,
	// with an alert family enabled), sorted by name.
	Integrations []IntegrationAccess `json:"integrations"`
	// Forge reports remediation coverage.
	Forge *ForgeAccess `json:"forge,omitempty"`
}

// IntegrationAccess is one Integration's view of one repository.
type IntegrationAccess struct {
	Name   string `json:"name"`
	Alerts Alerts `json:"alerts"`
}

// Alerts holds one check per alert family the Integration ingests; a family
// it does not ingest is absent.
type Alerts struct {
	CodeScanning   *Check `json:"codeScanning,omitempty"`
	SecretScanning *Check `json:"secretScanning,omitempty"`
}

// Check is one probe's outcome. Detail carries GitHub's own message.
type Check struct {
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Readable reports whether any of the Integration's alert families answered.
func (ia IntegrationAccess) Readable() bool {
	for _, c := range []*Check{ia.Alerts.CodeScanning, ia.Alerts.SecretScanning} {
		if c != nil && c.Status == StatusOK {
			return true
		}
	}
	return false
}

// ForgeAccess is the Forge coverage of one repository.
type ForgeAccess struct {
	// Name is the covering Forge, empty when none covers it.
	Name   string      `json:"name,omitempty"`
	Status ForgeStatus `json:"status"`
	// Contents is the credential's contents permission (Contents*).
	Contents string `json:"contents,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Backfillable returns the first Integration, by name, that can read the
// repository's alerts: the one a one-repository backfill is stamped on.
func (r Result) Backfillable() (string, bool) {
	for _, ia := range r.Integrations {
		if ia.Readable() {
			return ia.Name, true
		}
	}
	return "", false
}

// repoPattern is an exact GitHub "owner/name": owners are alphanumerics and
// hyphens, names add dots and underscores.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*)/[A-Za-z0-9._-]+$`)

// ValidRepository reports whether s is an exact "owner/name".
func ValidRepository(s string) bool {
	return len(s) <= maxRepositoryLength && repoPattern.MatchString(s) &&
		!strings.HasSuffix(s, "/.") && !strings.HasSuffix(s, "/..")
}

// Validate checks a request's repository list.
func (req Request) Validate() error {
	if len(req.Repositories) == 0 {
		return errors.New("no repositories")
	}
	if len(req.Repositories) > MaxRepositories {
		return fmt.Errorf("at most %d repositories", MaxRepositories)
	}
	for _, r := range req.Repositories {
		if !ValidRepository(r) {
			return fmt.Errorf("repository %q is not an exact owner/name", r)
		}
	}
	return nil
}

// ErrUnavailable reports that the endpoint is not configured.
var ErrUnavailable = errors.New("repository access check unavailable")

// Client calls the endpoint. The zero URL disables it (Check returns
// ErrUnavailable).
type Client struct {
	// URL is the integration-controller's internal base URL.
	URL string
	// TokenFile is the projected ServiceAccount token; it is read again on
	// every call, because the kubelet rotates it.
	TokenFile string
	// HTTP defaults to a client with a 30-second timeout.
	HTTP *http.Client
}

// defaultHTTP bounds a check: up to MaxRepositories repositories, each a few
// GitHub requests per Integration.
var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

// Enabled reports whether the client has an endpoint to call.
func (c *Client) Enabled() bool { return c != nil && c.URL != "" }

// Check asks the endpoint about repos and returns the results keyed by
// repository.
func (c *Client) Check(ctx context.Context, repos []string) (map[string]Result, error) {
	if !c.Enabled() {
		return nil, ErrUnavailable
	}
	req := Request{Repositories: slices.Clone(repos)}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("read caller token: %w", err)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSuffix(c.URL, "/") + Path
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set(TokenHeader, strings.TrimSpace(string(token)))
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("repository access: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("repository access: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out Response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("repository access: decode: %w", err)
	}
	byRepo := make(map[string]Result, len(out.Results))
	for _, r := range out.Results {
		byRepo[r.Repo] = r
	}
	return byRepo, nil
}
