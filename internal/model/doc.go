// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package model defines the model vendors (Anthropic, OpenAI) and their
// canonical models: provider-qualified ids, the harnesses each model can be
// driven by, and per-token pricing.
//
// It is a leaf domain package — it imports no other internal package — so both
// the controllers and the in-pod agent-runner can share one source of truth
// for which harness runs a model and what CLI model-id that harness expects. A
// model's identity (its provider-qualified ID) is independent of the executing
// harness: the Supported map records the harness-specific CLI id each driver
// needs, and Preferred names the default driver. The harness package layers
// resolution (ResolveModel/ValidateAllowlist) on top, filtering by which
// harnesses are enabled in a given deployment.
//
// Model ids resolve in three tiers (Resolve / Lookup):
//
//   - exact: a registered canonical id ("anthropic/claude-opus-5-5");
//   - alias: a family alias with no version ("anthropic/claude-opus") resolves
//     to the newest registered version of that family. Controllers resolve
//     aliases once at startup (Canonicalize), so only the concrete id flows
//     downstream — pod env, CRD status, rollups — and stats and pricing stay
//     per-version;
//   - pattern: a well-formed but unregistered Anthropic version id
//     ("anthropic/claude-opus-6") is synthesized as a claude-harness-only,
//     unpriced model, so a new Anthropic release runs before patchy registers
//     it. The claude CLI reports its own cost, so the missing rates matter
//     only for display until the model is registered.
package model
