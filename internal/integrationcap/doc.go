// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package integrationcap selects Integrations by the capability they provide.
// More than one controller reads Integration configuration: the
// integration-controller's receiver routes deliveries by capability, and the
// context-controller reads the enhancers' settings. Keeping selection here
// stops the rule (exactly one enabled Integration per capability per
// namespace, the v1alpha1 singleton rule) from forking between them.
package integrationcap
