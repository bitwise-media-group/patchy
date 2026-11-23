// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package agentrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bitwise-media-group/patchy/internal/envelope"
	"github.com/bitwise-media-group/patchy/internal/harness"
	"github.com/bitwise-media-group/patchy/internal/transcript"
	"github.com/bitwise-media-group/patchy/pkg/source"
)

// secretsPath is the leaked-secret handoff the init container copies in:
// present only for a secret finding, a JSON array of source.Location.
func (c Config) secretsPath() string { return filepath.Join(c.Workspace, "input", "secrets.json") }

// loadSecrets reads the leaked-secret handoff, when there is one. It marks
// the run as a secret finding (the prompts switch to their secret variants)
// and registers each located literal, sliced out of the workspace, for
// scrubbing. The pod already holds the repository, so it can read the
// secret; what this prevents is the run copying it into the transcript, the
// report or the envelope.
//
// A handoff that exists but cannot be read is fatal: running a secret
// finding without its scrub list would be the one unsafe outcome. A
// location whose file is gone (the secret was removed at HEAD) has nothing
// to slice and is skipped.
func (a *Agent) loadSecrets() error {
	raw, err := os.ReadFile(a.cfg.secretsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("secret handoff: %w", err)
	}
	var locs []source.Location
	if err := json.Unmarshal(raw, &locs); err != nil {
		return fmt.Errorf("secret handoff: %w", err)
	}
	a.secret = true
	for _, loc := range locs {
		a.scrub = append(a.scrub, sliceSecret(a.cfg.repoDir(), loc)...)
	}
	return nil
}

// sliceSecret returns the literals a location covers in the repository,
// longest first so Redact scrubs a value whole before its own prefix.
//
// Secret scanning reports 1-based byte columns. Whether its end column is
// inclusive is not something to bet a leak on, so both readings are
// registered: the longer one may carry one delimiter (a quote, say) past
// the value, and the shorter one catches the value wherever it appears
// without it. A multi-line value (a PEM key) also registers each of its
// lines, since a tool's output can show them one at a time. Without columns
// the whole trimmed line stands in.
func sliceSecret(repoDir string, loc source.Location) []string {
	rel := filepath.Clean(strings.TrimPrefix(filepath.FromSlash(loc.Path), string(filepath.Separator)))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(repoDir, rel))
	if err != nil || loc.StartLine < 1 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	end := max(loc.EndLine, loc.StartLine)
	if loc.StartLine > len(lines) || end > len(lines) {
		return nil
	}
	span := lines[loc.StartLine-1 : end]
	for i := range span {
		span[i] = strings.TrimSuffix(span[i], "\r")
	}

	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if loc.StartColumn < 1 || loc.EndColumn < 1 {
		for _, l := range span {
			add(l)
		}
		return longestFirst(out)
	}
	first, last := span[0], span[len(span)-1]
	start := min(loc.StartColumn-1, len(first))
	for _, endCol := range []int{loc.EndColumn, loc.EndColumn - 1} {
		stop := min(max(endCol, 0), len(last))
		if len(span) == 1 {
			if stop > start {
				add(first[start:stop])
			}
			continue
		}
		parts := append([]string{first[start:]}, span[1:len(span)-1]...)
		add(strings.Join(append(parts, last[:stop]), "\n"))
	}
	if len(span) > 1 {
		add(first[start:])
		for _, l := range span[1 : len(span)-1] {
			add(l)
		}
		add(last[:min(max(loc.EndColumn, 0), len(last))])
	}
	return longestFirst(out)
}

// longestFirst orders values longest first, stably.
func longestFirst(vals []string) []string {
	slices.SortStableFunc(vals, func(x, y string) int { return len(y) - len(x) })
	return vals
}

// scrubValues is every literal a stage's output must not carry: the
// transcript's credential set (harness keys, credential-named env, the
// broker token) plus the located secrets.
func (a *Agent) scrubValues(harnessID string) []string {
	if h, ok := harness.ByID(harnessID); ok {
		return credentialValues(h, a.scrub...)
	}
	return slices.Clone(a.scrub)
}

// redactInvestigation scrubs the free text of an analysis event: the
// report, the failure detail, and the dimension summaries.
func (a *Agent) redactInvestigation(ev *envelope.Investigation) {
	vals := transcript.Scrubbable(a.scrubValues(a.cfg.InvestigateHarness))
	if len(vals) == 0 {
		return
	}
	ev.ReportMarkdown = transcript.Redact(ev.ReportMarkdown, vals)
	ev.Detail = transcript.Redact(ev.Detail, vals)
	for _, d := range []*envelope.AnalysisResult{&ev.Exploitability, &ev.Likelihood, &ev.Impact} {
		d.Summary = transcript.Redact(d.Summary, vals)
	}
}

// redactRemediation scrubs the free text of a remediation event: the
// report, the failure detail, and the commit message. The changeset's file
// contents and patch are left alone: they are the change itself, and the
// removed line is already in the repository and its history.
func (a *Agent) redactRemediation(ev *envelope.Remediation) {
	vals := transcript.Scrubbable(a.scrubValues(a.cfg.RemediateHarness))
	if len(vals) == 0 {
		return
	}
	ev.ReportMarkdown = transcript.Redact(ev.ReportMarkdown, vals)
	ev.Detail = transcript.Redact(ev.Detail, vals)
	if ev.Changeset != nil {
		ev.Changeset.CommitMessage = transcript.Redact(ev.Changeset.CommitMessage, vals)
	}
}
