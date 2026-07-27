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
	"strings"
	"time"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/harness"
	"github.com/bitwise-media-group/patchy/internal/model"
	"github.com/bitwise-media-group/patchy/internal/report"
	"github.com/bitwise-media-group/patchy/internal/runner"
	"github.com/bitwise-media-group/patchy/internal/templates"
	"github.com/bitwise-media-group/patchy/internal/transcript"
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

// remediationParams are the clamped stage-2 knobs. The model and harness are
// not here: the controller resolves them and passes them per-Job, so the pod
// runs the model its runner image was built for.
type remediationParams struct {
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
		Model:   a.cfg.RemediateModel,
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

	onLine, _ := a.observe(h, params.budget)
	res, runErr := a.exec.Run(ctx, h.PromptSpec(a.cfg.repoDir(), harness.PromptRequest{
		Prompt:    prompt,
		Model:     cliModel(a.cfg.RemediateModel, a.cfg.RemediateHarness),
		MaxTurns:  params.maxTurns,
		Sandbox:   harness.SandboxWorkspaceWrite,
		SessionID: a.newSessionID(),
		AddDirs:   []string{a.cfg.Workspace},
	}), a.cfg.RemediateTimeout, onLine)
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
	// Raw, frontmatter included: the report is the machine contract as well
	// as the human artifact. Presentation seams strip the fence before
	// rendering (report.StripFrontmatter).
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

// observe builds the runner's per-line observer: the transcript recorder and
// the output-token budget kill switch, over the one pass the runner makes.
// Either half may be absent — a harness that cannot report usage, a budget of
// zero, a harness that cannot transcribe — and when both are, the observer is
// nil and the runner does no per-line work at all.
//
// Recording happens before the budget check so the turn that tripped the limit
// is in the transcript that explains why the run stopped.
func (a *Agent) observe(h harness.Harness, budget int) (func([]byte) (bool, string), *transcript.Recorder) {
	rec := a.recorder(h)
	watch := budgetWatcher(h, budget)
	if rec == nil && watch == nil {
		return nil, nil
	}

	turns, _ := h.(harness.TurnScanner)
	return func(line []byte) (bool, string) {
		if rec != nil && turns != nil {
			rec.RecordAll(turns.ScanTurns(line))
		}
		if watch == nil {
			return false, ""
		}
		abort, reason := watch(line)
		if abort && rec != nil {
			rec.Notice("%s", reason)
		}
		return abort, reason
	}, rec
}

// recorder builds the transcript recorder for a run, or nil when the harness
// cannot project its stream onto the turn vocabulary.
func (a *Agent) recorder(h harness.Harness) *transcript.Recorder {
	if _, ok := h.(harness.TurnScanner); !ok {
		a.cfg.Log.Info("harness cannot transcribe; no transcript for this run", "harness", h.ID())
		return nil
	}
	return transcript.NewRecorder(a.cfg.transcriptLimits(), credentialValues(h), a.emitTurn)
}

// credentialValues returns the literal secret values to scrub from a
// transcript: the harness's own credential variables, plus anything else in
// the environment named like a credential. The pod carries the model API key,
// and a tool result that dumps the environment would otherwise put it in a
// ConfigMap the status page serves.
//
// The harness keys are the authoritative half — runnercfg validates the
// injected credential's variable against exactly that set at startup, so the
// model key is always covered. The name heuristic is defence in depth for
// whatever else an operator forwards.
func credentialValues(h harness.Harness) []string {
	wanted := make(map[string]bool, len(h.EnvKeys()))
	for _, k := range h.EnvKeys() {
		wanted[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		if wanted[name] || credentialNamed(name) {
			out = append(out, value)
		}
	}
	return out
}

// credentialWords mark an environment variable as holding a secret.
var credentialWords = []string{"TOKEN", "KEY", "SECRET", "PASSWORD", "CREDENTIALS"}

// credentialNamed reports whether an environment variable's name marks it as
// a credential. It over-matches on purpose — a redacted non-secret costs a
// reader nothing, a leaked secret costs a rotation — but skips patchy's own
// PATCHY_* configuration, which holds no credentials and does hold numbers
// (token budgets) common enough to redact real transcript text by accident.
func credentialNamed(name string) bool {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "PATCHY_") {
		return false
	}
	for _, word := range credentialWords {
		if strings.Contains(upper, word) {
			return true
		}
	}
	return false
}

// budgetWatcher builds the per-line observer enforcing the cumulative
// output-token budget, when the harness can report usage.
func budgetWatcher(h harness.Harness, budget int) func([]byte) (bool, string) {
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
		IssuePath:         a.cfg.issuePath(),
		ReportPath:        a.cfg.investigationPath(),
		AllowedModels:     a.cfg.ModelAllowlist,
		AutoMaxTurns:      a.cfg.RemediateAutoMaxTurns,
		AutoTokenBudget:   a.cfg.RemediateAutoTokenBudget,
		ManualMaxTurns:    a.cfg.RemediateManualMaxTurns,
		ManualTokenBudget: a.cfg.RemediateManualTokenBudget,
		Calibration:       a.cfg.Calibration,
	})
	if err != nil {
		ev.Outcome = envelope.OutcomeRuntimeError
		ev.Detail = err.Error()
		return ev
	}

	onLine, _ := a.observe(h, a.cfg.InvestigateTokenBudget)
	res, runErr := a.exec.Run(ctx, h.PromptSpec(a.cfg.repoDir(), harness.PromptRequest{
		Prompt:    prompt,
		Model:     cliModel(a.cfg.InvestigateModel, a.cfg.InvestigateHarness),
		MaxTurns:  a.cfg.InvestigateMaxTurns,
		Sandbox:   harness.SandboxReadOnly,
		SessionID: a.newSessionID(),
		AddDirs:   []string{a.cfg.Workspace},
	}), a.cfg.InvestigateTimeout, onLine)
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
	// Raw, frontmatter included: the remediation stage re-parses this exact
	// text as its investigation.md input (remediationInput), so the fence
	// must survive the round-trip through Finding status.
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
	if inv.Recommendation == report.RecommendRemediate {
		// The agent's raw suggested (canonical) model rides the envelope; the
		// remediation spawner clamps it to the allowlist and resolves the
		// harness that runs it. The turn and token figures ride it verbatim,
		// unclamped: they are the agent's ESTIMATE of the fix's cost, and
		// clamping them would destroy the very signal the approval gate and
		// the calibration averages are reading.
		ev.RemediationModel = inv.Model
		ev.EstimatedMaxTurns = inv.MaxTurns
		ev.EstimatedTokenBudget = inv.TokenBudget
		ev.HoldReasons = a.holdReasons(inv)
	}
	ev.AwaitApproval = len(ev.HoldReasons) > 0
	return ev
}

// holdReasons decides why — if at all — a remediate verdict must wait for a
// human before it runs. An estimate above the automated budget is a hold
// rather than a clamp: that budget is the line past which spend needs
// authorizing, and approving grants the estimate rather than merely
// permitting the automated amount. Confidence is NOT judged here; the
// controller owns that threshold.
func (a *Agent) holdReasons(inv *report.Investigation) []envelope.HoldReason {
	var reasons []envelope.HoldReason
	if inv.BreakingChangeAvailable {
		reasons = append(reasons, envelope.HoldBreakingChangeAvailable)
	}
	if inv.MaxTurns > a.cfg.RemediateAutoMaxTurns {
		reasons = append(reasons, envelope.HoldExceedsAutomatedTurns)
	}
	if inv.TokenBudget > a.cfg.RemediateAutoTokenBudget {
		reasons = append(reasons, envelope.HoldExceedsAutomatedTokens)
	}
	return reasons
}

// remediationInput validates that the controller-provided analysis is present
// and parseable, and resolves what this run may spend.
//
// The investigation's numbers are NOT read as limits. They are an estimate:
// the controller already compared them to the automated budget, held it for
// approval if they exceeded it, and resolved the grant it passed per-Job. A
// low estimate must never starve the run — that is exactly the failure this
// separation exists to prevent. The model and harness are likewise not read
// from here: the controller resolved them, so the pod runs what its runner
// image was built for.
func (a *Agent) remediationInput() (remediationParams, error) {
	raw, err := os.ReadFile(a.cfg.inputInvestigation())
	if err != nil {
		return remediationParams{}, fmt.Errorf("input analysis: %w", err)
	}
	if _, err := report.ParseInvestigation(raw); err != nil {
		return remediationParams{}, err
	}
	maxTurns, budget := a.grant()
	return remediationParams{maxTurns: maxTurns, budget: budget}, nil
}

// grant resolves the remediation's spend. The controller's per-Job grant wins
// when present; otherwise the run gets the automated budget, which is the
// floor every remediation is entitled to. Either way the manual budget binds
// — the runner is what actually spends the money, so it re-checks rather than
// trusting the grant it was handed.
func (a *Agent) grant() (maxTurns, budget int) {
	maxTurns, budget = a.cfg.GrantedMaxTurns, a.cfg.GrantedTokenBudget
	if maxTurns < a.cfg.RemediateAutoMaxTurns {
		if maxTurns > 0 {
			a.cfg.Log.Warn("granted max_turns below the automated budget; using the automated budget",
				"granted", maxTurns, "automated", a.cfg.RemediateAutoMaxTurns)
		}
		maxTurns = a.cfg.RemediateAutoMaxTurns
	}
	if budget < a.cfg.RemediateAutoTokenBudget {
		if budget > 0 {
			a.cfg.Log.Warn("granted token_budget below the automated budget; using the automated budget",
				"granted", budget, "automated", a.cfg.RemediateAutoTokenBudget)
		}
		budget = a.cfg.RemediateAutoTokenBudget
	}
	if maxTurns > a.cfg.RemediateManualMaxTurns {
		a.cfg.Log.Warn("granted max_turns clamped to the manual budget",
			"granted", maxTurns, "manual", a.cfg.RemediateManualMaxTurns)
		maxTurns = a.cfg.RemediateManualMaxTurns
	}
	if budget > a.cfg.RemediateManualTokenBudget {
		a.cfg.Log.Warn("granted token_budget clamped to the manual budget",
			"granted", budget, "manual", a.cfg.RemediateManualTokenBudget)
		budget = a.cfg.RemediateManualTokenBudget
	}
	return maxTurns, budget
}

// cliModel translates a canonical model id into the CLI model-id the given
// harness's --model flag expects. The controller validated the model against
// the registry before launching, so this defends against an unknown id by
// falling back to the bare (provider-stripped) id — which is also what the
// fake harness receives and ignores.
func cliModel(canonical, harnessID string) string {
	if m, ok := model.ModelByID(model.Builtins(), canonical); ok {
		if id, ok := m.CLIModelID(harnessID); ok {
			return id
		}
	}
	if _, id, ok := strings.Cut(canonical, "/"); ok {
		return id
	}
	return canonical
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
// A run that exhausted its turn or token limit is reported as budget
// exceeded rather than a generic runtime error — it names the cause, and it
// is the same outcome the runner's own kill switch raises.
func stageOutcome(h harness.Harness, res runner.Result, runErr error) (envelope.Outcome, string) {
	if runErr != nil {
		return envelope.OutcomeRuntimeError, runErr.Error()
	}
	if res.TimedOut {
		return envelope.OutcomeTimeout, fmt.Sprintf("stage timed out after %s", res.Elapsed.Round(time.Second))
	}
	if msg := h.RuntimeError(res.Stdout, res.ExitCode, res.TimedOut); msg != "" {
		if b, ok := h.(harness.BudgetReporter); ok && b.Exhausted(res.Stdout) {
			return envelope.OutcomeBudgetExceeded, msg
		}
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

// emitTurn writes one transcript turn to the runner's stdout. Turns share the
// stream with envelope events under their own prefix; the owning controller
// scans for both and the status server follows the log live for the turns.
//
// A turn that cannot be written is logged and dropped: the transcript is
// observability, and losing a line of it must never fail the run producing it.
func (a *Agent) emitTurn(t transcript.Turn) {
	line, err := transcript.Encode(t)
	if err != nil {
		a.cfg.Log.Error("encode transcript turn", "error", err)
		return
	}
	if _, err := fmt.Fprintln(a.cfg.Out, line); err != nil {
		a.cfg.Log.Error("emit transcript turn", "error", err)
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
