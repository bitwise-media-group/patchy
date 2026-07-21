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
	IssuePath          string
	ReportPath         string
	AllowedModels      []string
	MaxTurnsCeiling    int
	TokenBudgetCeiling int
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
