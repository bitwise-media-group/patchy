// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package devharness is the engine behind `patchy dev`: a local, in-memory
// stand-in for the pipeline that generic-integration authors test against.
//
// The harness hosts the real receiver code path — the same webhook server,
// HMAC authenticator, decoder, and validating source handler the
// integration-controller runs — but where the controller would write Finding
// resources, the harness keeps findings in memory and immediately exercises
// the two outbound legs of the contract: the enhancer call and the resolver
// write-back. No Kubernetes API is involved anywhere.
//
// What is deliberately not emulated: accumulation and duplicate-merge into
// Finding resources, tracking-issue projection, and the investigation that
// separates enhancement from dismissal in production. Where the timing or
// shape differs from the real pipeline (resolve fires immediately, one alert
// per call), the emitted events say so.
package devharness
