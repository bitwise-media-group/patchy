// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command egress-broker serves the egress credential broker: the reverse
// proxy agent pods send all model-API traffic through. It validates each
// caller's projected ServiceAccount token via TokenReview, then injects or
// signs the model credential outbound (Anthropic API key, Bedrock SigV4,
// Vertex OAuth, Foundry key/Entra) — which is what keeps agent pods fully
// credential-less. Not a controller: no reconcilers, no leases, and no
// Kubernetes access beyond TokenReview.
package main

import (
	"os"

	"github.com/bitwise-media-group/patchy/internal/cli"
)

func main() {
	opts := cli.NewOptions()
	root := cli.NewControllerRoot("egress-broker",
		"Serve the egress credential broker agent pods reach model providers through", opts)
	root.AddCommand(newServeCmd(opts))
	os.Exit(cli.Execute(root, opts.Log))
}
