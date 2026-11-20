// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package authz resolves what an identity may do in the status server's
// namespace. It asks the cluster directly (SubjectAccessReviews with the
// identity's user and groups), so the grant grammar is ordinary Kubernetes
// RBAC in the patchy.bitwisemedia.uk group: native get on findings,
// findingrollups and integrations gates the findings, metrics and
// configuration views; the custom verbs approve, retry, expedite, suspend
// and resume on findings gate the per-finding actions; and backfill, replay
// and reset on integrations gate the integration actions. Reviews run as
// the server's own ServiceAccount (which needs only create on
// subjectaccessreviews), never via impersonation.
package authz
