// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/webhook"
)

// TrackingURLIndex is the field-indexer key mapping a tracking item's html
// URL to its Finding.
const TrackingURLIndex = "status.tracking.url"

// BranchPrefix prefixes every remediation branch; the finding name follows,
// so pull-request webhooks resolve their Finding from the head ref alone.
const BranchPrefix = "patchy/"

// approverAssociations are the author associations allowed to /approve.
var approverAssociations = []string{"OWNER", "MEMBER", "COLLABORATOR"}

// Signals applies human actions on tracking items to Findings: the writer of
// edges 16/17 (PR merged/closed), 19 (issue reopened after dismissal), 20
// (issue closed by a human), and of spec.approval.
type Signals struct {
	client.Client
	// Namespace the Findings live in.
	Namespace string
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
	// Log receives diagnostics; nil discards.
	Log *slog.Logger
}

// Handle applies one tracking-system delivery.
func (s *Signals) Handle(ctx context.Context, integ *v1alpha1.Integration, e webhook.Event) error {
	switch e.Type {
	case "issues":
		return s.issues(ctx, e.Payload)
	case "issue_comment":
		return s.comment(ctx, integ, e.Payload)
	case "pull_request":
		return s.pullRequest(ctx, e.Payload)
	default:
		return nil
	}
}

type issueRef struct {
	Number  int64  `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
}

// issues handles close (any non-terminal phase → HandedOff) and reopen
// (Dismissed → HandedOff).
func (s *Signals) issues(ctx context.Context, payload []byte) error {
	var ev struct {
		Action string   `json:"action"`
		Issue  issueRef `json:"issue"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Errorf("decode issues event: %w", err)
	}
	if ev.Action != "closed" && ev.Action != "reopened" {
		return nil
	}
	fnd, err := s.findByIssueURL(ctx, ev.Issue.HTMLURL)
	if err != nil || fnd == "" {
		return err
	}
	return s.updateFinding(ctx, fnd, func(cur *v1alpha1.Finding) error {
		if cur.Status.Tracking != nil {
			cur.Status.Tracking.State = map[string]string{"closed": "closed", "reopened": "open"}[ev.Action]
		}
		switch {
		case ev.Action == "closed" && !v1alpha1.Terminal(cur.Status.Phase):
			return v1alpha1.SetPhase(cur, v1alpha1.PhaseHandedOff, s.now())
		case ev.Action == "reopened" && cur.Status.Phase == v1alpha1.PhaseDismissed:
			return v1alpha1.SetPhase(cur, v1alpha1.PhaseHandedOff, s.now())
		default:
			return nil
		}
	})
}

// comment handles the approve command: an authorized commenter sets
// spec.approval; remediation-controller reacts to the spec change.
func (s *Signals) comment(ctx context.Context, integ *v1alpha1.Integration, payload []byte) error {
	var ev struct {
		Action  string   `json:"action"`
		Issue   issueRef `json:"issue"`
		Comment struct {
			Body              string `json:"body"`
			AuthorAssociation string `json:"author_association"`
			User              struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"comment"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Errorf("decode issue_comment event: %w", err)
	}
	command := "/approve"
	if integ.Spec.GitHub != nil && integ.Spec.GitHub.Issues != nil && integ.Spec.GitHub.Issues.ApproveComment != "" {
		command = integ.Spec.GitHub.Issues.ApproveComment
	}
	body := strings.TrimSpace(ev.Comment.Body)
	if ev.Action != "created" || (body != command && !strings.HasPrefix(body, command+" ")) {
		return nil
	}
	if !slices.Contains(approverAssociations, ev.Comment.AuthorAssociation) {
		s.log().LogAttrs(ctx, slog.LevelInfo, "approve from unauthorized association",
			slog.String("association", ev.Comment.AuthorAssociation),
			slog.String("login", ev.Comment.User.Login))
		return nil
	}
	fnd, err := s.findByIssueURL(ctx, ev.Issue.HTMLURL)
	if err != nil || fnd == "" {
		return err
	}
	note := strings.TrimSpace(strings.TrimPrefix(body, command))
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := s.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: fnd}, &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		// First approval wins — except a HandedOff finding whose recorded
		// approval predates completion: that approval can never revive it
		// (remediation-controller requires approval newer than completedAt),
		// so a fresh /approve replaces it.
		if cur.Spec.Approval != nil && !staleApproval(&cur) {
			return nil // first approval wins
		}
		cur.Spec.Approval = &v1alpha1.Approval{
			By:   ev.Comment.User.Login,
			At:   metav1.NewTime(s.now()),
			Note: truncate(note, 1024),
		}
		return s.Update(ctx, &cur)
	})
}

// staleApproval reports a HandedOff finding whose approval is too old to
// revive it (not newer than status.completedAt). Keep in lockstep with the
// status server's copy in internal/web/actions.go.
func staleApproval(f *v1alpha1.Finding) bool {
	if f.Status.Phase != v1alpha1.PhaseHandedOff || f.Spec.Approval == nil {
		return false
	}
	done := f.Status.CompletedAt
	return done != nil && !f.Spec.Approval.At.After(done.Time)
}

// pullRequest handles merge/close of a remediation PR, resolved to its
// Finding by the branch name.
func (s *Signals) pullRequest(ctx context.Context, payload []byte) error {
	var ev struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number   int64  `json:"number"`
			HTMLURL  string `json:"html_url"`
			Merged   bool   `json:"merged"`
			MergedAt string `json:"merged_at"`
			Head     struct {
				Ref string `json:"ref"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Errorf("decode pull_request event: %w", err)
	}
	if ev.Action != "closed" || !strings.HasPrefix(ev.PullRequest.Head.Ref, BranchPrefix) {
		return nil
	}
	fnd := strings.TrimPrefix(ev.PullRequest.Head.Ref, BranchPrefix)
	return s.updateFinding(ctx, fnd, func(cur *v1alpha1.Finding) error {
		if cur.Status.Phase != v1alpha1.PhaseInReview {
			return nil // stale or duplicate delivery
		}
		to := v1alpha1.PhaseFailed
		state := "closed"
		if ev.PullRequest.Merged {
			to = v1alpha1.PhaseRemediated
			state = "merged"
		}
		if cur.Status.PullRequest != nil {
			cur.Status.PullRequest.State = state
			if ev.PullRequest.Merged {
				if at, err := time.Parse(time.RFC3339, ev.PullRequest.MergedAt); err == nil {
					t := metav1.NewTime(at)
					cur.Status.PullRequest.MergedAt = &t
				}
			}
		}
		return v1alpha1.SetPhase(cur, to, s.now())
	})
}

// findByIssueURL resolves a Finding by its projected tracking URL; empty
// when the issue is not one of ours.
func (s *Signals) findByIssueURL(ctx context.Context, url string) (string, error) {
	if url == "" {
		return "", nil
	}
	var list v1alpha1.FindingList
	if err := s.List(ctx, &list, client.InNamespace(s.Namespace),
		client.MatchingFields{TrackingURLIndex: url}); err != nil {
		return "", fmt.Errorf("index findings by tracking url: %w", err)
	}
	if len(list.Items) == 0 {
		return "", nil
	}
	return list.Items[0].Name, nil
}

// updateFinding applies mutate under conflict retry; a vanished Finding is a
// no-op.
func (s *Signals) updateFinding(ctx context.Context, name string, mutate func(*v1alpha1.Finding) error) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.Finding
		if err := s.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: name}, &cur); err != nil {
			return client.IgnoreNotFound(err)
		}
		if err := mutate(&cur); err != nil {
			return err
		}
		return s.Status().Update(ctx, &cur)
	})
}

func (s *Signals) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Signals) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}
