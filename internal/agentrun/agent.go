// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/harness"
	"github.com/bitwise-media-group/patchy/internal/report"
	"github.com/bitwise-media-group/patchy/internal/runner"
	"github.com/bitwise-media-group/patchy/internal/templates"
)

// Tool policies per stage. Investigation reads the tree read-only (plus the
// one Write for its report); remediation edits freely — network is denied at
// the pod layer, not the tool layer.
var (
	investigateAllowedTools = []string{
		"Read", "Glob", "Grep", "Write",
		"Bash(git log:*)", "Bash(git show:*)", "Bash(git blame:*)", "Bash(git diff:*)",
	}
	investigateDisallowedTools = []string{"WebFetch", "WebSearch", "Task"}
	remediateAllowedTools      = []string{"Read", "Glob", "Grep", "Edit", "Write", "NotebookEdit", "Bash"}
	remediateDisallowedTools   = []string{"WebFetch", "WebSearch"}
)

// Executor runs harness command specs; *runner.Exec satisfies it, tests
// fake it.
type Executor interface {
	Run(ctx context.Context, spec runner.CommandSpec, timeout time.Duration,
		onLine func([]byte) (bool, string)) (runner.Result, error)
}

// Agent drives the stages.
type Agent struct {
	cfg  Config
	exec Executor
	// newSessionID is replaceable for tests.
	newSessionID func() string
}

// New builds an Agent.
func New(cfg Config, exec Executor) *Agent {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Agent{cfg: cfg, exec: exec, newSessionID: sessionID}
}

// remediationParams are the clamped stage-2 knobs.
type remediationParams struct {
	model    string
	maxTurns int
	budget   int
}

// Run executes the configured phase. It returns an error only for fatal,
// before-any-stage failures (also emitted as a fatal event); stage outcomes
// — including failed stages — are envelope events with a nil return, so the
// controller, not the Job status, routes the issue.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.prepare(); err != nil {
		a.emit(envelope.Event{Type: envelope.TypeFatal, Error: err.Error()})
		return err
	}

	if a.cfg.Phase == PhaseInvestigate {
		// The analysis stage: one event, no continuation — the controller
		// routes on the verdict.
		ev := a.investigate(ctx)
		a.emit(envelope.Event{Type: envelope.TypeInvestigation, Investigation: ev})
		return nil
	}

	// The controller supplies the analysis this run executes; thresholds and
	// holds were already applied controller-side.
	params, err := a.remediationInput()
	if err != nil {
		a.emit(envelope.Event{Type: envelope.TypeFatal, Error: err.Error()})
		return err
	}
	rev := a.remediate(ctx, params)
	a.emit(envelope.Event{Type: envelope.TypeRemediation, Remediation: rev})
	return nil
}

// prepare validates the workspace the controller assembled and creates the
// output directories.
func (a *Agent) prepare() error {
	if _, err := os.Stat(filepath.Join(a.cfg.repoDir(), ".git")); err != nil {
		return fmt.Errorf("workspace: repository clone missing: %w", err)
	}
	if _, err := os.Stat(a.cfg.issuePath()); err != nil {
		return fmt.Errorf("workspace: issue handoff missing: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(a.cfg.investigationPath()), 0o755); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	return nil
}

// remediate runs stage 2 and packages the changeset.
func (a *Agent) remediate(ctx context.Context, params remediationParams) *envelope.Remediation {
	ev := &envelope.Remediation{Stage: envelope.Stage{
		Harness: a.cfg.RemediateHarness,
		Model:   params.model,
	}}

	h, ok := harness.ByID(a.cfg.RemediateHarness)
	if !ok {
		ev.Outcome = envelope.OutcomeRuntimeError
		ev.Detail = fmt.Sprintf("unknown harness %q", a.cfg.RemediateHarness)
		return ev
	}
	if err := ensureIdentity(ctx, a.cfg.repoDir()); err != nil {
		ev.Outcome = envelope.OutcomeRuntimeError
		ev.Detail = err.Error()
		return ev
	}
	// HEAD at startup is the pinned base the init container fetched; the
	// changeset is diffed against it and the pushed commit parents it.
	baseSHA, err := headSHA(ctx, a.cfg.repoDir())
	if err != nil {
		ev.Outcome = envelope.OutcomeRuntimeError
		ev.Detail = err.Error()
		return ev
	}
	if err := checkoutBranch(ctx, a.cfg.repoDir(), a.cfg.branch()); err != nil {
		ev.Outcome = envelope.OutcomeRuntimeError
		ev.Detail = err.Error()
		return ev
	}

	prompt, err := templates.RenderRemediatePrompt(templates.RemediatePrompt{
		IssuePath:         a.cfg.issuePath(),
		InvestigationPath: a.cfg.inputInvestigation(),
		ReportPath:        a.cfg.remediationPath(),
		CommitScriptPath:  a.cfg.commitScript(),
	})
	if err != nil {
		ev.Outcome = envelope.OutcomeRuntimeError
		ev.Detail = err.Error()
		return ev
	}

	res, runErr := a.exec.Run(ctx, h.PromptSpec(a.cfg.repoDir(), harness.PromptRequest{
		Prompt:          prompt,
		Model:           params.model,
		MaxTurns:        params.maxTurns,
		AllowedTools:    remediateAllowedTools,
		DisallowedTools: remediateDisallowedTools,
		SessionID:       a.newSessionID(),
		AddDirs:         []string{a.cfg.Workspace},
	}), a.cfg.RemediateTimeout, a.budgetWatcher(h, params.budget))
	a.fillStage(&ev.Stage, h, res)

	if res.Aborted {
		ev.Outcome = envelope.OutcomeBudgetExceeded
		ev.Detail = res.AbortReason
		return ev
	}
	if outcome, detail := stageOutcome(h, res, runErr); outcome != envelope.OutcomeOK {
		ev.Outcome, ev.Detail = outcome, detail
		return ev
	}

	raw, err := os.ReadFile(a.cfg.remediationPath())
	if err != nil {
		ev.Outcome = envelope.OutcomeReportMissing
		ev.Detail = err.Error()
		return ev
	}
	rem, err := report.ParseRemediation(raw)
	if err != nil {
		ev.Outcome = envelope.OutcomeReportInvalid
		ev.Detail = err.Error()
		return ev
	}
	ev.ReportMarkdown = string(raw)
	ev.Confidence = *rem.Confidence
	ev.Outcome = envelope.OutcomeOK

	if !*rem.Success {
		return ev
	}
	// The agent claims success; the repository decides. commit.sh must run
	// cleanly and leave real commits, else the claim is downgraded.
	if outcome, detail := a.packageChangeset(ctx, baseSHA, ev); outcome != envelope.OutcomeOK {
		ev.Outcome, ev.Detail = outcome, detail
		return ev
	}
	ev.Success = true
	ev.Branch = a.cfg.branch()
	return ev
}

// budgetWatcher builds the runner's per-line observer enforcing the
// cumulative output-token budget, when the harness can report usage.
func (a *Agent) budgetWatcher(h harness.Harness, budget int) func([]byte) (bool, string) {
	scanner, ok := h.(harness.UsageScanner)
	if !ok || budget <= 0 {
		return nil
	}
	total := 0
	return func(line []byte) (bool, string) {
		n, ok := scanner.ScanUsage(line)
		if !ok {
			return false, ""
		}
		total += n
		if total > budget {
			return true, fmt.Sprintf("output token budget exceeded (%d > %d)", total, budget)
		}
		return false, ""
	}
}

// investigate runs the analysis stage and folds everything into the event
// payload. It parses the investigation contract and never decides
// continuation — the controller routes on the verdict.
func (a *Agent) investigate(ctx context.Context) *envelope.Investigation {
	ev := &envelope.Investigation{Stage: envelope.Stage{
		Harness: a.cfg.InvestigateHarness,
		Model:   a.cfg.InvestigateModel,
	}}

	h, ok := harness.ByID(a.cfg.InvestigateHarness)
	if !ok {
		ev.Outcome = envelope.OutcomeRuntimeError
		ev.Detail = fmt.Sprintf("unknown harness %q", a.cfg.InvestigateHarness)
		return ev
	}
	prompt, err := templates.RenderInvestigatePrompt(templates.InvestigatePrompt{
		IssuePath:          a.cfg.issuePath(),
		ReportPath:         a.cfg.investigationPath(),
		AllowedModels:      a.cfg.ModelAllowlist,
		MaxTurnsCeiling:    a.cfg.RemediateMaxTurns,
		TokenBudgetCeiling: a.cfg.RemediateTokenBudget,
	})
	if err != nil {
		ev.Outcome = envelope.OutcomeRuntimeError
		ev.Detail = err.Error()
		return ev
	}

	res, runErr := a.exec.Run(ctx, h.PromptSpec(a.cfg.repoDir(), harness.PromptRequest{
		Prompt:          prompt,
		Model:           a.cfg.InvestigateModel,
		MaxTurns:        a.cfg.InvestigateMaxTurns,
		AllowedTools:    investigateAllowedTools,
		DisallowedTools: investigateDisallowedTools,
		SessionID:       a.newSessionID(),
		AddDirs:         []string{a.cfg.Workspace},
	}), a.cfg.InvestigateTimeout, a.budgetWatcher(h, a.cfg.InvestigateTokenBudget))
	a.fillStage(&ev.Stage, h, res)

	if res.Aborted {
		ev.Outcome = envelope.OutcomeBudgetExceeded
		ev.Detail = res.AbortReason
		return ev
	}
	if outcome, detail := stageOutcome(h, res, runErr); outcome != envelope.OutcomeOK {
		ev.Outcome, ev.Detail = outcome, detail
		return ev
	}

	raw, err := os.ReadFile(a.cfg.investigationPath())
	if err != nil {
		ev.Outcome = envelope.OutcomeReportMissing
		ev.Detail = err.Error()
		return ev
	}
	inv, err := report.ParseInvestigation(raw)
	if err != nil {
		ev.Outcome = envelope.OutcomeReportInvalid
		ev.Detail = err.Error()
		return ev
	}

	ev.Outcome = envelope.OutcomeOK
	ev.ReportMarkdown = string(raw)
	ev.Exploitability = envelope.AnalysisResult{
		Rating: string(inv.Exploitability.Rating), Summary: inv.Exploitability.Summary,
	}
	ev.Likelihood = envelope.AnalysisResult{Rating: string(inv.Likelihood.Rating), Summary: inv.Likelihood.Summary}
	ev.Impact = envelope.AnalysisResult{Rating: string(inv.Impact.Rating), Summary: inv.Impact.Summary}
	ev.Recommendation = string(inv.Recommendation)
	ev.Priority = string(inv.Priority)
	ev.Severity = string(inv.Severity)
	ev.Confidence = *inv.Confidence
	ev.AwaitApproval = inv.Recommendation == report.RecommendRemediate && inv.BreakingChangeAvailable
	if inv.Recommendation == report.RecommendRemediate {
		params := a.clampInvestigation(inv)
		ev.RemediationModel = params.model
		ev.MaxTurns = params.maxTurns
		ev.TokenBudget = params.budget
	}
	return ev
}

// remediationInput reads the controller-provided investigation.md and clamps
// its suggested stage-2 parameters.
func (a *Agent) remediationInput() (remediationParams, error) {
	raw, err := os.ReadFile(a.cfg.inputInvestigation())
	if err != nil {
		return remediationParams{}, fmt.Errorf("input analysis: %w", err)
	}
	inv, err := report.ParseInvestigation(raw)
	if err != nil {
		return remediationParams{}, err
	}
	return a.clampInvestigation(inv), nil
}

// clampInvestigation holds the investigation's suggested stage-2 parameters
// to the configured ceilings/allowlist, logging every correction.
func (a *Agent) clampInvestigation(inv *report.Investigation) remediationParams {
	p := remediationParams{model: inv.Model, maxTurns: inv.MaxTurns, budget: inv.TokenBudget}
	if !slices.Contains(a.cfg.ModelAllowlist, p.model) {
		a.cfg.Log.Warn("investigation model not allowlisted; using default",
			"suggested", p.model, "default", a.cfg.RemediateModel)
		p.model = a.cfg.RemediateModel
	}
	if p.maxTurns < 1 || p.maxTurns > a.cfg.RemediateMaxTurns {
		a.cfg.Log.Warn("investigation max_turns clamped",
			"suggested", p.maxTurns, "ceiling", a.cfg.RemediateMaxTurns)
		p.maxTurns = a.cfg.RemediateMaxTurns
	}
	if p.budget < 1 || p.budget > a.cfg.RemediateTokenBudget {
		a.cfg.Log.Warn("investigation token_budget clamped",
			"suggested", p.budget, "ceiling", a.cfg.RemediateTokenBudget)
		p.budget = a.cfg.RemediateTokenBudget
	}
	return p
}

// packageChangeset runs commit.sh, verifies the repository state, and
// expresses base..branch as the changeset the controller pushes via the
// GitHub API.
func (a *Agent) packageChangeset(ctx context.Context, baseSHA string,
	ev *envelope.Remediation) (envelope.Outcome, string) {
	script := a.cfg.commitScript()
	if _, err := os.Stat(script); err != nil {
		return envelope.OutcomeCommitFailed, "commit.sh missing despite success report"
	}
	if out, err := runScript(ctx, a.cfg.repoDir(), script); err != nil {
		return envelope.OutcomeCommitFailed, fmt.Sprintf("commit.sh failed: %v: %s", err, out)
	}
	if err := verifyCommitted(ctx, a.cfg.repoDir(), baseSHA, a.cfg.branch()); err != nil {
		return envelope.OutcomeCommitFailed, err.Error()
	}
	cs, err := buildChangeset(ctx, a.cfg.repoDir(), baseSHA, a.cfg.branch(), a.cfg.ChangesetMaxBytes)
	if err == nil && a.cfg.BaseSHA != "" {
		// Artifact mode: the local base is synthetic; the push parents the
		// controller-resolved remote SHA.
		cs.BaseSHA = a.cfg.BaseSHA
	}
	if err != nil {
		if errors.Is(err, errChangesetTooLarge) {
			return envelope.OutcomeChangesetTooLarge, err.Error()
		}
		return envelope.OutcomeCommitFailed, err.Error()
	}
	ev.Changeset = cs
	return envelope.OutcomeOK, ""
}

// fillStage copies the harness accounting into the stage payload.
func (a *Agent) fillStage(st *envelope.Stage, h harness.Harness, res runner.Result) {
	st.ElapsedSeconds = res.Elapsed.Seconds()
	ar, ok := h.ParseResult(res.Stdout)
	if !ok {
		return
	}
	st.SessionID = ar.SessionID
	st.NumTurns = ar.NumTurns
	if u := ar.Usage; u != nil {
		st.Usage = envelope.Usage{
			InputTokens:         deref(u.InputTokens),
			OutputTokens:        deref(u.OutputTokens),
			CacheReadTokens:     deref(u.CacheReadTokens),
			CacheCreationTokens: deref(u.CacheCreationTokens),
			CostUSD:             deref(u.CostUSD),
		}
	}
}

// stageOutcome folds run error, timeout, and the harness's runtime-error
// gate into an outcome; OK means the stage's report can be trusted to exist.
func stageOutcome(h harness.Harness, res runner.Result, runErr error) (envelope.Outcome, string) {
	if runErr != nil {
		return envelope.OutcomeRuntimeError, runErr.Error()
	}
	if res.TimedOut {
		return envelope.OutcomeTimeout, fmt.Sprintf("stage timed out after %s", res.Elapsed.Round(time.Second))
	}
	if msg := h.RuntimeError(res.Stdout, res.ExitCode, res.TimedOut); msg != "" {
		return envelope.OutcomeRuntimeError, msg
	}
	return envelope.OutcomeOK, ""
}

// emit writes one envelope event to the runner's stdout.
func (a *Agent) emit(e envelope.Event) {
	e.Repo, e.Finding = a.cfg.Repo, a.cfg.Finding
	line, err := e.Encode()
	if err != nil {
		a.cfg.Log.Error("encode envelope event", "error", err)
		return
	}
	if _, err := fmt.Fprintln(a.cfg.Out, line); err != nil {
		a.cfg.Log.Error("emit envelope event", "error", err)
	}
}

// sessionID returns a random UUIDv4 so the session identifier exists even
// if the harness crashes before reporting one.
func sessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
