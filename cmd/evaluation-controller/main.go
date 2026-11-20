// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command evaluation-controller executes remote skill evaluations submitted
// by evolve: it hosts the bearer-authenticated HTTP API (workspace upload,
// submission, SSE monitoring, cancellation), expands each submission into
// EvaluationUnit children, schedules them through the sandboxed agent-Job
// machinery with bounded concurrency, collects each pod's result stream onto
// CR statuses and per-unit results ConfigMaps, and expires finished
// evaluations on a TTL. It is optional: deployments that never submit remote
// evaluations do not run it.
package main

import (
	"os"

	"github.com/bitwise-media-group/patchy/internal/cli"
)

func main() {
	opts := cli.NewOptions()
	root := cli.NewControllerRoot("evaluation-controller",
		"Run remote skill evaluations submitted by evolve in sandboxed agent jobs", opts)
	root.AddCommand(newServeCmd(opts))
	os.Exit(cli.Execute(root, opts.Log))
}
