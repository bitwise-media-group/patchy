// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bitwise-media-group/patchy/cmd/patchy/internal/printer"
	"github.com/bitwise-media-group/patchy/internal/mirror"
	"github.com/bitwise-media-group/patchy/internal/mirror/spec"
)

// upgradeResults is the render fixture: a moved pin, a converged no-op,
// and a failure.
var upgradeResults = []mirror.UpgradeResult{
	{Name: "demo", Kind: "Chart", From: "1.0.0", To: "1.1.0", Changed: []string{"manifest.yaml", "images.lock.yaml"}},
	{Name: "steady", Kind: "Artifact", From: "2.0.0", To: "2.0.0"},
	{Name: "broken", Kind: "Chart", From: "1.0.0", To: "1.0.0", Err: "upstream gone"},
}

func TestRenderUpgradeResultsTable(t *testing.T) {
	out := &bytes.Buffer{}
	opts := &Options{Out: out, ErrOut: &bytes.Buffer{}}
	if err := renderUpgradeResults(opts, upgradeResults, printer.FormatTable); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"1.0.0 -> 1.1.0", "2 file(s) changed",
		"unchanged",
		"failed: upstream gone",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table lacks %q:\n%s", want, got)
		}
	}
	// A pin that did not move renders once, never as "2.0.0 -> 2.0.0".
	if strings.Contains(got, "2.0.0 -> 2.0.0") {
		t.Errorf("unmoved pin rendered as movement:\n%s", got)
	}
}

func TestRenderUpgradeResultsMarkdown(t *testing.T) {
	out := &bytes.Buffer{}
	opts := &Options{Out: out, ErrOut: &bytes.Buffer{}}
	if err := renderUpgradeResults(opts, upgradeResults, printer.FormatMarkdown); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"### `demo`",
		"- `1.0.0 -> 1.1.0` 2 file(s) changed",
		"### `steady`",
		"- `2.0.0` unchanged",
		"### `broken`",
		"**error:** upstream gone",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown lacks %q:\n%s", want, got)
		}
	}
}

func TestRenderUpdatePlan(t *testing.T) {
	plan := &mirror.UpdatePlan{Groups: []mirror.UpdateGroup{{
		Group:  "pair",
		Target: "1.1.0",
		Members: []mirror.MemberPlan{
			{Name: "demo", Kind: "Chart", Current: "1.0.0", Target: "1.1.0", TrackedImages: []mirror.TrackPlan{
				{Image: "reg.example.test/apps/runner", Current: "2.320.0", Selected: "2.321.0"},
				// An unmoved pick must not clutter the summary.
				{Image: "reg.example.test/apps/steady", Current: "1.0.0", Selected: "1.0.0"},
			}},
			{Name: "twin", Kind: "Chart", Current: "1.1.0", Target: "1.1.0"},
		},
	}}}

	t.Run("table", func(t *testing.T) {
		out := &bytes.Buffer{}
		opts := &Options{Out: out, ErrOut: &bytes.Buffer{}}
		if err := renderUpdatePlan(opts, plan, printer.FormatTable); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		for _, want := range []string{"pair", "demo", "1.0.0", "1.1.0",
			"reg.example.test/apps/runner 2.320.0->2.321.0"} {
			if !strings.Contains(got, want) {
				t.Errorf("table lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "steady") {
			t.Errorf("unmoved pick rendered:\n%s", got)
		}
	})

	t.Run("markdown", func(t *testing.T) {
		out := &bytes.Buffer{}
		opts := &Options{Out: out, ErrOut: &bytes.Buffer{}}
		if err := renderUpdatePlan(opts, plan, printer.FormatMarkdown); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		for _, want := range []string{
			"### `pair`",
			"- `demo` 1.0.0 -> 1.1.0 (reg.example.test/apps/runner 2.320.0->2.321.0)",
			"- `twin` 1.1.0 -> 1.1.0",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("markdown lacks %q:\n%s", want, got)
			}
		}
	})

	t.Run("empty plan", func(t *testing.T) {
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		opts := &Options{Out: out, ErrOut: errOut}
		if err := renderUpdatePlan(opts, &mirror.UpdatePlan{}, printer.FormatTable); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), "everything is current") {
			t.Errorf("stdout = %q, stderr = %q", out.String(), errOut.String())
		}

		out.Reset()
		if err := renderUpdatePlan(opts, &mirror.UpdatePlan{}, printer.FormatMarkdown); err != nil {
			t.Fatal(err)
		}
		// The markdown summary goes to a job summary: the all-clear must
		// land on stdout, not stderr narration.
		if !strings.Contains(out.String(), "Everything is current.") {
			t.Errorf("markdown = %q", out.String())
		}
	})
}

// TestUpgradeTargetsExplicit pins the --to fan-out: every selected entry
// maps to the same version, without consulting the update plan (a nil
// engine proves no plan is computed).
func TestUpgradeTargetsExplicit(t *testing.T) {
	entries := []spec.Entry{{Name: "a"}, {Name: "b"}}
	targets, err := upgradeTargets(&cobra.Command{}, nil, entries, "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets["a"] != "2.0.0" || targets["b"] != "2.0.0" {
		t.Errorf("targets = %v", targets)
	}
}
