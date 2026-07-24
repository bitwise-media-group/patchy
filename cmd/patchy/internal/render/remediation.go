// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package render

import (
	"fmt"
	"time"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
)

// RemediationDetail renders everything known about one remediation attempt.
func RemediationDetail(d *printer.Doc, rem *v1alpha1.Remediation, now time.Time) {
	d.Section(fmt.Sprintf("Remediation %s", rem.Name)).
		Field("Finding", rem.Spec.FindingRef.Name).
		Field("Investigation", rem.Spec.InvestigationRef.Name).
		Fieldf("Attempt", "%d", rem.Spec.Attempt).
		Fieldf("Queue priority", "%d", rem.Spec.Priority).
		Field("Phase", string(rem.Status.Phase)).
		Fieldf("Success", "%t", rem.Status.Success).
		Field("Confidence", rem.Status.Confidence).
		Field("Approved by", rem.Spec.ApprovedBy).
		Field("Granted", Timestamp(rem.Status.GrantedAt, now)).
		Field("Age", Age(rem.CreationTimestamp.Time, now))
	if rem.Spec.Revival {
		d.Field("Revival", "yes — remediate-only run reviving a handed-off finding")
	}

	d.Section("Changeset").
		Field("Branch", rem.Status.Branch).
		Field("Commit", rem.Status.PushedCommit)
	if pr := rem.Status.PullRequest; pr != nil {
		d.Fieldf("Pull request", "#%d %s", pr.Number, pr.URL)
	}

	Stage(d, rem.Status.Stage, now)
}

// RemediationReview renders the result and the report.
func RemediationReview(d *printer.Doc, rem *v1alpha1.Remediation, raw bool) {
	d.Section(fmt.Sprintf("Remediation %s (attempt %d)", rem.Name, rem.Spec.Attempt)).
		Fieldf("Success", "%t", rem.Status.Success).
		Field("Confidence", rem.Status.Confidence).
		Field("Branch", rem.Status.Branch).
		Field("Commit", rem.Status.PushedCommit).
		Field("Outcome", Outcome(rem.Status.Stage)).
		Field("Cost", Cost(rem.Status.Stage))
	if pr := rem.Status.PullRequest; pr != nil {
		d.Fieldf("Pull request", "#%d %s", pr.Number, pr.URL)
	}
	d.Body(Body(rem.Status.Report, raw))
}
