// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package provider is the pure logic behind brokered claude runners: which
// gateway environment a model provider (anthropic, bedrock, vertex, foundry)
// needs in the agent pod, and how canonical model ids translate to the
// provider-specific ids the claude CLI's --model flag expects there.
//
// Canonical ids ("anthropic/claude-sonnet-5") stay canonical everywhere
// controller-side — CRDs, allowlists, pricing; translation happens in-pod at
// agentrun's cliModel, fed by the PATCHY_MODEL_MAP this package renders. The
// per-provider derived defaults (Bedrock's region-prefixed ids, Vertex's bare
// ids) mean operators only write a model map for Foundry, whose deployment
// names cannot be derived, or to override a default.
package provider
