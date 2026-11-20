// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command remediation-controller admits approved and revived Findings to the
// remediation queue, runs the remediation coding agent in an ephemeral
// Kubernetes Job in priority order, and pushes the agent's changeset and opens
// the pull request through the forge write path (the only write credential).
// It also hosts the rollup statistics and finding TTL loop.
package main

import (
	"os"

	"github.com/bitwise-media-group/patchy/internal/cli"
)

func main() {
	opts := cli.NewOptions()
	root := cli.NewControllerRoot("remediation-controller",
		"Run coding-agent classification/remediation Jobs and apply their GitHub side effects", opts)
	root.AddCommand(newServeCmd(opts))
	os.Exit(cli.Execute(root, opts.Log))
}
