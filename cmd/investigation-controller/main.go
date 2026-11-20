// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command investigation-controller turns enhanced Findings into analysis
// agent runs: it gates on the accumulation window and minimum age, resolves
// forge coverage, creates the Repository that source-controller turns into a
// SHA-pinned artifact, runs the investigation agent Job (bounded
// concurrency, severity-priority order),
// and routes each verdict: queue for remediation, hold for approval,
// dismiss, or hand to humans.
package main

import (
	"os"

	"github.com/bitwise-media-group/patchy/internal/cli"
)

func main() {
	opts := cli.NewOptions()
	root := cli.NewControllerRoot("investigation-controller",
		"Run analysis agent jobs over enhanced findings and route their verdicts", opts)
	root.AddCommand(newServeCmd(opts))
	os.Exit(cli.Execute(root, opts.Log))
}
