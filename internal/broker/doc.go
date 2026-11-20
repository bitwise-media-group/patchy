// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package broker is the egress credential broker's engine: a reconciler-less
// reverse proxy that agent pods address directly (base-URL env overrides
// point the claude CLI at it; a rendered config.toml model provider points
// the codex CLI at it) and that injects or signs the model credential
// outbound. It is what makes agent pods fully credential-less. The pod
// authenticates to the broker with an audience-bound projected
// ServiceAccount token (an identity document, not a capability), and the
// broker alone holds the Anthropic or OpenAI API key or the cloud workload
// identity that signs Bedrock/Vertex/Foundry/Azure OpenAI traffic.
//
// There is one route per upstream, keyed by path prefix (anthropic, bedrock,
// vertex, foundry for claude; openai, azure for codex). New brokered
// upstreams (forge-minted GitHub tokens, package registries) would be added
// as routes. Every request is authenticated via TokenReview before
// any upstream contact, audited as a single slog line (never bodies or
// headers), and streamed through with immediate flushing so SSE responses
// survive multi-minute runs.
package broker
