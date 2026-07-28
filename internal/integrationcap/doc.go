// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package integrationcap selects Integrations by the capability they provide.
// It exists because more than one controller now reads Integration
// configuration — the integration-controller's receiver routes deliveries by
// capability, and the context-controller reads the asset-inventory enhancer's
// settings — and the selection rule (exactly one enabled Integration per
// capability per namespace, the v1alpha1 singleton rule) must not fork.
package integrationcap
