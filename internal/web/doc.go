// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package web is the status-server backend: the embedded status page SPA, a
// JSON projection of Findings, FindingRollups and the Forge/Integration
// configuration, the per-finding human actions (approve, retry, expedite,
// suspend, resume), the integration actions (backfill, and the replay and
// reset demo tooling), and an SSE change signal that tells open browsers to
// refetch.
//
// Every data route requires an identity (internal/web/auth) holding the
// matching RBAC grant (internal/web/authz): native get on findings for the
// findings surface (list, detail, transcripts), on findingrollups for
// metrics (GET /api/rollups and /api/rollups/repositories), on integrations
// for the configuration view, and the custom verbs for each action. The SSE
// stream needs any one of the read grants. Only auth mode none (an explicit
// "authentication disabled") opens everything; with no auth configuration
// at all nothing is served. The server writes spec only (spec.approval,
// spec.retry, spec.expedite, spec.suspend on Findings; spec.backfill,
// spec.replay, spec.reset on Integrations). It never writes status and never
// moves a phase: every phase edge belongs to its owning controller.
//
// The wire types in data.go mirror the status page's TypeScript contract
// (ui/src/types.ts) field for field; keep the two in lockstep.
package web
