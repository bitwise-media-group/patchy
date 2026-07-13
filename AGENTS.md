# Agent orientation

Fast map of this repository so a new session can act without re-exploring. For _what_ the system must do — the
requirements, the label taxonomy, and the end-to-end flow — read [DESIGN.md](DESIGN.md); for end-user usage read
[README.md](README.md). This file is the "where things are"; DESIGN.md is the "what it must do".

## What this is

`patchy` is an end-to-end pipeline (module `github.com/bitwise-media-group/patchy`) for triaging and remediating
security findings, using **GitHub issues as the state machine** — labels carry the state, and GitHub is the only
state store. GHAS/CodeQL alerts arrive via webhook, accumulate into issues for an hour, get context-enhanced, then a
sandboxed `claude -p` run classifies each issue and, at high confidence, remediates it into a pull request; everything
else routes to humans.

Four binaries, one module. "Not monolithic" means separate binaries/deployments with shared `internal/` code:

- `cmd/source-controller` — receives `code_scanning_alert` webhooks, opens/accumulates issues (1-hour window keyed on
  the `security-advisory` label), flips accumulation state on age.
- `cmd/context-controller` — reacts to `security-finding: opened` issues, runs the enhancer chain (CMDB placeholder),
  transitions to `context-enhanced`.
- `cmd/remediation-controller` — picks up enhanced issues older than 1h, runs the agent in an ephemeral Kubernetes
  Job, and performs **all** GitHub side effects (labels, comments, alert dismissal, branch push, PRs).
- `cmd/agent-runner` — the in-pod coding-agent runtime: classification then remediation via `claude -p`, results
  emitted as a `PATCHY-EVENT:` JSONL stream on stdout. Never talks to GitHub; has no GitHub credentials.

## Layout

```text
cmd/<binary>/       package main, thin: build root command, delegate to internal/cli.Execute.
internal/           All private code, one package per concern (see "Packages" below).
pkg/                PUBLIC plugin seams only: pkg/source (finding sources), pkg/enhance (context
                    enhancers). Exported signatures must not reference internal/ types.
deploy/             kustomize base/overlays; deploy/README.md is the operator doc. The container
                    Dockerfile.* live at the repo root (goreleaser dockers_v2 builds them).
e2e/                SEPARATE Go module: fakegithub (in-memory API), recorded webhook fixtures, the
                    replay tool, and the suite that drives the real binaries (`make e2e`).
.mise/              Shared toolchain submodule (bitwise-media-group/toolchain): pinned dev CLIs +
                    the go-cli task archetype. Makefile is a one-line forwarder; repo-local tasks
                    (multi-binary build, e2e, replay) live in tasks.toml.
.claude/plans/      The living implementation plan (git-ignored).
```

## Packages (`internal/`)

- `labels` — **the state machine**: typed label set, parse/render/diff, the legal transition table. Start here.
- `templates` — every markdown artifact (issue body + its machine manifest, comments, PR body, the agent's
  handoff file and both stage prompts), rendered from embedded templates with golden tests.
- `ghclient` — GitHub App auth, scoped-token minting, narrow issue/alert/repo stores, rate-limit-aware transport.
- `webhook`, `reconcile`, `telemetry`, `cli`, `version` — the shared service plumbing every controller composes.
- `ghas`, `enhancers` — the built-in `pkg/source` and `pkg/enhance` implementations.
- `sourcectrl`, `contextctrl`, `remedctrl` — one engine per controller; the binaries are thin wiring over these.
- `harness`, `runner` — adapted from evolve: harness builds argv, runner executes (observe-and-collect with a
  token-budget kill switch), harness parses stdout. Keep that separation.
- `agentrun` — the in-pod two-stage flow; `report`/`envelope` are its contracts (frontmatter schemas in, JSONL
  events out).
- `jobs` — the Kubernetes Job the agent runs in. The isolation model lives here: the clone token reaches only the
  init container.
- `gitpush` — unpacks the agent's bundle and pushes the branch; the only place a write credential exists.
- `ghfake` — the in-memory issue store the controller tests share.

## Conventions

- Go 1.26; cobra + viper (`PATCHY_` env prefix); `log/slog` to stderr (stdout is reserved — agent-runner's event
  stream lives there); OpenTelemetry with an otelslog fanout that never fails startup.
- Every package has a `doc.go`; every file starts with the MIT SPDX header (enforced by revive + addlicense).
- Table-driven stdlib tests, no testify; fakes over mocks; `httptest` fake GitHub for client/controller tests.
- Conventional Commits; release-please + goreleaser drive releases; `make pr` is the local gate.
- The harness/runner packages are adapted from `../evolve` (`internal/harness`, `internal/runner`) — keep their
  "harness builds argv, runner executes, harness parses stdout" separation intact.

## State machine (the heart of the system)

`internal/labels` owns the label taxonomy and legal transitions; no label key has two writer components. The flow:
`opened → context-enhanced → classifying → classified → in-review → remediated` (or `attempted`), with
`security-accumulation: open|complete` gating the 1-hour window. See DESIGN.md for the full label list and
.claude/plans/ for the transition table with writers.
