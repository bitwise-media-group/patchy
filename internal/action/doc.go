// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package action owns the human-action vocabulary and the state-machine
// gating behind it: which verbs exist, and what each one is allowed to do to a
// Finding's spec in a given phase.
//
// It exists so the two clients that apply human actions cannot drift. The
// status server (internal/web) applies them as its own ServiceAccount after a
// SubjectAccessReview; the patchy CLI (cmd/patchy) applies them as the signed-in
// user's kubeconfig identity. Both call Apply, so a phase gate tightened here
// tightens in both places at once.
//
// Authorization is emphatically NOT this package's job — it decides only
// whether an action is *meaningful*, never whether the caller may take it.
// Grants live in internal/web/authz (SubjectAccessReview) for the server, in a
// SelfSubjectAccessReview for the CLI, and are enforced for real by the
// ValidatingAdmissionPolicy that binds each custom verb to the spec field it
// governs.
//
// Every verb is idempotent: re-applying one whose effect is already recorded
// succeeds and reports changed=false, so a double-click and a re-run of a shell
// loop both degrade to no-ops rather than churning the object.
package action
