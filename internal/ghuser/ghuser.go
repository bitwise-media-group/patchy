// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghuser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bitwise-media-group/patchy/internal/repoaccess"
)

// The endpoints' fixed contract. They share the repository-access
// endpoint's listener, token header and audience.
const (
	PathExchange            = "/internal/v1/github-user/exchange"
	PathRefresh             = "/internal/v1/github-user/refresh"
	PathRepositories        = "/internal/v1/github-user/repositories"
	PathAuthorizeRepository = "/internal/v1/github-user/authorize-repository"
	// TokenHeader carries the caller's projected ServiceAccount token.
	TokenHeader = repoaccess.TokenHeader
	// MaxRepositories caps the maintained-repository list.
	MaxRepositories = 1000
	// maxField bounds every string field of a request.
	maxField = 4096
)

// Repository roles, highest first, as GitHub reports a user's permission.
const (
	RoleAdmin    = "admin"
	RoleMaintain = "maintain"
	RoleWrite    = "write"
	RoleTriage   = "triage"
	RoleRead     = "read"
	RoleNone     = "none"
)

// Maintains reports whether role is admin or maintain: the roles the
// maintainer grants act on.
func Maintains(role string) bool { return role == RoleAdmin || role == RoleMaintain }

// ExchangeRequest trades an authorization code for a user token.
type ExchangeRequest struct {
	Integration string `json:"integration"`
	Code        string `json:"code"`
	Verifier    string `json:"verifier"`
	RedirectURI string `json:"redirectURI"`
}

// Tokens is a user-to-server token pair. ExpiresAt is zero when the App does
// not expire user tokens.
type Tokens struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt,omitzero"`
}

// ExchangeResponse is the linked identity and its tokens.
type ExchangeResponse struct {
	Login  string `json:"login"`
	UserID int64  `json:"userID"`
	Tokens
}

// RefreshRequest rotates a token pair. GitHub accepts each refresh token
// once.
type RefreshRequest struct {
	Integration  string `json:"integration"`
	RefreshToken string `json:"refreshToken"`
}

// RepositoriesRequest lists the repositories the user maintains.
type RepositoriesRequest struct {
	Integration string `json:"integration"`
	AccessToken string `json:"accessToken"`
}

// Repository is one repository the user maintains.
type Repository struct {
	// Name is "owner/name".
	Name string `json:"name"`
	Role string `json:"role"`
}

// RepositoriesResponse is the user's maintained repositories (admin or
// maintain), across the Integration App's installations the user can see.
type RepositoriesResponse struct {
	Repositories []Repository `json:"repos"`
	// Truncated marks a list cut at MaxRepositories or the page budget.
	Truncated bool `json:"truncated,omitempty"`
}

// AuthorizeRepositoryRequest asks for the user's role on one repository.
type AuthorizeRepositoryRequest struct {
	Integration string `json:"integration"`
	AccessToken string `json:"accessToken"`
	Repo        string `json:"repo"`
}

// AuthorizeRepositoryResponse is the user's role on the repository and the
// Integration's alert access to it: together they decide a maintained
// backfill in one round trip.
type AuthorizeRepositoryResponse struct {
	Role   string                       `json:"role"`
	Access repoaccess.IntegrationAccess `json:"access"`
}

// Error codes the endpoints answer with.
const (
	// CodeCaller: the caller's ServiceAccount token was rejected.
	CodeCaller = "caller"
	// CodeBadRequest: the request body is malformed or out of bounds.
	CodeBadRequest = "bad_request"
	// CodeIntegration: the Integration is missing, suspended, not github, or
	// does not enable user authorization (or its credential cannot).
	CodeIntegration = "integration"
	// CodeUserToken: GitHub rejected the user's code, access token or
	// refresh token. An access token can be refreshed; a refresh token or
	// code means authorizing again.
	CodeUserToken = "user_token"
	// CodeUpstream: GitHub failed or could not be reached.
	CodeUpstream = "upstream"
)

// StatusFor maps an error code onto its HTTP status.
func StatusFor(code string) int {
	switch code {
	case CodeCaller:
		return http.StatusUnauthorized
	case CodeBadRequest:
		return http.StatusBadRequest
	case CodeIntegration:
		return http.StatusConflict
	case CodeUserToken:
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}

// Error is an endpoint's failure, as the body carries it.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return "github user: " + e.Code + ": " + e.Message }

// IsCode reports whether err is an endpoint Error with the code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// ErrUnavailable reports that the endpoints are not configured.
var ErrUnavailable = errors.New("github user endpoints unavailable")

// Validate checks the fields every request shares.
func validate(integration string, fields ...string) error {
	if integration == "" || len(integration) > 253 {
		return &Error{Code: CodeBadRequest, Message: "integration is required"}
	}
	for _, f := range fields {
		if f == "" || len(f) > maxField {
			return &Error{Code: CodeBadRequest, Message: "a required field is empty or too long"}
		}
	}
	return nil
}

// Validate checks an exchange request.
func (r ExchangeRequest) Validate() error {
	return validate(r.Integration, r.Code, r.Verifier, r.RedirectURI)
}

// Validate checks a refresh request.
func (r RefreshRequest) Validate() error { return validate(r.Integration, r.RefreshToken) }

// Validate checks a repositories request.
func (r RepositoriesRequest) Validate() error { return validate(r.Integration, r.AccessToken) }

// Validate checks an authorize-repository request.
func (r AuthorizeRepositoryRequest) Validate() error {
	if err := validate(r.Integration, r.AccessToken); err != nil {
		return err
	}
	if !repoaccess.ValidRepository(r.Repo) {
		return &Error{Code: CodeBadRequest, Message: "repo must be an exact owner/name"}
	}
	return nil
}

// WebURL is the GitHub web root for an Integration's base URL: the OAuth
// endpoints live there, not under the API. "" is github.com; a GHES base
// URL keeps its scheme and host and drops the /api/v3 path.
func WebURL(baseURL string) string {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return "https://github.com"
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return strings.TrimSuffix(base, "/")
	}
	if strings.EqualFold(u.Hostname(), "api.github.com") {
		return "https://github.com"
	}
	return u.Scheme + "://" + u.Host
}

// Host is the lower-case host an Integration's repositories live on.
func Host(baseURL string) string {
	u, err := url.Parse(WebURL(baseURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// AuthorizeURL builds the GitHub authorize redirect for the user-to-server
// flow. A GitHub App takes no scopes: the App's permissions bound the token.
func AuthorizeURL(baseURL, clientID, redirectURI, state, challenge string) string {
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return WebURL(baseURL) + "/login/oauth/authorize?" + q.Encode()
}

// Client calls the endpoints. The zero URL disables it.
type Client struct {
	// URL is the integration-controller's internal base URL.
	URL string
	// TokenFile is the projected ServiceAccount token, read again on every
	// call because the kubelet rotates it.
	TokenFile string
	// HTTP defaults to a client with a 30-second timeout.
	HTTP *http.Client
}

var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

// Enabled reports whether the client has an endpoint to call.
func (c *Client) Enabled() bool { return c != nil && c.URL != "" }

// Exchange trades an authorization code for the linked identity.
func (c *Client) Exchange(ctx context.Context, req ExchangeRequest) (*ExchangeResponse, error) {
	var out ExchangeResponse
	return &out, c.do(ctx, PathExchange, req, &out)
}

// Refresh rotates a token pair.
func (c *Client) Refresh(ctx context.Context, req RefreshRequest) (*Tokens, error) {
	var out Tokens
	return &out, c.do(ctx, PathRefresh, req, &out)
}

// Repositories lists the user's maintained repositories.
func (c *Client) Repositories(ctx context.Context, req RepositoriesRequest) (*RepositoriesResponse, error) {
	var out RepositoriesResponse
	return &out, c.do(ctx, PathRepositories, req, &out)
}

// AuthorizeRepository returns the user's role on one repository and the
// Integration's alert access to it.
func (c *Client) AuthorizeRepository(
	ctx context.Context, req AuthorizeRepositoryRequest,
) (*AuthorizeRepositoryResponse, error) {
	var out AuthorizeRepositoryResponse
	return &out, c.do(ctx, PathAuthorizeRepository, req, &out)
}

func (c *Client) do(ctx context.Context, path string, in, out any) error {
	if !c.Enabled() {
		return ErrUnavailable
	}
	if v, ok := in.(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			return err
		}
	}
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return fmt.Errorf("read caller token: %w", err)
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.URL, "/")+path,
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set(TokenHeader, strings.TrimSpace(string(token)))
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		return fmt.Errorf("github user: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("github user: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e Error
		if json.Unmarshal(raw, &e) == nil && e.Code != "" {
			return &e
		}
		return &Error{Code: CodeUpstream, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode,
			strings.TrimSpace(string(raw[:min(len(raw), 512)])))}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("github user: decode: %w", err)
	}
	return nil
}

// WriteError answers a request with an endpoint Error.
func WriteError(w http.ResponseWriter, e *Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(StatusFor(e.Code))
	_ = json.NewEncoder(w).Encode(e)
}
