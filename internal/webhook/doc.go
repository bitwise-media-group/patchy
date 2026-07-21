// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package webhook is the GitHub-webhook HTTP server the
// integration-controller embeds: HMAC-SHA256 signature validation on the
// raw body (against a static secret or the per-Integration candidate set),
// immediate 202 acknowledgement into a bounded worker pool, delivery-ID
// deduplication, and health/readiness endpoints with graceful drain.
//
// Handlers must be idempotent — GitHub redelivers, the dedup window is
// finite, and the controllers' reconcile loops converge missed or dropped
// deliveries anyway.
package webhook
