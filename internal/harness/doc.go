// Copyright 2026 Bitwise Media Group Ltd.
// SPDX-License-Identifier: MIT

// Package harness abstracts the coding-agent CLIs patchy drives. A harness
// builds the argv for one prompted run (PromptSpec), the runner package
// executes it, and the harness parses the CLI's stream-json stdout back into
// an AgentResult — harnesses never touch os/exec themselves, so every caller
// can be tested against captured output.
//
// Two harnesses register: Claude (the claude CLI, patchy's real agent) and
// Fake (replays a fixture file through cat, for tests and the agent-runner's
// --fake mode).
package harness
