// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/ghas"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
)

// resetClient is the subset of ghclient.Client the demo reset needs; tests
// substitute a fake.
type resetClient interface {
	DeleteIssue(ctx context.Context, repo ghclient.Repo, number int) error
	Close(ctx context.Context, repo ghclient.Repo, number int) error
	OpenAlert(ctx context.Context, repo ghclient.Repo, number int) error
	ReopenSecretAlert(ctx context.Context, repo ghclient.Repo, number int) error
}

// runReset performs the demo reset: permanently delete every finding's
// tracking issue, reopen the dismissed findings' own code- and
// secret-scanning alerts (patchy closed them on ignore verdicts). Only findings that
// still exist are covered; a demo reset happens within the finding TTL, so
// nothing older lingers. Then it deletes every pipeline resource. GitHub
// cleanup runs first and any failure aborts before the deletes: the
// Findings carry the issue numbers, repositories, and alert numbers the
// cleanup needs, so that state must outlive a retried attempt. Everything
// here is idempotent, and forge objects that are already gone are skipped
// rather than failed.
func (r *IntegrationReconciler) runReset(ctx context.Context, namespace string) error {
	var findings v1alpha1.FindingList
	if err := r.List(ctx, &findings, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("list findings: %w", err)
	}

	var errs []error
	tracker, err := r.resetIntegration(ctx, namespace, issuesEnabled)
	if err != nil {
		errs = append(errs, err)
	}
	scanners := map[string]*v1alpha1.Integration{}
	for sourceID, has := range map[string]capability{
		ghas.ID:        codeScanningEnabled,
		ghas.SecretsID: secretScanningEnabled,
	} {
		integ, err := r.resetIntegration(ctx, namespace, has)
		if err != nil {
			errs = append(errs, err)
		}
		if integ != nil {
			scanners[sourceID] = integ
		}
	}
	for i := range findings.Items {
		errs = append(errs, r.resetFinding(ctx, tracker, scanners, &findings.Items[i])...)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	for _, obj := range []client.Object{
		&v1alpha1.Finding{},
		&v1alpha1.Investigation{},
		&v1alpha1.Remediation{},
		&v1alpha1.Repository{},
		&v1alpha1.FindingRollup{},
	} {
		if err := r.DeleteAllOf(ctx, obj, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("delete all %T: %w", obj, err)
		}
	}
	return nil
}

// resetFinding runs the GitHub cleanup for one finding: delete its tracking
// issue (when a tracker Integration exists) and, for dismissed findings,
// reopen its GitHub alerts, each through the scanner Integration of the
// source that raised it (scanners, keyed by source id).
// Only admin-permission user tokens may delete issues. With any other
// credential the delete falls back to closing the issue, which keeps a
// replayed demo from duplicating open trackers at the cost of leaving
// closed ones behind.
func (r *IntegrationReconciler) resetFinding(
	ctx context.Context, tracker *v1alpha1.Integration, scanners map[string]*v1alpha1.Integration,
	fnd *v1alpha1.Finding,
) []error {
	if fnd.Spec.Repository == nil {
		return nil
	}
	repo, err := parseOwnerRepo(fnd.Spec.Repository.Name)
	if err != nil {
		return nil
	}
	var errs []error
	if tr := fnd.Status.Tracking; tracker != nil && tr != nil && tr.IssueNumber != 0 {
		if err := r.resetIssue(ctx, tracker, repo, int(tr.IssueNumber)); err != nil && !r.forgeGone(ctx, repo, err) {
			errs = append(errs, err)
		}
	}
	if fnd.Status.Phase != v1alpha1.PhaseDismissed {
		return errs
	}
	// Grouped by source: a code-scanning and a secret-scanning alert can
	// share a number, and reopening one through the other's API would
	// touch the wrong alert. Alerts of any other source have nothing on
	// GitHub to reopen.
	for sourceID, alerts := range fnd.AlertsBySource() {
		if scanner := scanners[sourceID]; scanner != nil {
			errs = append(errs, r.resetAlerts(ctx, scanner, sourceID, repo, alerts)...)
		}
	}
	return errs
}

// resetIssue removes one finding's tracking issue from the forge.
func (r *IntegrationReconciler) resetIssue(
	ctx context.Context, tracker *v1alpha1.Integration, repo ghclient.Repo, number int,
) error {
	c, err := r.resetClientFor(ctx, tracker, repo)
	if err != nil {
		return err
	}
	return r.deleteOrCloseIssue(ctx, c, repo, number)
}

// resetAlerts reopens one source's alerts that a dismissed finding closed:
// code-scanning dismissals or secret-scanning resolutions. An id that is not
// a GitHub alert number is corrupt state with nothing to reopen.
func (r *IntegrationReconciler) resetAlerts(
	ctx context.Context, scanner *v1alpha1.Integration, sourceID string, repo ghclient.Repo,
	alerts []v1alpha1.Alert,
) []error {
	c, err := r.resetClientFor(ctx, scanner, repo)
	if err != nil {
		if r.forgeGone(ctx, repo, err) {
			return nil
		}
		return []error{err}
	}
	var errs []error
	for _, a := range alerts {
		num, err := strconv.Atoi(a.ID)
		if err != nil {
			continue
		}
		reopen := c.OpenAlert
		if sourceID == ghas.SecretsID {
			reopen = c.ReopenSecretAlert
		}
		if err := reopen(ctx, repo, num); err != nil && !r.forgeGone(ctx, repo, err) {
			errs = append(errs, err)
		}
	}
	return errs
}

// forgeGone reports whether err is GitHub answering "no such thing": a 404
// for the repository, its installation, an issue, or an alert. The reset
// converges on "no forge state left", so anything already absent is success:
// the benchmark repositories a demo runs against get deleted and recreated
// between runs, and failing on their remains would strand the pipeline
// resources the reset exists to delete.
func (r *IntegrationReconciler) forgeGone(ctx context.Context, repo ghclient.Repo, err error) bool {
	if !ghclient.IsNotFound(err) {
		return false
	}
	r.log().LogAttrs(ctx, slog.LevelInfo, "reset skipping forge object that no longer exists",
		slog.String("repo", repo.String()), slog.String("error", err.Error()))
	return true
}

// deleteOrCloseIssue deletes a tracking issue, closing it instead when
// the credential cannot delete. Both are idempotent: a missing issue
// deletes as success and a closed issue closes as a no-op.
func (r *IntegrationReconciler) deleteOrCloseIssue(
	ctx context.Context, c resetClient, repo ghclient.Repo, number int,
) error {
	err := c.DeleteIssue(ctx, repo, number)
	if !errors.Is(err, ghclient.ErrDeleteUnauthorized) {
		return err
	}
	r.log().LogAttrs(ctx, slog.LevelWarn, "issue delete unauthorized; closing instead",
		slog.String("repo", repo.String()), slog.Int("issue", number))
	return c.Close(ctx, repo, number)
}

// resetIntegration resolves the Integration providing a capability; a
// namespace without one yields nil (that side of the cleanup is skipped),
// any other failure an error.
func (r *IntegrationReconciler) resetIntegration(
	ctx context.Context, namespace string, has capability,
) (*v1alpha1.Integration, error) {
	integ, err := selectIntegration(ctx, r.Client, namespace, has)
	if errors.Is(err, ErrNoIntegration) {
		return nil, nil
	}
	return integ, err
}

// resetClientFor returns the forge client for the reset: ClientFor when
// set (tests), else Creds.Client.
func (r *IntegrationReconciler) resetClientFor(
	ctx context.Context, integ *v1alpha1.Integration, repo ghclient.Repo,
) (resetClient, error) {
	if r.ClientFor != nil {
		return r.ClientFor(ctx, integ, repo)
	}
	return r.Creds.Client(ctx, integ, repo)
}

// consumeReset handles a pending spec.reset: GitHub cleanup + pipeline-CR
// deletion, then the receiver dedup drop so redeliveries land as new. The
// status echo is written by the caller only on success; a failed attempt
// retries on the next reconcile with the Findings still intact.
func (r *IntegrationReconciler) consumeReset(
	ctx context.Context, integ *v1alpha1.Integration, req *v1alpha1.ActionRequest,
) error {
	if err := r.runReset(ctx, integ.Namespace); err != nil {
		return err
	}
	r.dropDedup()
	integ.Status.ResetAt = &req.At
	r.log().LogAttrs(ctx, slog.LevelInfo, "demo reset applied",
		slog.String("integration", integ.Name), slog.String("by", req.By))
	return nil
}
