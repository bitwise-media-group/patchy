// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command source-controller runs the Forge and Repository reconcilers. It
// validates forge credentials, pins each Repository's head SHA once,
// downloads the tarball archive at that SHA, and serves it from the artifact
// endpoint that agent pods fetch from without credentials.
package main

import (
	"os"

	"github.com/bitwise-media-group/patchy/internal/cli"
)

func main() {
	opts := cli.NewOptions()
	root := cli.NewControllerRoot("source-controller",
		"Receive security findings via webhooks and accumulate them into GitHub issues", opts)
	root.AddCommand(newServeCmd(opts))
	os.Exit(cli.Execute(root, opts.Log))
}
