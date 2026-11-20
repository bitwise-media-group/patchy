// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Command egress-broker serves the egress credential broker: the reverse
// proxy agent pods send all model-API traffic through. It validates each
// caller's projected ServiceAccount token via TokenReview, then injects or
// signs the model credential outbound (Anthropic key or token, Bedrock SigV4,
// Vertex OAuth, Foundry key/Entra, OpenAI key, Azure key/Entra). This is what
// keeps agent pods free of credentials. It is not a controller: no
// reconcilers, no leases, and no Kubernetes access beyond TokenReview.
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
