// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command status-server serves the patchy status page: a web dashboard over
// the Finding state machine and the FindingRollup statistics, with SSO
// sign-in and RBAC-gated approve/suspend/resume actions. Findings and
// metrics both require an authorised identity; with no authentication
// configured the server serves nothing.
package main

import (
	"os"

	"github.com/bitwise-media-group/patchy/internal/cli"
)

func main() {
	opts := cli.NewOptions()
	root := cli.NewControllerRoot("status-server",
		"Serve the status page: findings review, human approvals, and rollup statistics", opts)
	root.AddCommand(newServeCmd(opts))
	os.Exit(cli.Execute(root, opts.Log))
}
