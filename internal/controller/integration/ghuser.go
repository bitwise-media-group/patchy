// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/ghuser"
	"github.com/bitwise-media-group/patchy/internal/repoaccess"
)

// maxUserBody caps a GitHub-user request body (a few tokens and names).
const maxUserBody = 32 << 10

// userOAuth is the token half of a GitHub App's user flow;
// ghclient.OAuthApp implements it.
type userOAuth interface {
	Exchange(ctx context.Context, code, verifier, redirectURI string) (*ghclient.UserToken, error)
	Refresh(ctx context.Context, refreshToken string) (*ghclient.UserToken, error)
}

// userAPI is the slice of a user-token GitHub client the endpoints call;
// ghclient.Client implements it.
type userAPI interface {
	AuthenticatedUser(ctx context.Context) (login string, id int64, err error)
	UserMaintainedRepositories(ctx context.Context, limit int) ([]ghclient.UserRepository, bool, error)
	UserRepositoryRole(ctx context.Context, repo ghclient.Repo) (string, error)
}

// GitHubUser serves the internal GitHub-user endpoints (internal/ghuser):
// the token exchange and refresh of a github Integration's user
// authorization, and the user-token calls the status server's maintainer
// grants rest on. Only the status server calls them. User tokens pass
// through request and response bodies; they are never logged or kept.
type GitHubUser struct {
	// Reader gets Integrations (the manager's cache).
	Reader client.Reader
	// Creds reads the Integration's App credential and proxy settings.
	Creds *Creds
	// Access probes the Integration's alert access to a repository.
	Access *RepositoryAccess
	// Auth admits only the status server's ServiceAccount.
	Auth      callerAuth
	Namespace string
	Log       *slog.Logger

	// oauthFor and userFor override the GitHub clients (tests).
	oauthFor func(ctx context.Context, integ *v1alpha1.Integration) (userOAuth, error)
	userFor  func(ctx context.Context, integ *v1alpha1.Integration, token string) (userAPI, error)
}

// Register mounts the four endpoints on the internal listener's mux.
func (g *GitHubUser) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+ghuser.PathExchange, g.serveExchange)
	mux.HandleFunc("POST "+ghuser.PathRefresh, g.serveRefresh)
	mux.HandleFunc("POST "+ghuser.PathRepositories, g.serveRepositories)
	mux.HandleFunc("POST "+ghuser.PathAuthorizeRepository, g.serveAuthorizeRepository)
}

func (g *GitHubUser) log() *slog.Logger {
	if g.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return g.Log
}

// decode authenticates the caller and reads a validated body, answering
// any failure itself; ok is false when it has.
func decodeUser[T interface{ Validate() error }](g *GitHubUser, w http.ResponseWriter, r *http.Request) (T, bool) {
	var req T
	if _, err := g.Auth.Authenticate(r, ghuser.TokenHeader); err != nil {
		ghuser.WriteError(w, &ghuser.Error{Code: ghuser.CodeCaller, Message: err.Error()})
		return req, false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&req); err != nil {
		ghuser.WriteError(w, &ghuser.Error{Code: ghuser.CodeBadRequest, Message: "malformed request body"})
		return req, false
	}
	if err := req.Validate(); err != nil {
		var e *ghuser.Error
		if !errors.As(err, &e) {
			e = &ghuser.Error{Code: ghuser.CodeBadRequest, Message: err.Error()}
		}
		ghuser.WriteError(w, e)
		return req, false
	}
	return req, true
}

// integration resolves an Integration the endpoints may act through: a
// github Integration with userAuthorization set and not suspended.
func (g *GitHubUser) integration(ctx context.Context, name string) (*v1alpha1.Integration, *ghuser.Error) {
	var integ v1alpha1.Integration
	err := g.Reader.Get(ctx, types.NamespacedName{Namespace: g.Namespace, Name: name}, &integ)
	switch {
	case kerrors.IsNotFound(err):
		return nil, &ghuser.Error{Code: ghuser.CodeIntegration, Message: "integration " + name + " not found"}
	case err != nil:
		return nil, &ghuser.Error{Code: ghuser.CodeUpstream, Message: "get integration: " + err.Error()}
	case integ.Spec.Provider != v1alpha1.IntegrationProviderGitHub || integ.Spec.GitHub == nil ||
		integ.Spec.GitHub.UserAuthorization == nil:
		return nil, &ghuser.Error{Code: ghuser.CodeIntegration,
			Message: "integration " + name + " does not enable GitHub user authorization"}
	case integ.Spec.Suspend:
		return nil, &ghuser.Error{Code: ghuser.CodeIntegration, Message: "integration " + name + " is suspended"}
	}
	return &integ, nil
}

func (g *GitHubUser) oauth(ctx context.Context, integ *v1alpha1.Integration) (userOAuth, *ghuser.Error) {
	var (
		o   userOAuth
		err error
	)
	if g.oauthFor != nil {
		o, err = g.oauthFor(ctx, integ)
	} else {
		o, err = g.Creds.UserOAuth(ctx, integ)
	}
	if err != nil {
		return nil, &ghuser.Error{Code: ghuser.CodeIntegration, Message: err.Error()}
	}
	return o, nil
}

func (g *GitHubUser) user(ctx context.Context, integ *v1alpha1.Integration, token string) (userAPI, *ghuser.Error) {
	var (
		u   userAPI
		err error
	)
	if g.userFor != nil {
		u, err = g.userFor(ctx, integ, token)
	} else {
		u, err = g.Creds.UserClient(ctx, integ, token)
	}
	if err != nil {
		return nil, &ghuser.Error{Code: ghuser.CodeIntegration, Message: err.Error()}
	}
	return u, nil
}

// githubError maps a GitHub failure onto the contract: a rejected grant or
// token is CodeUserToken, anything else upstream.
func githubError(err error) *ghuser.Error {
	if errors.Is(err, ghclient.ErrUserToken) || ghclient.IsUnauthorized(err) {
		return &ghuser.Error{Code: ghuser.CodeUserToken, Message: userTokenMessage(err)}
	}
	_, detail := ghclient.ClassifyAccess(err)
	return &ghuser.Error{Code: ghuser.CodeUpstream, Message: detail}
}

// userTokenMessage keeps GitHub's reason without ever echoing a token.
func userTokenMessage(err error) string {
	if errors.Is(err, ghclient.ErrUserToken) {
		return err.Error()
	}
	return "GitHub rejected the user token"
}

func tokens(t *ghclient.UserToken) ghuser.Tokens {
	return ghuser.Tokens{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, ExpiresAt: t.Expiry}
}

func writeUserJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (g *GitHubUser) serveExchange(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeUser[ghuser.ExchangeRequest](g, w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	integ, gerr := g.integration(ctx, req.Integration)
	if gerr != nil {
		ghuser.WriteError(w, gerr)
		return
	}
	o, gerr := g.oauth(ctx, integ)
	if gerr != nil {
		ghuser.WriteError(w, gerr)
		return
	}
	tok, err := o.Exchange(ctx, req.Code, req.Verifier, req.RedirectURI)
	if err != nil {
		g.log().LogAttrs(ctx, slog.LevelInfo, "github user exchange failed",
			slog.String("integration", integ.Name), slog.String("error", userTokenMessage(err)))
		ghuser.WriteError(w, githubError(err))
		return
	}
	u, gerr := g.user(ctx, integ, tok.AccessToken)
	if gerr != nil {
		ghuser.WriteError(w, gerr)
		return
	}
	login, id, err := u.AuthenticatedUser(ctx)
	if err != nil {
		ghuser.WriteError(w, githubError(err))
		return
	}
	g.log().LogAttrs(ctx, slog.LevelInfo, "github user linked",
		slog.String("integration", integ.Name), slog.String("github.login", login))
	writeUserJSON(w, ghuser.ExchangeResponse{Login: login, UserID: id, Tokens: tokens(tok)})
}

func (g *GitHubUser) serveRefresh(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeUser[ghuser.RefreshRequest](g, w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	integ, gerr := g.integration(ctx, req.Integration)
	if gerr != nil {
		ghuser.WriteError(w, gerr)
		return
	}
	o, gerr := g.oauth(ctx, integ)
	if gerr != nil {
		ghuser.WriteError(w, gerr)
		return
	}
	tok, err := o.Refresh(ctx, req.RefreshToken)
	if err != nil {
		ghuser.WriteError(w, githubError(err))
		return
	}
	writeUserJSON(w, tokens(tok))
}

func (g *GitHubUser) serveRepositories(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeUser[ghuser.RepositoriesRequest](g, w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	integ, gerr := g.integration(ctx, req.Integration)
	if gerr != nil {
		ghuser.WriteError(w, gerr)
		return
	}
	u, gerr := g.user(ctx, integ, req.AccessToken)
	if gerr != nil {
		ghuser.WriteError(w, gerr)
		return
	}
	repos, truncated, err := u.UserMaintainedRepositories(ctx, ghuser.MaxRepositories)
	if err != nil {
		ghuser.WriteError(w, githubError(err))
		return
	}
	out := ghuser.RepositoriesResponse{Repositories: make([]ghuser.Repository, 0, len(repos)), Truncated: truncated}
	for _, repo := range repos {
		out.Repositories = append(out.Repositories, ghuser.Repository{Name: repo.Name, Role: repo.Role})
	}
	writeUserJSON(w, out)
}

func (g *GitHubUser) serveAuthorizeRepository(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeUser[ghuser.AuthorizeRepositoryRequest](g, w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	integ, gerr := g.integration(ctx, req.Integration)
	if gerr != nil {
		ghuser.WriteError(w, gerr)
		return
	}
	u, gerr := g.user(ctx, integ, req.AccessToken)
	if gerr != nil {
		ghuser.WriteError(w, gerr)
		return
	}
	owner, name, _ := strings.Cut(req.Repo, "/")
	repo := ghclient.Repo{Owner: owner, Name: name}
	role, err := u.UserRepositoryRole(ctx, repo)
	if err != nil {
		ghuser.WriteError(w, githubError(err))
		return
	}
	out := ghuser.AuthorizeRepositoryResponse{Role: role, Access: repoaccess.IntegrationAccess{Name: integ.Name}}
	// The alert probe costs App requests; a role that grants nothing does
	// not need it.
	if ghclient.MaintainsRole(role) && g.Access != nil && eligibleForAccess(integ) {
		out.Access = g.Access.IntegrationAccess(ctx, integ, repo)
	}
	writeUserJSON(w, out)
}
