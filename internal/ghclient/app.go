// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v89/github"
)

// AppConfig configures GitHub App authentication.
type AppConfig struct {
	AppID      int64
	PrivateKey []byte // PEM-encoded RSA private key
	BaseURL    string // default https://api.github.com/ ; tests and GHES override
}

// App mints installation-scoped clients and tokens for a GitHub App.
type App struct {
	atr     *ghinstallation.AppsTransport
	gh      *github.Client // authenticated as the app (JWT)
	baseURL string         // cfg.BaseURL, propagated to installation clients

	mu            sync.Mutex
	installations map[Repo]int64    // repo → installation ID
	clients       map[int64]*Client // installation ID → cached client
}

// NewApp builds an App from cfg, parsing and validating the private key.
func NewApp(cfg AppConfig) (*App, error) {
	if cfg.AppID == 0 {
		return nil, errors.New("ghclient: AppConfig.AppID is required")
	}
	atr, err := ghinstallation.NewAppsTransport(newRetryTransport(), cfg.AppID, cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("ghclient: app transport: %w", err)
	}
	gh, err := newGitHub(atr, cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("ghclient: app client: %w", err)
	}
	// ghinstallation wants the resolved API root without a trailing slash;
	// deriving it from the go-github client keeps the two in lockstep.
	atr.BaseURL = apiRoot(gh)
	return &App{
		atr:           atr,
		gh:            gh,
		baseURL:       cfg.BaseURL,
		installations: make(map[Repo]int64),
		clients:       make(map[int64]*Client),
	}, nil
}

// Installation returns a Client authenticated as the installation covering
// repo. Both the repo → installation lookup and the per-installation client
// are cached, so repeated calls cost no API requests.
func (a *App) Installation(ctx context.Context, repo Repo) (*Client, error) {
	id, err := a.installationID(ctx, repo)
	if err != nil {
		return nil, err
	}

	return a.clientFor(id)
}

// clientFor returns (building and caching if needed) the client for an
// installation ID.
func (a *App) clientFor(id int64) (*Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.clients[id]; ok {
		return c, nil
	}
	tr := ghinstallation.NewFromAppsTransport(a.atr, id)
	gh, err := newGitHub(tr, a.baseURL)
	if err != nil {
		return nil, fmt.Errorf("ghclient: installation client %d: %w", id, err)
	}
	c := &Client{gh: gh}
	a.clients[id] = c
	return c, nil
}

// Installations returns one client per installation of the App — the fan-out
// for cross-repository sweeps like the reconcile searches.
func (a *App) Installations(ctx context.Context) ([]*Client, error) {
	opts := &github.ListOptions{PerPage: 100}
	var out []*Client
	for {
		page, resp, err := a.gh.Apps.ListInstallations(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("ghclient: list installations: %w", err)
		}
		for _, inst := range page {
			c, err := a.clientFor(inst.GetID())
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// TokenPerms is the permission set for a scoped token; string values are
// "read"/"write" per the GitHub API; empty means not requested.
type TokenPerms struct {
	Contents string
}

// ScopedToken mints a short-lived installation token restricted to the
// single repository and permissions given, returning the token and its
// expiry.
func (a *App) ScopedToken(ctx context.Context, repo Repo, perms TokenPerms) (string, time.Time, error) {
	id, err := a.installationID(ctx, repo)
	if err != nil {
		return "", time.Time{}, err
	}
	opts := &github.InstallationTokenOptions{Repositories: []string{repo.Name}}
	if perms.Contents != "" {
		opts.Permissions = &github.InstallationPermissions{Contents: github.Ptr(perms.Contents)}
	}
	tok, _, err := a.gh.Apps.CreateInstallationToken(ctx, id, opts)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("ghclient: scoped token for %s: %w", repo, err)
	}
	return tok.GetToken(), tok.GetExpiresAt().Time, nil
}

// installationID resolves (and caches) the installation covering repo via
// GET /repos/{owner}/{repo}/installation.
func (a *App) installationID(ctx context.Context, repo Repo) (int64, error) {
	a.mu.Lock()
	id, ok := a.installations[repo]
	a.mu.Unlock()
	if ok {
		return id, nil
	}
	inst, _, err := a.gh.Apps.GetRepositoryInstallation(ctx, repo.Owner, repo.Name)
	if err != nil {
		return 0, fmt.Errorf("ghclient: resolve installation for %s: %w", repo, err)
	}
	id = inst.GetID()
	a.mu.Lock()
	a.installations[repo] = id
	a.mu.Unlock()
	return id, nil
}
