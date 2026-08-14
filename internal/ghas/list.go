// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package ghas

import (
	"context"

	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// AlertEnumerator walks the credential's reachable open code-scanning
// alerts — an installation fan-out for an App, an explicit repository
// list for a PAT (ghclient.AppAlertEnumerator / PATAlertEnumerator). The
// repos entries it receives are only a scope hint (skipping whole
// installations); the authoritative per-repository filter is the
// Handler's.
type AlertEnumerator interface {
	Enumerate(
		ctx context.Context, repos []string, yield func(ghclient.Repo, *ghclient.Alert) bool,
	) (complete bool, err error)
}

// NewLister builds a handler for the backfill path, enumerating open
// alerts through e.
func NewLister(e AlertEnumerator) *Handler { return &Handler{enum: e} }

// The lister half of the seam.
var _ source.Lister = (*Handler)(nil)

// List implements source.Lister: it walks the enumerator's open alerts,
// keeps the ones matching the repository filter, and yields each as a
// finding. complete is false when the walk ended early — the caller's
// yield stopped it or the enumerator's page budget ran out.
func (h *Handler) List(
	ctx context.Context, repos []string, yield func(source.Finding) bool,
) (complete bool, err error) {
	return h.enum.Enumerate(ctx, repos, func(repo ghclient.Repo, alert *ghclient.Alert) bool {
		// The authoritative filter: enumerators may pre-prune (skipping
		// installations, or enumerating only matching repositories) but
		// whatever they yield is checked again here.
		if !ghclient.RepoMatches(repos, repo) {
			return true
		}
		return yield(FindingFromAlert(repo, alert))
	})
}
