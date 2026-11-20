// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package provider is the pure logic behind brokered agent runners: which
// gateway environment a model provider needs in the agent pod, and how
// canonical model ids translate to the provider-specific ids the CLI's
// --model flag expects there. Two harnesses are brokered:
//
//   - claude (anthropic, bedrock, vertex, foundry), configured through the
//     claude CLI's own base-URL and skip-auth env;
//   - codex (openai, azure), configured through a rendered config.toml that
//     declares a custom model provider at the broker.
//
// Canonical ids ("anthropic/claude-sonnet-5") stay canonical everywhere
// controller-side (CRDs, allowlists, pricing). Translation happens in-pod at
// agentrun's cliModel, fed by the PATCHY_MODEL_MAP this package renders. The
// per-provider derived defaults (Bedrock's region-prefixed ids, Vertex's bare
// ids) mean operators only write a model map for Foundry or Azure, whose
// deployment names cannot be derived, or to override a default.
package provider
