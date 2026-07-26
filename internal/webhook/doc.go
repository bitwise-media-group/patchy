// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package webhook is the provider-webhook HTTP server the
// integration-controller embeds: one listener, one bounded worker pool, and
// one delivery-ID dedup window shared by every provider route, plus
// health/readiness endpoints with graceful drain.
//
// Providers are the same shape downstream and differ only at the door, so an
// Endpoint supplies the two things that vary — how a delivery proves it is
// genuine (Authenticator) and where its event type and delivery id live
// (Decoder) — and the server owns everything after that. GitHub signs an
// HMAC over the raw body and labels deliveries with headers; a Google Cloud
// Pub/Sub push cannot compute an HMAC at all and instead presents a
// Google-signed OIDC token, with the message id inside the body.
//
// Every route answers 202 before handling: the delivery is queued, and a full
// queue answers 503 so the provider retries. Handlers must be idempotent —
// providers redeliver, the dedup window is finite, and the controllers'
// reconcile loops converge missed or dropped deliveries anyway.
package webhook
