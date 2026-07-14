// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package remedctrl

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/jobs"
)

// Stores is the per-repository GitHub surface the controller mutates.
type Stores interface {
	ghclient.IssueStore
	ghclient.AlertStore
	ghclient.RepoStore
}

// Searcher runs cross-repository issue searches within one installation.
type Searcher interface {
	SearchIssues(ctx context.Context, query string) ([]*ghclient.Issue, error)
}

// Clients resolves GitHub access and mints the short-lived, single-repo
// tokens the Job's init container clones with.
type Clients interface {
	For(ctx context.Context, repo ghclient.Repo) (Stores, error)
	All(ctx context.Context) ([]Searcher, error)
	// CloneToken mints a contents:read token for repo.
	CloneToken(ctx context.Context, repo ghclient.Repo) (string, error)
	// PushToken mints a contents:write token for repo.
	PushToken(ctx context.Context, repo ghclient.Repo) (string, error)
}

// Runner creates and observes the agent Jobs.
type Runner interface {
	Create(ctx context.Context, spec jobs.Spec) (string, error)
	Follow(ctx context.Context, job string, fn func(envelope.Event) error) error
	Result(ctx context.Context, job string) ([]envelope.Event, error)
	Status(ctx context.Context, job string) (jobs.Status, error)
	List(ctx context.Context) ([]jobs.Owned, error)
	Delete(ctx context.Context, job string) error
}

// Pusher applies an agent's changeset to GitHub through the API with a
// short-lived write token. Kept behind an interface so the controller stays
// testable without the network.
type Pusher interface {
	Push(ctx context.Context, repo ghclient.Repo, token, branch string, cs *envelope.Changeset) error
}

// Config tunes the controller.
type Config struct {
	// MinAge is how old an issue must be before remediation picks it up —
	// DESIGN's "older than one hour" gate, belt-and-braces with the
	// accumulation label. Default one hour.
	MinAge time.Duration
	// MaxAttempts bounds Job retries per issue before it is handed to a
	// human. Default 2.
	MaxAttempts int
	// ApproveCommand is the issue comment forcing a remediation attempt.
	ApproveCommand string
}

// Controller is the remediation-controller engine; it implements
// webhook.Handler.
type Controller struct {
	log     *slog.Logger
	clients Clients
	runner  Runner
	pusher  Pusher
	cfg     Config
	now     func() time.Time
}

// New builds a Controller.
func New(log *slog.Logger, clients Clients, runner Runner, pusher Pusher, cfg Config) *Controller {
	if cfg.MinAge <= 0 {
		cfg.MinAge = time.Hour
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 2
	}
	if cfg.ApproveCommand == "" {
		cfg.ApproveCommand = "/approve"
	}
	return &Controller{
		log:     log,
		clients: clients,
		runner:  runner,
		pusher:  pusher,
		cfg:     cfg,
		now:     time.Now,
	}
}

// issueRef names one issue.
type issueRef struct {
	repo   ghclient.Repo
	number int
}

func (r issueRef) String() string { return fmt.Sprintf("%s#%d", r.repo, r.number) }
