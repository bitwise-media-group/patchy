// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package webhookctrl implements the webhook-controller engine: the router
// between the GitHub App's single webhook URL and the controllers'
// cluster-internal receivers. A GitHub App can deliver to exactly one URL,
// but every controller runs its own receiver, so the webhook-controller — the only
// internet-facing patchy component — accepts each delivery once and forwards
// it to the targets its X-GitHub-Event type routes to (a "*" route catches
// event types no rule claims, e.g. those registered by pkg/source plugins).
//
// The webhook-controller holds no GitHub credential. It shares only the webhook HMAC
// secret, which it uses twice: internal/webhook validates the inbound
// signature before anything is forwarded, and the Forwarder re-signs the
// payload on the way out (an identical signature, since the secret is the
// same), so every controller keeps authenticating every delivery itself.
// Forwarding is best-effort: a failed or unrouted target is logged and
// skipped, because the controllers' reconcile loops — not webhook redelivery
// — are the correctness mechanism.
package webhookctrl
