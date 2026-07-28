// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package wiz is the Wiz source: it turns Wiz automation-rule webhook
// deliveries into patchy Findings. Two feeds share one endpoint — Wiz Issues
// (cloud misconfigurations and toxic combinations) and Wiz Defend (threat
// detections) — discriminated by payload shape, not by path or header: a Wiz
// automation action POSTs whatever body its rule template renders and carries
// no event-type header.
//
// The body template documented in docs/integrations/wiz.md is therefore the
// payload contract: this package decodes exactly the shapes that document
// tells operators to configure. There is no other spec to hold Wiz to — the
// payloads are operator-templated mustache, so patchy publishes the template
// rather than guessing at one.
//
// Wiz findings are about cloud resources, not repository code, so they carry
// a source.CloudResource and no Repo. Resources on Google Cloud have their
// providerId normalized to the Cloud Asset Inventory name form at ingest so
// the asset-inventory enhancer can resolve ownership labels — and possibly a
// repository — without knowing which source ingested the finding.
package wiz
