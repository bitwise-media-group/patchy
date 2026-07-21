// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package agentrun orchestrates the coding-agent stage inside the Job pod:
// render the prompt, drive the harness under a token-budget kill switch, and
// report the stage as an envelope event on stdout. The investigate phase
// runs the analysis and emits the verdict without ever deciding
// continuation; the remediate phase executes a controller-provided analysis,
// then verifies and packages the resulting changeset.
//
// The runner trusts the repository over the agent: a remediation report
// claiming success is downgraded unless commit.sh ran cleanly and left real
// commits on the branch. It never talks to a forge — the pod has no forge
// credentials by design; the controllers own every side effect.
package agentrun
