// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package web

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/bitwise-media-group/patchy/api/v1alpha1"
	"github.com/bitwise-media-group/patchy/internal/report"
	"github.com/bitwise-media-group/patchy/internal/stats"
	"github.com/bitwise-media-group/patchy/internal/version"
)

// Dataset is the payload behind GET /api/findings (everything) and
// GET /api/rollups (findings empty, no user). It mirrors ui/src/types.ts.
type Dataset struct {
	GeneratedAt string    `json:"generatedAt"`
	Namespace   string    `json:"namespace,omitempty"`
	Version     string    `json:"version,omitempty"`
	User        *User     `json:"user,omitempty"`
	Findings    []Finding `json:"findings"`
	Rollups     []Rollup  `json:"rollups,omitempty"`
}

// User is the signed-in identity the top bar renders.
type User struct {
	Name     string `json:"name"`
	LoggedIn bool   `json:"loggedIn"`
	// AdminActions are the granted namespace-wide verbs (replay, reset) the
	// user menu renders. Absent means none.
	AdminActions []string `json:"adminActions,omitempty"`
}

// Finding is the flattened metadata+spec+status projection of one Finding,
// plus the requesting user's granted action verbs.
type Finding struct {
	Name        string      `json:"name"`
	CreatedAt   string      `json:"createdAt,omitempty"`
	Integration string      `json:"integration,omitempty"`
	Source      string      `json:"source,omitempty"`
	Repository  *Repository `json:"repository,omitempty"`
	Advisories  []string    `json:"advisories"`
	RuleID      string      `json:"ruleID,omitempty"`
	Title       string      `json:"title,omitempty"`
	Description string      `json:"description,omitempty"`
	Severity    string      `json:"severity,omitempty"`
	Alerts      []Alert     `json:"alerts,omitempty"`
	// OverflowAlerts counts alerts dropped past the accumulation cap.
	OverflowAlerts  int32          `json:"overflowAlerts,omitempty"`
	Related         []Related      `json:"related,omitempty"`
	Suspend         bool           `json:"suspend,omitempty"`
	Approval        *Approval      `json:"approval,omitempty"`
	Retry           *ActionRequest `json:"retry,omitempty"`
	Expedite        *ActionRequest `json:"expedite,omitempty"`
	Phase           string         `json:"phase,omitempty"`
	PhaseTimes      []PhaseTime    `json:"phaseTimes,omitempty"`
	FirstObservedAt string         `json:"firstObservedAt,omitempty"`
	AccumulateUntil string         `json:"accumulateUntil,omitempty"`
	Tracking        *Tracking      `json:"tracking,omitempty"`
	Owners          []string       `json:"owners,omitempty"`
	Enrichments     []Enrichment   `json:"enrichments,omitempty"`
	Priority        string         `json:"priority,omitempty"`
	Investigation   *Investigation `json:"investigation,omitempty"`
	Remediation     *Remediation   `json:"remediation,omitempty"`
	PullRequest     *PullRequest   `json:"pullRequest,omitempty"`
	Attempts        *Attempts      `json:"attempts,omitempty"`
	// TotalUsage sums token and cost accounting across every attempt of
	// both stages, lifted from the Investigation/Remediation children.
	TotalUsage        *Usage     `json:"totalUsage,omitempty"`
	ActiveRun         *ActiveRun `json:"activeRun,omitempty"`
	LastFailureReason string     `json:"lastFailureReason,omitempty"`
	CompletedAt       string     `json:"completedAt,omitempty"`
	// UserActions are the verbs the requesting user may invoke; the client
	// intersects them with the state machine. Absent means read-only.
	UserActions []string `json:"userActions,omitempty"`
}

// Repository locates the finding's repository.
type Repository struct {
	Type          string `json:"type"`
	URL           string `json:"url"`
	Name          string `json:"name,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
}

// Location is one source location an alert points at.
type Location struct {
	Path      string `json:"path"`
	StartLine int32  `json:"startLine,omitempty"`
	EndLine   int32  `json:"endLine,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
}

// Alert is one scanner alert folded into the finding.
type Alert struct {
	ID        string     `json:"id"`
	URL       string     `json:"url,omitempty"`
	Locations []Location `json:"locations,omitempty"`
}

// Related is one relationship edge, named from this finding's perspective:
// the CRD stores {from, to}, the wire type carries the other endpoint.
type Related struct {
	Name         string `json:"name"`
	Relationship string `json:"relationship"`
}

// Approval is the recorded human approval.
type Approval struct {
	By   string `json:"by"`
	At   string `json:"at"`
	Note string `json:"note,omitempty"`
}

// ActionRequest is a recorded human retry/expedite request.
type ActionRequest struct {
	By string `json:"by"`
	At string `json:"at"`
}

// PhaseTime is one phase-entry log record.
type PhaseTime struct {
	Phase string `json:"phase"`
	At    string `json:"at"`
}

// Tracking links the projected tracking issue.
type Tracking struct {
	IssueNumber int64  `json:"issueNumber,omitempty"`
	URL         string `json:"url,omitempty"`
	State       string `json:"state,omitempty"`
}

// Enrichment is one enhancer contribution.
type Enrichment struct {
	Enhancer   string            `json:"enhancer"`
	Owners     []string          `json:"owners,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Markdown   string            `json:"markdown,omitempty"`
	AppliedAt  string            `json:"appliedAt,omitempty"`
}

// Investigation mirrors the Finding's investigation summary, plus the
// report markdown and run accounting lifted from the Investigation child.
type Investigation struct {
	Name           string `json:"name,omitempty"`
	Attempt        int32  `json:"attempt,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
	Confidence     string `json:"confidence,omitempty"`
	Exploitability string `json:"exploitability,omitempty"`
	Likelihood     string `json:"likelihood,omitempty"`
	Impact         string `json:"impact,omitempty"`
	AwaitApproval  bool   `json:"awaitApproval,omitempty"`
	CompletedAt    string `json:"completedAt,omitempty"`
	Report         string `json:"report,omitempty"`
	Harness        string `json:"harness,omitempty"`
	Model          string `json:"model,omitempty"`
	Usage          *Usage `json:"usage,omitempty"`
}

// Remediation mirrors the Finding's remediation summary, plus the report
// markdown and run accounting lifted from the Remediation child.
type Remediation struct {
	Name        string `json:"name,omitempty"`
	Attempt     int32  `json:"attempt,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
	Success     bool   `json:"success,omitempty"`
	Branch      string `json:"branch,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	Report      string `json:"report,omitempty"`
	Harness     string `json:"harness,omitempty"`
	Model       string `json:"model,omitempty"`
	Usage       *Usage `json:"usage,omitempty"`
}

// Usage is token and cost accounting — one run's, or the finding's total
// across every attempt of both stages. Cost is integer micro-USD, matching
// the rollup wire type.
type Usage struct {
	InputTokens         int64 `json:"inputTokens,omitempty"`
	OutputTokens        int64 `json:"outputTokens,omitempty"`
	CacheReadTokens     int64 `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int64 `json:"cacheCreationTokens,omitempty"`
	CostMicroUSD        int64 `json:"costMicroUSD,omitempty"`
}

// PullRequest is the remediation pull request's lifecycle.
type PullRequest struct {
	Number   int64  `json:"number"`
	URL      string `json:"url,omitempty"`
	State    string `json:"state,omitempty"`
	MergedAt string `json:"mergedAt,omitempty"`
}

// Attempts tallies agent runs per stage.
type Attempts struct {
	Investigation int32 `json:"investigation,omitempty"`
	Remediation   int32 `json:"remediation,omitempty"`
}

// ActiveRun points at the child currently running.
type ActiveRun struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Rollup is one scope's all-time statistics, identified by scope.key ("" is
// the total scope) — never by object name.
type Rollup struct {
	Scope          RollupScope              `json:"scope"`
	FirstProcessed string                   `json:"firstProcessed,omitempty"`
	LastProcessed  string                   `json:"lastProcessed,omitempty"`
	Bucket         RollupBucket             `json:"bucket"`
	Monthly        map[string]MonthlyBucket `json:"monthly,omitempty"`
}

// RollupScope identifies one rollup dimension value.
type RollupScope struct {
	Type string `json:"type"`
	Key  string `json:"key,omitempty"`
}

// RollupBucket carries the finding-level counters; harness and model scopes
// carry only stages.
type RollupBucket struct {
	Findings        int64                     `json:"findings,omitempty"`
	Phases          map[string]int64          `json:"phases,omitempty"`
	Recommendations map[string]int64          `json:"recommendations,omitempty"`
	Attempts        int64                     `json:"attempts,omitempty"`
	Stages          map[string]StageAggregate `json:"stages,omitempty"`
}

// StageAggregate is one stage's raw sums; the client computes rates and
// averages.
type StageAggregate struct {
	Runs                int64            `json:"runs,omitempty"`
	Succeeded           int64            `json:"succeeded,omitempty"`
	Outcomes            map[string]int64 `json:"outcomes,omitempty"`
	InputTokens         int64            `json:"inputTokens,omitempty"`
	OutputTokens        int64            `json:"outputTokens,omitempty"`
	CacheReadTokens     int64            `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int64            `json:"cacheCreationTokens,omitempty"`
	CostMicroUSD        int64            `json:"costMicroUSD,omitempty"`
	ElapsedMilliseconds int64            `json:"elapsedMilliseconds,omitempty"`
}

// MonthlyBucket is one month of the total scope's trend line.
type MonthlyBucket struct {
	Findings     int64 `json:"findings,omitempty"`
	Runs         int64 `json:"runs,omitempty"`
	CostMicroUSD int64 `json:"costMicroUSD,omitempty"`
}

// buildDataset assembles the payload from the cached client. userActions is
// stamped uniformly — RBAC grants are namespace-scoped, and the client
// intersects with each finding's state machine itself. withFindings=false
// produces the public rollups-only projection.
func (s *Server) buildDataset(ctx context.Context, withFindings bool, verbs []string, user *User) (*Dataset, error) {
	ds := &Dataset{
		GeneratedAt: s.now().UTC().Format(time.RFC3339),
		Namespace:   s.namespace,
		Version:     version.Version,
		User:        user,
		Findings:    []Finding{},
	}

	var rollups v1alpha1.FindingRollupList
	if err := s.client.List(ctx, &rollups, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list rollups: %w", err)
	}
	for i := range rollups.Items {
		ds.Rollups = append(ds.Rollups, projectRollup(&rollups.Items[i]))
	}
	slices.SortFunc(ds.Rollups, func(a, b Rollup) int {
		if c := cmp.Compare(a.Scope.Type, b.Scope.Type); c != 0 {
			return c
		}
		return cmp.Compare(a.Scope.Key, b.Scope.Key)
	})

	if !withFindings {
		return ds, nil
	}
	var findings v1alpha1.FindingList
	if err := s.client.List(ctx, &findings, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list findings: %w", err)
	}
	runs := s.loadRunDetails(ctx)
	for i := range findings.Items {
		out := projectFinding(&findings.Items[i], verbs)
		runs.attach(&findings.Items[i], &out)
		ds.Findings = append(ds.Findings, out)
	}
	// Newest first, stable across refetches.
	slices.SortFunc(ds.Findings, func(a, b Finding) int {
		at, bt := cmp.Or(a.FirstObservedAt, a.CreatedAt), cmp.Or(b.FirstObservedAt, b.CreatedAt)
		if c := cmp.Compare(bt, at); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return ds, nil
}

// runDetails indexes the Investigation/Remediation children, listed once
// per dataset build: the summarised (latest) child's report and stage
// accounting by child name, plus per-finding usage totals across every
// attempt of both stages.
type runDetails struct {
	inv    map[string]*v1alpha1.Investigation
	rem    map[string]*v1alpha1.Remediation
	totals map[string]Usage
}

// loadRunDetails lists the children. Errors degrade gracefully — reports
// and usage are absent, the findings surface still renders (a deployment
// whose RBAC predates the child read grant must not lose the whole page).
func (s *Server) loadRunDetails(ctx context.Context) runDetails {
	d := runDetails{
		inv:    map[string]*v1alpha1.Investigation{},
		rem:    map[string]*v1alpha1.Remediation{},
		totals: map[string]Usage{},
	}
	var invs v1alpha1.InvestigationList
	if err := s.client.List(ctx, &invs, client.InNamespace(s.namespace)); err == nil {
		for i := range invs.Items {
			child := &invs.Items[i]
			d.inv[child.Name] = child
			d.addStage(child.Labels[v1alpha1.LabelFinding], child.Status.Stage)
		}
	}
	var rems v1alpha1.RemediationList
	if err := s.client.List(ctx, &rems, client.InNamespace(s.namespace)); err == nil {
		for i := range rems.Items {
			child := &rems.Items[i]
			d.rem[child.Name] = child
			d.addStage(child.Labels[v1alpha1.LabelFinding], child.Status.Stage)
		}
	}
	return d
}

// addStage folds one child run's accounting into its finding's total.
func (d *runDetails) addStage(finding string, st *v1alpha1.StageResult) {
	if finding == "" || st == nil {
		return
	}
	u := d.totals[finding]
	if su := stageUsage(st); su != nil {
		u.InputTokens += su.InputTokens
		u.OutputTokens += su.OutputTokens
		u.CacheReadTokens += su.CacheReadTokens
		u.CacheCreationTokens += su.CacheCreationTokens
		u.CostMicroUSD += su.CostMicroUSD
	}
	d.totals[finding] = u
}

// attach lifts the child-only fields (report, harness, model, usage, and
// the cross-attempt total) onto one finding's projection. An absent child
// (expired, deleted) simply leaves them empty.
func (d *runDetails) attach(f *v1alpha1.Finding, out *Finding) {
	if inv := f.Status.Investigation; inv != nil && out.Investigation != nil {
		if child := d.inv[inv.Name]; child != nil {
			// Runs recorded before the runner stored body-only reports
			// carry the raw frontmatter; strip it rather than render YAML.
			out.Investigation.Report = report.StripFrontmatter(child.Status.Report)
			if st := child.Status.Stage; st != nil {
				out.Investigation.Harness = st.Harness
				out.Investigation.Model = st.Model
				out.Investigation.Usage = stageUsage(st)
			}
		}
	}
	if rem := f.Status.Remediation; rem != nil && out.Remediation != nil {
		if child := d.rem[rem.Name]; child != nil {
			out.Remediation.Report = report.StripFrontmatter(child.Status.Report)
			if st := child.Status.Stage; st != nil {
				out.Remediation.Harness = st.Harness
				out.Remediation.Model = st.Model
				out.Remediation.Usage = stageUsage(st)
			}
		}
	}
	if u, ok := d.totals[f.Name]; ok && u != (Usage{}) {
		out.TotalUsage = &u
	}
}

// stageUsage converts a stage's usage block onto the wire type, or nil when
// the harness reported nothing. A malformed cost string parses as zero —
// the tokens still render.
func stageUsage(st *v1alpha1.StageResult) *Usage {
	u := Usage{
		InputTokens:         st.Usage.InputTokens,
		OutputTokens:        st.Usage.OutputTokens,
		CacheReadTokens:     st.Usage.CacheReadTokens,
		CacheCreationTokens: st.Usage.CacheCreationTokens,
	}
	if micro, err := stats.ParseCostMicroUSD(st.Usage.CostUSD); err == nil {
		u.CostMicroUSD = micro
	}
	if u == (Usage{}) {
		return nil
	}
	return &u
}

// projectFinding flattens one Finding CR onto the wire type.
func projectFinding(f *v1alpha1.Finding, verbs []string) Finding {
	spec, st := &f.Spec, &f.Status
	out := Finding{
		Name:              f.Name,
		CreatedAt:         stamp(f.CreationTimestamp),
		Integration:       spec.IntegrationRef.Name,
		Source:            spec.Source,
		Advisories:        spec.Advisories,
		RuleID:            spec.RuleID,
		Title:             spec.Title,
		Description:       spec.Description,
		Severity:          string(spec.Severity),
		OverflowAlerts:    spec.OverflowAlerts,
		Suspend:           spec.Suspend,
		Phase:             string(st.Phase),
		FirstObservedAt:   stampPtr(st.FirstObservedAt),
		AccumulateUntil:   stampPtr(st.AccumulateUntil),
		Owners:            st.Owners,
		Priority:          string(st.Priority),
		LastFailureReason: st.LastFailureReason,
		CompletedAt:       stampPtr(st.CompletedAt),
		UserActions:       verbs,
	}
	if spec.Repository != nil {
		out.Repository = &Repository{
			Type:          string(spec.Repository.Type),
			URL:           spec.Repository.URL,
			Name:          spec.Repository.Name,
			DefaultBranch: spec.Repository.DefaultBranch,
		}
	}
	for _, a := range spec.Alerts {
		alert := Alert{ID: a.ID, URL: a.URL}
		for _, l := range a.Locations {
			alert.Locations = append(alert.Locations, Location{
				Path: l.Path, StartLine: l.StartLine, EndLine: l.EndLine, Snippet: l.Snippet,
			})
		}
		out.Alerts = append(out.Alerts, alert)
	}
	for _, rel := range spec.Related {
		other := rel.From
		if other == f.Name {
			other = rel.To
		}
		out.Related = append(out.Related, Related{Name: other, Relationship: string(rel.Relationship)})
	}
	if spec.Approval != nil {
		out.Approval = &Approval{By: spec.Approval.By, At: stamp(spec.Approval.At), Note: spec.Approval.Note}
	}
	if spec.Retry != nil {
		out.Retry = &ActionRequest{By: spec.Retry.By, At: stamp(spec.Retry.At)}
	}
	if spec.Expedite != nil {
		out.Expedite = &ActionRequest{By: spec.Expedite.By, At: stamp(spec.Expedite.At)}
	}
	for _, pt := range st.PhaseTimes {
		out.PhaseTimes = append(out.PhaseTimes, PhaseTime{Phase: string(pt.Phase), At: stamp(pt.At)})
	}
	if st.Tracking != nil {
		out.Tracking = &Tracking{
			IssueNumber: st.Tracking.IssueNumber,
			URL:         st.Tracking.URL,
			State:       st.Tracking.State,
		}
	}
	for _, e := range st.Enrichments {
		out.Enrichments = append(out.Enrichments, Enrichment{
			Enhancer: e.Enhancer, Owners: e.Owners, Attributes: e.Attributes,
			Markdown: e.Markdown, AppliedAt: stamp(e.AppliedAt),
		})
	}
	if inv := st.Investigation; inv != nil {
		out.Investigation = &Investigation{
			Name:           inv.Name,
			Attempt:        inv.Attempt,
			Outcome:        inv.Outcome,
			Recommendation: string(inv.Recommendation),
			Confidence:     inv.Confidence,
			Exploitability: string(inv.Exploitability),
			Likelihood:     string(inv.Likelihood),
			Impact:         string(inv.Impact),
			AwaitApproval:  inv.AwaitApproval,
			CompletedAt:    stampPtr(inv.CompletedAt),
		}
	}
	if rem := st.Remediation; rem != nil {
		out.Remediation = &Remediation{
			Name:        rem.Name,
			Attempt:     rem.Attempt,
			Outcome:     rem.Outcome,
			Success:     rem.Success,
			Branch:      rem.Branch,
			CompletedAt: stampPtr(rem.CompletedAt),
		}
	}
	if pr := st.PullRequest; pr != nil {
		out.PullRequest = &PullRequest{
			Number: pr.Number, URL: pr.URL, State: pr.State, MergedAt: stampPtr(pr.MergedAt),
		}
	}
	if st.Attempts != (v1alpha1.AttemptCounts{}) {
		out.Attempts = &Attempts{
			Investigation: st.Attempts.Investigation,
			Remediation:   st.Attempts.Remediation,
		}
	}
	if st.ActiveRun != nil {
		out.ActiveRun = &ActiveRun{Kind: string(st.ActiveRun.Kind), Name: st.ActiveRun.Name}
	}
	return out
}

// projectRollup flattens one FindingRollup CR onto the wire type. The ledger
// and schema version stay server-side.
func projectRollup(fr *v1alpha1.FindingRollup) Rollup {
	st := &fr.Status
	out := Rollup{
		Scope:          RollupScope{Type: string(fr.Spec.Scope.Type), Key: fr.Spec.Scope.Key},
		FirstProcessed: stampPtr(st.FirstProcessed),
		LastProcessed:  stampPtr(st.LastProcessed),
		Bucket: RollupBucket{
			Findings:        st.Bucket.Findings,
			Phases:          st.Bucket.Phases,
			Recommendations: st.Bucket.Recommendations,
			Attempts:        st.Bucket.Attempts,
		},
	}
	if len(st.Bucket.Stages) > 0 {
		out.Bucket.Stages = make(map[string]StageAggregate, len(st.Bucket.Stages))
		for name, agg := range st.Bucket.Stages {
			out.Bucket.Stages[name] = StageAggregate{
				Runs:                agg.Runs,
				Succeeded:           agg.Succeeded,
				Outcomes:            agg.Outcomes,
				InputTokens:         agg.InputTokens,
				OutputTokens:        agg.OutputTokens,
				CacheReadTokens:     agg.CacheReadTokens,
				CacheCreationTokens: agg.CacheCreationTokens,
				CostMicroUSD:        agg.CostMicroUSD,
				ElapsedMilliseconds: agg.ElapsedMilliseconds,
			}
		}
	}
	if len(st.Monthly) > 0 {
		out.Monthly = make(map[string]MonthlyBucket, len(st.Monthly))
		for month, b := range st.Monthly {
			out.Monthly[month] = MonthlyBucket{
				Findings: b.Findings, Runs: b.Runs, CostMicroUSD: b.CostMicroUSD,
			}
		}
	}
	return out
}

// stamp renders a CRD time as RFC3339 UTC; zero times render empty (and are
// omitted by omitempty).
func stamp(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// stampPtr renders an optional CRD time.
func stampPtr(t *metav1.Time) string {
	if t == nil {
		return ""
	}
	return stamp(*t)
}
