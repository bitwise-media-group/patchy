// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package agentrun orchestrates the coding-agent stages inside the Job pod:
// render the prompts, drive the classification harness, decide whether to
// continue, drive the remediation harness under a token-budget kill switch,
// verify and package the resulting changeset, and report every stage as an
// envelope event on stdout.
//
// The runner trusts the repository over the agent: a remediation report
// claiming success is downgraded unless commit.sh ran cleanly and left real
// commits on the branch. It never talks to GitHub — the pod has no GitHub
// credentials by design; the remediation-controller owns every side effect.
package agentrun
