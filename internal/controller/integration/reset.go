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
	"github.com/bitwise-media-group/patchy/internal/ghclient"
)

// resetClient is the forge surface the demo reset needs — a slice of
// ghclient.Client; tests substitute a fake.
type resetClient interface {
	DeleteIssue(ctx context.Context, repo ghclient.Repo, number int) error
	OpenAlert(ctx context.Context, repo ghclient.Repo, number int) error
}

// runReset performs the demo reset: permanently delete every finding's
// tracking issue, reopen the dismissed findings' own code-scanning alerts
// (patchy dismissed them on false-positive verdicts; only the findings we
// know about — a demo reset happens within the finding TTL, so nothing
// older lingers), then delete every pipeline resource. GitHub cleanup runs
// first and any failure aborts before the deletes — the Findings carry the
// issue numbers, repositories, and alert numbers the cleanup needs, so
// state must outlive a retried attempt. Everything here is idempotent.
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
	scanner, err := r.resetIntegration(ctx, namespace, codeScanningEnabled)
	if err != nil {
		errs = append(errs, err)
	}
	for i := range findings.Items {
		errs = append(errs, r.resetFinding(ctx, tracker, scanner, &findings.Items[i])...)
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
// reopen its code-scanning alerts (when a scanner Integration exists).
func (r *IntegrationReconciler) resetFinding(
	ctx context.Context, tracker, scanner *v1alpha1.Integration, fnd *v1alpha1.Finding,
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
		c, err := r.resetClientFor(ctx, tracker, repo)
		if err != nil {
			errs = append(errs, err)
		} else if err := c.DeleteIssue(ctx, repo, int(tr.IssueNumber)); err != nil {
			errs = append(errs, err)
		}
	}
	if scanner == nil || fnd.Status.Phase != v1alpha1.PhaseDismissed {
		return errs
	}
	c, err := r.resetClientFor(ctx, scanner, repo)
	if err != nil {
		return append(errs, err)
	}
	for _, a := range fnd.Spec.Alerts {
		num, err := strconv.Atoi(a.ID)
		if err != nil {
			continue // foreign-source alert id; nothing to reopen on GitHub
		}
		if err := c.OpenAlert(ctx, repo, num); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
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

// resetClientFor resolves the forge-client seam.
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
