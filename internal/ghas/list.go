// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghas

import (
	"context"

	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// AlertEnumerator walks the open code-scanning alerts the credential can
// reach: an installation fan-out for an App, an explicit repository list
// for a PAT (ghclient.AppAlertEnumerator / PATAlertEnumerator). The repos
// it receives are only a scope hint for skipping whole installations. The
// Handler applies the authoritative per-repository filter.
type AlertEnumerator interface {
	Enumerate(
		ctx context.Context, repos []string, yield func(ghclient.Repo, *ghclient.Alert) bool,
	) (complete bool, err error)
}

// NewLister builds a handler for the backfill path, enumerating open
// alerts through e.
func NewLister(e AlertEnumerator) *Handler { return &Handler{enum: e} }

// Handler is also the source.Lister.
var _ source.Lister = (*Handler)(nil)

// List implements source.Lister: it walks the enumerator's open alerts,
// keeps the ones matching the repository filter, and yields each as a
// finding. complete is false when the walk ended early: the caller's
// yield stopped it or the enumerator's page budget ran out.
func (h *Handler) List(
	ctx context.Context, repos []string, yield func(source.Finding) bool,
) (complete bool, err error) {
	return h.enum.Enumerate(ctx, repos, func(repo ghclient.Repo, alert *ghclient.Alert) bool {
		// The authoritative filter. Enumerators may pre-prune, but
		// whatever they yield is checked again here.
		if !ghclient.RepoMatches(repos, repo) {
			return true
		}
		return yield(FindingFromAlert(repo, alert))
	})
}
