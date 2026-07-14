// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package remedctrl

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/ghclient"
	"github.com/bitwise-media-group/patchy/internal/ghfake"
	"github.com/bitwise-media-group/patchy/internal/jobs"
	"github.com/bitwise-media-group/patchy/internal/templates"
	"github.com/bitwise-media-group/patchy/internal/webhook"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

var (
	testLog  = slog.New(slog.NewTextHandler(io.Discard, nil))
	testRepo = ghclient.Repo{Owner: "acme", Name: "shop"}
	baseTime = time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
)

// stores adds the alert/repo surfaces to the shared issue fake.
type stores struct {
	*ghfake.Store
	mu        sync.Mutex
	dismissed []int
	prs       []ghclient.PRRequest
}

func newStores() *stores { return &stores{Store: ghfake.New()} }

func (s *stores) GetAlert(context.Context, ghclient.Repo, int) (*ghclient.Alert, error) {
	return &ghclient.Alert{}, nil
}

func (s *stores) DismissAlert(_ context.Context, _ ghclient.Repo, number int, reason, _ string) error {
	if reason != dismissReason {
		return fmt.Errorf("dismiss reason = %q, want %q", reason, dismissReason)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dismissed = append(s.dismissed, number)
	return nil
}

func (s *stores) DefaultBranch(context.Context, ghclient.Repo) (string, error) { return "main", nil }

func (s *stores) CreatePR(_ context.Context, _ ghclient.Repo, req ghclient.PRRequest) (*ghclient.PR, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prs = append(s.prs, req)
	return &ghclient.PR{Number: 900 + len(s.prs), HTMLURL: "https://gh/pr"}, nil
}

type clients struct{ st *stores }

func (c *clients) For(context.Context, ghclient.Repo) (Stores, error) { return c.st, nil }
func (c *clients) All(context.Context) ([]Searcher, error)            { return []Searcher{c.st}, nil }
func (c *clients) CloneToken(context.Context, ghclient.Repo) (string, error) {
	return "clone-token", nil
}
func (c *clients) PushToken(context.Context, ghclient.Repo) (string, error) { return "push-token", nil }

// fakeRunner scripts the envelope events one Job "produces".
type fakeRunner struct {
	events []envelope.Event
	// followErr forces the live-follow path to fail, exercising the
	// full-log fallback.
	followErr error
	created   []jobs.Spec
	owned     []jobs.Owned
}

func (f *fakeRunner) Create(_ context.Context, spec jobs.Spec) (string, error) {
	f.created = append(f.created, spec)
	return fmt.Sprintf("patchy-job-%d", len(f.created)), nil
}

func (f *fakeRunner) Follow(_ context.Context, _ string, fn func(envelope.Event) error) error {
	if f.followErr != nil {
		return f.followErr
	}
	for _, e := range f.events {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeRunner) Result(context.Context, string) ([]envelope.Event, error) {
	return f.events, nil
}

func (f *fakeRunner) Status(context.Context, string) (jobs.Status, error) {
	return jobs.Status{Done: true, Succeeded: 1}, nil
}

func (f *fakeRunner) List(context.Context) ([]jobs.Owned, error) { return f.owned, nil }
func (f *fakeRunner) Delete(context.Context, string) error       { return nil }

// fakePusher records pushes instead of touching git.
type fakePusher struct {
	pushed  []string
	bundles [][]byte
	err     error
}

func (p *fakePusher) Push(_ context.Context, _ ghclient.Repo, _, _, branch string, bundle []byte) error {
	if p.err != nil {
		return p.err
	}
	p.pushed = append(p.pushed, branch)
	p.bundles = append(p.bundles, bundle)
	return nil
}

const classificationReport = `---
recommendation: remediate
priority: high
severity: high
confidence: 0.9
breaking_change_available: false
model: claude-sonnet-5
max_turns: 40
token_budget: 200000
---
Real finding.
`

// seedIssue creates an eligible (context-enhanced, accumulation-complete)
// finding issue with an enrichment comment naming an owner.
func seedIssue(t *testing.T, st *stores) *ghclient.Issue {
	t.Helper()
	m := templates.NewManifest(source.Finding{
		Source: "ghas", Repo: source.Repo{Owner: "acme", Name: "shop"},
		AlertNumber: 7, Advisories: []string{"CWE-79"}, Title: "XSS", Severity: "high",
	})
	m.Add(source.Finding{AlertNumber: 9})
	body, err := templates.RenderIssueBody(m)
	if err != nil {
		t.Fatal(err)
	}
	st.Now = func() time.Time { return baseTime }
	issue, err := st.Create(context.Background(), testRepo, ghclient.IssueRequest{
		Title: "[ghas] CWE-79: XSS",
		Body:  body,
		Labels: []string{
			"security-source: ghas", "security-advisory: CWE-79",
			"security-alert: 7", "security-alert: 9",
			"security-finding: context-enhanced", "security-accumulation: complete",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	enrichment, err := templates.RenderEnrichmentComment(
		templates.Enrichment{Enhancer: "cmdb", Owners: []string{"octocat"}}, "**Owners:** @octocat")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Comment(context.Background(), testRepo, issue.Number, enrichment); err != nil {
		t.Fatal(err)
	}
	return issue
}

func newController(st *stores, r *fakeRunner, p *fakePusher) *Controller {
	c := New(testLog, &clients{st}, r, p, Config{MinAge: time.Hour, MaxAttempts: 2})
	c.now = func() time.Time { return baseTime.Add(2 * time.Hour) }
	return c
}

// classificationEvent builds a stage-1 event with the given verdict.
func classificationEvent(rec string, confidence float64, willRemediate, awaitApproval bool) envelope.Event {
	return envelope.Event{
		Type: envelope.TypeClassification,
		Classification: &envelope.Classification{
			Stage: envelope.Stage{
				Outcome: envelope.OutcomeOK, Harness: "claude", Model: "claude-sonnet-5",
				SessionID: "a1b2c3d4-0000-0000-0000-000000000000", NumTurns: 9,
				Usage:          envelope.Usage{InputTokens: 100, OutputTokens: 200, CostUSD: 0.42},
				ElapsedSeconds: 12.5,
			},
			ReportMarkdown: classificationReport,
			Recommendation: rec, Priority: "high", Severity: "high", Confidence: confidence,
			RemediationModel: "claude-sonnet-5", MaxTurns: 40, TokenBudget: 200000,
			WillRemediate: willRemediate, AwaitApproval: awaitApproval,
		},
	}
}

func remediationEvent(success bool, bundle []byte) envelope.Event {
	rem := &envelope.Remediation{
		Stage: envelope.Stage{
			Outcome: envelope.OutcomeOK, Harness: "claude", Model: "claude-sonnet-5",
			SessionID: "b2c3d4e5-0000-0000-0000-000000000000", NumTurns: 20,
			Usage:          envelope.Usage{InputTokens: 500, OutputTokens: 900, CostUSD: 1.10},
			ElapsedSeconds: 300,
		},
		ReportMarkdown: "---\nsuccess: true\nconfidence: 0.9\n---\nEscaped the sink.\n",
		Success:        success,
		Confidence:     0.9,
	}
	if success {
		rem.Branch = "patchy/issue-101"
		rem.BundleB64 = base64.StdEncoding.EncodeToString(bundle)
	}
	return envelope.Event{Type: envelope.TypeRemediation, Remediation: rem}
}

func labelsOf(st *stores, number int) []string { return st.Issues[number].Labels }

func TestRemediatesEndToEnd(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	pusher := &fakePusher{}
	runner := &fakeRunner{events: []envelope.Event{
		classificationEvent("remediate", 0.9, true, false),
		remediationEvent(true, []byte("BUNDLE")),
	}}

	if err := newController(st, runner, pusher).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// The Job ran the full phase against the right issue.
	if len(runner.created) != 1 {
		t.Fatalf("jobs created = %d, want 1", len(runner.created))
	}
	spec := runner.created[0]
	if spec.Phase != phaseFull || spec.Issue != issue.Number || spec.Repo != "acme/shop" {
		t.Errorf("job spec = %+v", spec)
	}
	if spec.Token != "clone-token" || !strings.Contains(spec.IssueMarkdown, "XSS") {
		t.Errorf("job spec inputs = token:%q markdown:%q", spec.Token, spec.IssueMarkdown)
	}

	// The branch was pushed and the PR opened.
	if !slices.Equal(pusher.pushed, []string{"patchy/issue-101"}) {
		t.Errorf("pushed = %v, want the agent's branch", pusher.pushed)
	}
	if string(pusher.bundles[0]) != "BUNDLE" {
		t.Errorf("pushed bundle = %q, want the decoded changeset", pusher.bundles[0])
	}
	if len(st.prs) != 1 {
		t.Fatalf("PRs = %d, want 1", len(st.prs))
	}
	pr := st.prs[0]
	if pr.Head != "patchy/issue-101" || pr.Base != "main" {
		t.Errorf("PR = %+v, want head=patchy/issue-101 base=main", pr)
	}
	if !strings.Contains(pr.Body, fmt.Sprintf("Fixes #%d", issue.Number)) {
		t.Errorf("PR body missing the auto-close reference:\n%s", pr.Body)
	}

	// The issue is in review, with both stages' accounting stamped on it.
	got := labelsOf(st, issue.Number)
	for _, want := range []string{
		"security-finding: in-review",
		"security-severity: high",
		"security-priority: high",
		"security-recommendation: remediate",
		"security-recommendation-confidence: 0.9",
		"security-classifier: claude",
		"security-remediator: claude",
		"security-token-budget: 200000",
		"security-max-turns: 40",
		"security-classification-session: a1b2c3d4",
		"security-remediation-session: b2c3d4e5",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("labels missing %q:\n%v", want, got)
		}
	}
	if slices.Contains(got, "security-finding: classifying") {
		t.Errorf("lease label never released: %v", got)
	}
}

func TestDismissesFalsePositive(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	runner := &fakeRunner{events: []envelope.Event{classificationEvent("ignore", 0.97, false, false)}}

	if err := newController(st, runner, &fakePusher{}).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Every accumulated alert is dismissed in GHAS, and the issue closed.
	if want := []int{7, 9}; !slices.Equal(st.dismissed, want) {
		t.Errorf("dismissed alerts = %v, want %v (all alerts in the manifest)", st.dismissed, want)
	}
	if st.Issues[issue.Number].State != "closed" {
		t.Error("issue not closed after dismissal")
	}
	if !slices.Contains(labelsOf(st, issue.Number), "security-recommendation: ignore") {
		t.Errorf("labels = %v, want recommendation ignore", labelsOf(st, issue.Number))
	}
}

func TestRoutesToHumans(t *testing.T) {
	tests := []struct {
		name        string
		event       envelope.Event
		wantApprove bool
	}{
		{
			name:  "manual assigns the owner",
			event: classificationEvent("manual", 0.9, false, false),
		},
		{
			name:  "an unknown recommendation falls back to manual",
			event: classificationEvent("escalate", 0.9, false, false),
		},
		{
			name:        "low confidence offers /approve",
			event:       classificationEvent("remediate", 0.6, false, false),
			wantApprove: true,
		},
		{
			name:        "breaking-change hold offers /approve",
			event:       classificationEvent("remediate", 0.95, false, true),
			wantApprove: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newStores()
			issue := seedIssue(t, st)
			runner := &fakeRunner{events: []envelope.Event{tt.event}}

			if err := newController(st, runner, &fakePusher{}).Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}

			if got := st.Assigned[issue.Number]; !slices.Contains(got, "octocat") {
				t.Errorf("assignees = %v, want the enrichment's owner", got)
			}
			if st.Issues[issue.Number].State != "open" {
				t.Error("issue closed; a human still has work to do")
			}
			if len(st.prs) != 0 {
				t.Error("a PR was opened without a successful remediation")
			}

			joined := strings.Join(st.Comments[issue.Number], "\n")
			if got := strings.Contains(joined, "/approve"); got != tt.wantApprove {
				t.Errorf("comment offers /approve = %v, want %v", got, tt.wantApprove)
			}
		})
	}
}

func TestApproveForcesRemediation(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	// Start from a classified, held-back issue: low confidence, with the
	// classification report on the issue as a comment.
	runner := &fakeRunner{events: []envelope.Event{classificationEvent("remediate", 0.6, false, false)}}
	c := newController(st, runner, &fakePusher{})
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(labelsOf(st, issue.Number), "security-finding: classified") {
		t.Fatalf("setup: labels = %v, want classified", labelsOf(st, issue.Number))
	}

	// A maintainer approves.
	pusher := &fakePusher{}
	runner2 := &fakeRunner{events: []envelope.Event{remediationEvent(true, []byte("BUNDLE"))}}
	c2 := newController(st, runner2, pusher)
	payload := fmt.Appendf(nil, `{"action":"created","issue":{"number":%d,"state":"open"},
		"comment":{"body":"/approve","author_association":"MEMBER","user":{"login":"octocat"}},
		"repository":{"name":"shop","owner":{"login":"acme"}}}`, issue.Number)

	if err := c2.Handle(context.Background(), webhook.Event{Type: "issue_comment", Payload: payload}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if len(runner2.created) != 1 {
		t.Fatalf("jobs created = %d, want 1", len(runner2.created))
	}
	spec := runner2.created[0]
	if spec.Phase != phaseRemediate {
		t.Errorf("phase = %q, want remediate-only", spec.Phase)
	}
	if !strings.Contains(spec.ClassificationMarkdown, "recommendation: remediate") {
		t.Errorf("the re-run was not handed the classification report:\n%q", spec.ClassificationMarkdown)
	}
	if !slices.Equal(pusher.pushed, []string{"patchy/issue-101"}) {
		t.Errorf("pushed = %v, want the remediation branch", pusher.pushed)
	}
	if !slices.Contains(labelsOf(st, issue.Number), "security-finding: in-review") {
		t.Errorf("labels = %v, want in-review", labelsOf(st, issue.Number))
	}
}

func TestApproveIgnoredFromNonMaintainer(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	runner := &fakeRunner{events: []envelope.Event{classificationEvent("remediate", 0.6, false, false)}}
	c := newController(st, runner, &fakePusher{})
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	runner2 := &fakeRunner{}
	c2 := newController(st, runner2, &fakePusher{})
	payload := fmt.Appendf(nil, `{"action":"created","issue":{"number":%d,"state":"open"},
		"comment":{"body":"/approve","author_association":"NONE","user":{"login":"drive-by"}},
		"repository":{"name":"shop","owner":{"login":"acme"}}}`, issue.Number)

	if err := c2.Handle(context.Background(), webhook.Event{Type: "issue_comment", Payload: payload}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(runner2.created) != 0 {
		t.Error("a drive-by /approve started a remediation job")
	}
}

func TestPullRequestMergedClosesIssue(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	runner := &fakeRunner{events: []envelope.Event{
		classificationEvent("remediate", 0.9, true, false),
		remediationEvent(true, []byte("BUNDLE")),
	}}
	c := newController(st, runner, &fakePusher{})
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	payload := fmt.Appendf(nil, `{"action":"closed","pull_request":{"number":900,"merged":true,
		"head":{"ref":"patchy/issue-%d"}},"repository":{"name":"shop","owner":{"login":"acme"}}}`, issue.Number)
	if err := c.Handle(context.Background(), webhook.Event{Type: "pull_request", Payload: payload}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if !slices.Contains(labelsOf(st, issue.Number), "security-finding: remediated") {
		t.Errorf("labels = %v, want remediated", labelsOf(st, issue.Number))
	}
	if st.Issues[issue.Number].State != "closed" {
		t.Error("issue not closed after the PR merged")
	}
}

func TestPullRequestClosedUnmergedHandsBack(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	runner := &fakeRunner{events: []envelope.Event{
		classificationEvent("remediate", 0.9, true, false),
		remediationEvent(true, []byte("BUNDLE")),
	}}
	c := newController(st, runner, &fakePusher{})
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	payload := fmt.Appendf(nil, `{"action":"closed","pull_request":{"number":900,"merged":false,
		"head":{"ref":"patchy/issue-%d"}},"repository":{"name":"shop","owner":{"login":"acme"}}}`, issue.Number)
	if err := c.Handle(context.Background(), webhook.Event{Type: "pull_request", Payload: payload}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	got := labelsOf(st, issue.Number)
	if !slices.Contains(got, "security-finding: attempted") ||
		!slices.Contains(got, "security-recommendation: manual") {
		t.Errorf("labels = %v, want attempted + manual", got)
	}
	if st.Issues[issue.Number].State != "open" {
		t.Error("issue closed; a rejected remediation still needs a human")
	}
}

func TestFailedRemediationHandsBack(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	runner := &fakeRunner{events: []envelope.Event{
		classificationEvent("remediate", 0.9, true, false),
		remediationEvent(false, nil),
	}}

	if err := newController(st, runner, &fakePusher{}).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	got := labelsOf(st, issue.Number)
	if !slices.Contains(got, "security-finding: attempted") {
		t.Errorf("labels = %v, want attempted", got)
	}
	if len(st.prs) != 0 {
		t.Error("a PR was opened for a failed remediation")
	}
	if !slices.Contains(st.Assigned[issue.Number], "octocat") {
		t.Error("failed remediation was not assigned to a human")
	}
}

func TestRetriesThenExhausts(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	// A Job that reports nothing at all: the attempt failed.
	runner := &fakeRunner{}
	c := newController(st, runner, &fakePusher{})

	// First attempt: the lease is released for a retry.
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	got := labelsOf(st, issue.Number)
	if !slices.Contains(got, "security-finding: context-enhanced") {
		t.Fatalf("labels = %v, want the lease released for retry", got)
	}
	if !slices.Contains(got, "security-attempts: 1") {
		t.Errorf("labels = %v, want the attempt counted", got)
	}

	// Second attempt exhausts the budget and hands the finding to a human.
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	got = labelsOf(st, issue.Number)
	if !slices.Contains(got, "security-finding: attempted") {
		t.Errorf("labels = %v, want attempted after exhausting retries", got)
	}
	if !slices.Contains(got, "security-attempts: 2") {
		t.Errorf("labels = %v, want two attempts recorded", got)
	}
	if !slices.Contains(st.Assigned[issue.Number], "octocat") {
		t.Error("exhausted finding not assigned to a human")
	}
}

func TestFallsBackToJobResultWhenFollowBreaks(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	runner := &fakeRunner{
		followErr: fmt.Errorf("log stream broke"),
		events: []envelope.Event{
			classificationEvent("remediate", 0.9, true, false),
			remediationEvent(true, []byte("BUNDLE")),
		},
	}
	pusher := &fakePusher{}

	if err := newController(st, runner, pusher).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	// Everything still lands, from the completed Job's full log.
	if !slices.Contains(labelsOf(st, issue.Number), "security-finding: in-review") {
		t.Errorf("labels = %v, want in-review via the fallback path", labelsOf(st, issue.Number))
	}
	if len(pusher.pushed) != 1 || len(st.prs) != 1 {
		t.Errorf("fallback did not complete the remediation: pushes=%v prs=%d", pusher.pushed, len(st.prs))
	}
}

func TestEventsAreAppliedOnce(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	// Follow delivers the events, and Result replays the same ones — the
	// controller must not act twice.
	runner := &fakeRunner{events: []envelope.Event{
		classificationEvent("ignore", 0.97, false, false),
	}}

	if err := newController(st, runner, &fakePusher{}).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if want := []int{7, 9}; !slices.Equal(st.dismissed, want) {
		t.Errorf("dismissed = %v, want each alert dismissed exactly once (%v)", st.dismissed, want)
	}
	reports := 0
	for _, cm := range st.Comments[issue.Number] {
		if strings.Contains(cm, templates.ClassificationReportHeading) {
			reports++
		}
	}
	if reports != 1 {
		t.Errorf("classification report posted %d times, want once", reports)
	}
}

func TestSkipsIssuesInsideTheWindow(t *testing.T) {
	st := newStores()
	seedIssue(t, st)
	runner := &fakeRunner{}
	c := New(testLog, &clients{st}, runner, &fakePusher{}, Config{MinAge: time.Hour})
	c.now = func() time.Time { return baseTime.Add(30 * time.Minute) }

	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(runner.created) != 0 {
		t.Error("an issue younger than the minimum age was picked up")
	}
}

func TestReapsOrphanedLease(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	// An issue stuck in classifying with no live Job — a controller that
	// died mid-run.
	if err := st.AddLabels(context.Background(), testRepo, issue.Number,
		[]string{"security-finding: classifying"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveLabel(context.Background(), testRepo, issue.Number,
		"security-finding: context-enhanced"); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{} // no jobs at all
	if err := newController(st, runner, &fakePusher{}).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	got := labelsOf(st, issue.Number)
	if !slices.Contains(got, "security-finding: context-enhanced") {
		t.Errorf("labels = %v, want the orphaned lease released", got)
	}
	if !slices.Contains(got, "security-attempts: 1") {
		t.Errorf("labels = %v, want the failed attempt counted", got)
	}
}

func TestLeaveLiveJobAlone(t *testing.T) {
	st := newStores()
	issue := seedIssue(t, st)
	if err := st.AddLabels(context.Background(), testRepo, issue.Number,
		[]string{"security-finding: classifying"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveLabel(context.Background(), testRepo, issue.Number,
		"security-finding: context-enhanced"); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{owned: []jobs.Owned{{
		Name: "patchy-job-1", Repo: "acme/shop", Issue: issue.Number,
		Status: jobs.Status{Active: 1},
	}}}
	if err := newController(st, runner, &fakePusher{}).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if !slices.Contains(labelsOf(st, issue.Number), "security-finding: classifying") {
		t.Errorf("labels = %v, want the live lease untouched", labelsOf(st, issue.Number))
	}
}
