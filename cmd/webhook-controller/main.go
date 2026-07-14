// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command webhook-controller is the single internet-facing entry point for the
// GitHub App's one webhook URL: it validates each delivery's HMAC signature
// and routes it to the cluster-internal controllers that consume its event
// type.
// It holds no GitHub credential — only the shared webhook secret, which
// cannot mint tokens — so the credentialed controllers never face the
// internet directly.
package main

import (
	"os"

	"github.com/bitwise-media-group/patchy/internal/cli"
)

func main() {
	opts := cli.NewOptions()
	root := cli.NewControllerRoot("webhook-controller",
		"Validate GitHub webhook deliveries and route them to the controllers", opts)
	root.AddCommand(newServeCmd(opts))
	os.Exit(cli.Execute(root, opts.Log))
}
