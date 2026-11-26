// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
	"golang.org/x/oauth2"
)

// The user-to-server surface: a GitHub App's OAuth flow, and the calls made
// with the user token it yields. A user token reaches what both the App and
// the user can reach, so every repository listed here is one the App is
// installed on.

// Repository roles, highest first, as GitHub's per-user permissions report
// them. RoleWrite is GitHub's "push".
const (
	RoleAdmin    = "admin"
	RoleMaintain = "maintain"
	RoleWrite    = "write"
	RoleTriage   = "triage"
	RoleRead     = "read"
	RoleNone     = "none"
)

// Bounds on the maintained-repository walk: at most userRepoPageBudget pages
// of 100 repositories are read (installations included), and the result is
// cut at the caller's limit.
const (
	userPageSize       = 100
	userRepoPageBudget = 100
)

// UserToken is a user-to-server token pair. GitHub rotates the refresh token
// on every refresh and accepts each one once.
type UserToken struct {
	AccessToken  string
	RefreshToken string
	// Expiry is when the access token expires; zero when GitHub reports no
	// expiry (the App does not expire user tokens).
	Expiry time.Time
}

// OAuthApp exchanges and refreshes user tokens for one GitHub App.
type OAuthApp struct {
	ClientID     string
	ClientSecret string
	// WebURL is the GitHub web root the OAuth endpoints live under
	// (https://github.com, or the GHES host).
	WebURL string
	// ProxyURL routes the token calls through a forward proxy; "" means the
	// environment applies.
	ProxyURL string
}

// ErrUserToken reports that GitHub rejected the code, verifier or refresh
// token: the user has to authorize again.
var ErrUserToken = errors.New("github rejected the user authorization")

func (o *OAuthApp) config(redirectURI string) *oauth2.Config {
	web := strings.TrimSuffix(o.WebURL, "/")
	return &oauth2.Config{
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		RedirectURL:  redirectURI,
		Endpoint: oauth2.Endpoint{
			AuthURL:   web + "/login/oauth/authorize",
			TokenURL:  web + "/login/oauth/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

func (o *OAuthApp) context(ctx context.Context) (context.Context, error) {
	proxy, err := parseProxy(o.ProxyURL)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Transport: newRetryTransport(proxy), Timeout: 20 * time.Second}
	return context.WithValue(ctx, oauth2.HTTPClient, hc), nil
}

// Exchange trades an authorization code (with its PKCE verifier) for a user
// token.
func (o *OAuthApp) Exchange(ctx context.Context, code, verifier, redirectURI string) (*UserToken, error) {
	hctx, err := o.context(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := o.config(redirectURI).Exchange(hctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, classifyOAuth(err)
	}
	return userToken(tok), nil
}

// Refresh rotates a user token pair.
func (o *OAuthApp) Refresh(ctx context.Context, refreshToken string) (*UserToken, error) {
	hctx, err := o.context(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := o.config("").TokenSource(hctx, &oauth2.Token{RefreshToken: refreshToken}).Token()
	if err != nil {
		return nil, classifyOAuth(err)
	}
	return userToken(tok), nil
}

func userToken(t *oauth2.Token) *UserToken {
	return &UserToken{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, Expiry: t.Expiry}
}

// classifyOAuth separates "the grant is bad" (an OAuth error response: an
// expired code, a used refresh token) from transport failures.
func classifyOAuth(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorCode != "" || re.Response == nil || re.Response.StatusCode < 500 {
			msg := re.ErrorCode
			if re.ErrorDescription != "" {
				msg += ": " + re.ErrorDescription
			}
			return fmt.Errorf("%w (%s)", ErrUserToken, strings.TrimSpace(msg))
		}
	}
	return fmt.Errorf("github token endpoint: %w", err)
}

// IsUnauthorized reports a GitHub 401: the token is expired or revoked.
func IsUnauthorized(err error) bool {
	var ger *github.ErrorResponse
	return errors.As(err, &ger) && ger.Response != nil && ger.Response.StatusCode == http.StatusUnauthorized
}

// AuthenticatedUser returns the token's user.
func (c *Client) AuthenticatedUser(ctx context.Context) (login string, id int64, err error) {
	u, _, err := c.gh.Users.Get(ctx, "")
	if err != nil {
		return "", 0, err
	}
	return u.GetLogin(), u.GetID(), nil
}

// UserRepository is one repository the token's user can reach through the
// App, with their role on it.
type UserRepository struct {
	// Name is "owner/name".
	Name string
	Role string
}

// RoleOf maps GitHub's per-user permission flags onto the highest role.
func RoleOf(p *github.RepositoryPermissions) string {
	switch {
	case p == nil:
		return RoleNone
	case p.GetAdmin():
		return RoleAdmin
	case p.GetMaintain():
		return RoleMaintain
	case p.GetPush():
		return RoleWrite
	case p.GetTriage():
		return RoleTriage
	case p.GetPull():
		return RoleRead
	}
	return RoleNone
}

// MaintainsRole reports whether role is admin or maintain.
func MaintainsRole(role string) bool { return role == RoleAdmin || role == RoleMaintain }

// UserMaintainedRepositories lists the repositories, across every App
// installation the user can see, on which they are admin or maintain.
// truncated is true when the list hit limit or the page budget.
func (c *Client) UserMaintainedRepositories(
	ctx context.Context, limit int,
) (repos []UserRepository, truncated bool, err error) {
	pages := 0
	var installs []*github.Installation
	opts := &github.ListOptions{PerPage: userPageSize}
	for {
		pages++
		page, resp, err := c.gh.Apps.ListUserInstallations(ctx, opts)
		if err != nil {
			return nil, false, err
		}
		installs = append(installs, page...)
		if resp.NextPage == 0 {
			break
		}
		if pages >= userRepoPageBudget {
			return repos, true, nil
		}
		opts.Page = resp.NextPage
	}
	repos = []UserRepository{}
	for _, inst := range installs {
		opts := &github.ListOptions{PerPage: userPageSize}
		for {
			if pages >= userRepoPageBudget {
				return repos, true, nil
			}
			pages++
			page, resp, err := c.gh.Apps.ListUserRepos(ctx, inst.GetID(), opts)
			if err != nil {
				return nil, false, err
			}
			for _, r := range page.Repositories {
				role := RoleOf(r.Permissions)
				if !MaintainsRole(role) {
					continue
				}
				if len(repos) >= limit {
					return repos, true, nil
				}
				repos = append(repos, UserRepository{Name: r.GetFullName(), Role: role})
			}
			if resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
	}
	return repos, false, nil
}

// UserRepositoryRole returns the token's user's role on repo. A repository
// the user cannot see (or the App is not installed on) is RoleNone.
func (c *Client) UserRepositoryRole(ctx context.Context, repo Repo) (string, error) {
	r, _, err := c.gh.Repositories.Get(ctx, repo.Owner, repo.Name)
	if IsNotFound(err) {
		return RoleNone, nil
	}
	if err != nil {
		return "", err
	}
	return RoleOf(r.Permissions), nil
}

// AppClientID returns the App's OAuth client ID as GitHub reports it
// (GET /app), for checking a configured client ID against the App.
func (a *App) AppClientID(ctx context.Context) (string, error) {
	app, _, err := a.gh.Apps.Get(ctx, "")
	if err != nil {
		return "", err
	}
	return app.GetClientID(), nil
}
