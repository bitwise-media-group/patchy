// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command context-controller reacts to newly opened Findings and enhances
// their context from first and third parties (CMDB ownership, associated
// infrastructure) before investigation picks them up.
package main

import (
	"os"

	"github.com/bitwise-media-group/patchy/internal/cli"
)

func main() {
	opts := cli.NewOptions()
	root := cli.NewControllerRoot("context-controller",
		"Enhance security-finding issues with ownership and infrastructure context", opts)
	root.AddCommand(newServeCmd(opts))
	os.Exit(cli.Execute(root, opts.Log))
}
