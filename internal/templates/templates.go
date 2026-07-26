// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package templates

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed *.md.tmpl
var files embed.FS

// tmpl parses every embedded template once; a parse failure is a programmer
// error caught by the package's golden tests, so panicking at init is right.
var tmpl = template.Must(template.New("").
	Funcs(template.FuncMap{"join": strings.Join}).
	ParseFS(files, "*.md.tmpl"))

func render(name string, data any) (string, error) {
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return b.String(), nil
}

// PRBody renders the pull-request body for a remediation branch; issue is
// the tracking issue number ("Fixes #issue" auto-links and auto-closes).
func PRBody(issue int, report string) (string, error) {
	return render("pr_body.md.tmpl", struct {
		Issue  int
		Report string
	}{issue, report})
}

// InvestigatePrompt is the data for the analysis-stage prompt (stage 1:
// exploitability/likelihood/impact plus the verdict).
type InvestigatePrompt struct {
	IssuePath     string
	ReportPath    string
	AllowedModels []string
	// MaxTurnsCeiling/TokenBudgetCeiling are the thresholds above which an
	// estimate needs human approval — NOT caps on the remediation.
	MaxTurnsCeiling    int
	TokenBudgetCeiling int
	// Calibration is how earlier estimates compared to reality; nil omits the
	// section (a cold start has nothing honest to say).
	Calibration *Calibration
}

// Calibration reports how previous remediations compared to what the
// investigation predicted, so the next estimate can correct for the observed
// skew. It is derived from a FindingRollup's remediation stage aggregate and
// carried to the pod as JSON — the runner has no Kubernetes access, so this
// travels as configuration like every other fact it needs.
type Calibration struct {
	// Scope describes where the figures came from, e.g. "owner/repo" or
	// "all repositories" when the per-repository history was too thin.
	Scope string `json:"scope,omitempty"`
	// Runs the averages are drawn from.
	Runs int64 `json:"runs"`
	// AvgPredictedTurns/AvgActualTurns and the token pair are whole-number
	// averages over those runs.
	AvgPredictedTurns        int64 `json:"avgPredictedTurns"`
	AvgActualTurns           int64 `json:"avgActualTurns"`
	AvgPredictedOutputTokens int64 `json:"avgPredictedOutputTokens"`
	AvgActualOutputTokens    int64 `json:"avgActualOutputTokens"`
}

// TurnSkew is how far actual turns ran over (positive) or under (negative)
// the prediction, as a percentage. Zero when there is no prediction to
// compare against.
func (c *Calibration) TurnSkew() int64 {
	return skewPercent(c.AvgPredictedTurns, c.AvgActualTurns)
}

// TokenSkew is the same measure for output tokens.
func (c *Calibration) TokenSkew() int64 {
	return skewPercent(c.AvgPredictedOutputTokens, c.AvgActualOutputTokens)
}

// skewPercent is actual ÷ predicted - 1, as a rounded percentage.
func skewPercent(predicted, actual int64) int64 {
	if predicted <= 0 {
		return 0
	}
	return (actual - predicted) * 100 / predicted
}

// RenderInvestigatePrompt renders the investigation prompt.
func RenderInvestigatePrompt(p InvestigatePrompt) (string, error) {
	return render("prompt_investigate.md.tmpl", p)
}

// RemediatePrompt is the data for the stage-2 (remediation) prompt.
type RemediatePrompt struct {
	IssuePath         string
	InvestigationPath string
	ReportPath        string
	CommitScriptPath  string
}

// RenderRemediatePrompt renders the remediation prompt.
func RenderRemediatePrompt(p RemediatePrompt) (string, error) {
	return render("prompt_remediate.md.tmpl", p)
}
