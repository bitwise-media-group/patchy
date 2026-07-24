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
package model
