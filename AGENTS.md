# Agent orientation

Fast map of this repository so a new session can act without re-exploring. For _what_ the system must do — the
requirements, the state machine, and the end-to-end flow — read [DESIGN.md](DESIGN.md); for end-user usage read
[README.md](README.md). This file is the "where things are"; DESIGN.md is the "what it must do".

## What this is

`patchy` is an end-to-end pipeline (module `github.com/bitwise-media-group/patchy`) for triaging and remediating
security findings, using **Kubernetes custom resources as the state machine** — the
`patchy.bitwisemedia.uk/v1alpha1` kinds carry all state, etcd is the only state store, and GitHub issues are a
one-way human-facing projection. GHAS/CodeQL alerts arrive via webhook, accumulate into `Finding` resources for an
hour, get context-enhanced, then a sandboxed `claude -p` run investigates each one; high-confidence verdicts are
remediated in priority order into pull requests, everything else routes to humans. Completed findings expire on a
TTL; `FindingRollup` resources keep the all-time statistics.

Seven binaries, one module. "Not monolithic" means separate binaries/deployments with shared `internal/` code:

- `cmd/integration-controller` — the single internet-facing entry point, driven by `Integration` CRs: validates
  provider webhooks (`/github/webhooks`, per-Integration HMAC secrets), ingests scanner alerts into Findings
  (accumulation, duplicate merge), projects Findings out as tracking issues, and applies human signals (issue
  close, `/approve`, PR merge) back onto Findings.
- `cmd/source-controller` — `Forge` + `Repository` reconcilers: validates forge credentials, pins
  each Repository's head SHA once, downloads the tarball archive at that SHA (pure HTTP, no git binary), and
  serves it from the artifact endpoint (`:9790`) agent pods fetch credential-lessly.
- `cmd/context-controller` — runs the enhancer chain (CMDB placeholder) over `Opened` Findings, writes
  enrichments/owners to status, transitions to `Enhanced`. No GitHub access at all.
- `cmd/investigation-controller` — the gate (admits accumulated, aged findings; creates the Repository and one
  immutable `Investigation` per attempt) plus the analysis scheduler (bounded concurrency, launches agent Jobs,
  routes verdicts onto the Finding).
- `cmd/remediation-controller` — queue admission (approvals/revivals), the priority scheduler, remediation agent
  Jobs, changeset push + PR via the forge write seam (the only write credential), and hosts the rollup/TTL loop.
- `cmd/agent-runner` — the in-pod coding-agent runtime: one stage per Job (`investigate` or `remediate`) via
  `claude -p`, results emitted as a `PATCHY-EVENT:` JSONL stream on stdout. Never talks to GitHub or the
  Kubernetes API; no credentials beyond the model key.
- `cmd/status-server` — the human-facing status page (NOT a controller: no reconcilers, no leases): the embedded
  SPA + JSON projection of Findings/FindingRollups, SSE refetch signal, OIDC sign-in, the access-review-gated
  approve/retry/expedite/suspend/resume actions, and the user-menu demo tooling (replay → Integration
  `spec.replay`; reset → delete all pipeline CRs). Rollup statistics are public; the findings surface always
  requires auth. Writes SPEC only (`spec.approval`, `spec.suspend`, `spec.replay`) — never status, never a phase.

## Layout

```text
api/v1alpha1/       The CRD types: one <kind>_types.go per kind, transitions.go (the phase table +
                    SetPhase), conditions.go, generated deepcopy. `mise run codegen` regenerates
                    deepcopy + the CRD manifests (kustomize + helm); CI fails on drift.
cmd/<binary>/       package main, thin: build root command, delegate to internal/cli.Execute.
internal/           All private code, one package per concern (see "Packages" below).
pkg/                PUBLIC plugin seams only: pkg/source (finding sources), pkg/enhance (context
                    enhancers). Exported signatures must not reference internal/ types.
deploy/             kustomize base/overlays; deploy/README.md is the operator doc. The container
                    Dockerfile.* live at the repo root (goreleaser dockers_v2 builds them).
charts/             Helm rendering of the same stack, pushed to ghcr OCI on release
                    (.github/workflows/helm.yaml): charts/patchy (CRDs + controllers) and
                    charts/patchy-config (the Integration/Forge CRs — a separate chart because
                    helm validates CRs against CRDs that must already exist). release-please
                    stamps both Chart.yaml versions. Lint/render with `mise run helm-lint`.
e2e/                SEPARATE Go module: envtest carries the CRDs, the real binaries run against it,
                    fakegithub (in-memory API) stands in at the network edge, recorded webhook
                    fixtures + the replay tool drive it (`make e2e`).
docs/ overrides/    Zensical docs site (zensical.toml at the root; patchy-branded theme in
                    docs/stylesheets/extra.css + overrides/). `mise run serve` to preview,
                    `mise run docs-build` to build; the reusable release workflow publishes it
                    to GitHub Pages (oss.bitwisemedia.uk/patchy). uv provisions zensical
                    (pyproject.toml / uv.lock).
.mise/              Shared toolchain submodule (bitwise-media-group/toolchain): pinned dev CLIs +
                    the go-cli task archetype. Makefile is a one-line forwarder; repo-local tasks
                    (multi-binary build, e2e, envtest, codegen, replay) live in tasks.toml.
.claude/plans/      The living implementation plan (git-ignored).
```

## Packages (`internal/`)

- `controller/` — one engine per controller binary; the binaries are thin wiring over these:
  `controller/integration` (receiver, ingest, projection, human signals), `controller/source` (Forge +
  Repository reconcilers), `controller/context` (the enhancer chain), `controller/investigation` (gate +
  analysis scheduler), `controller/remediation` (spawner + priority scheduler + push/PR), `controller/rollup`
  (all-time stats + finding TTL; hosted by the remediation binary).
- `kube` — the controller-runtime manager wrapper: scheme, kubeconfig/in-cluster config, leader election,
  multi-namespace cache, health probes, logr↔slog bridge. Secrets are never cached.
- `forge` — the shared forge seam: resolve a repository URL to its covering `Forge` CR (host → orgs → repo
  regexes; most-constrained wins) and mint scoped read/write tokens. Consumers: source (read), remediation
  (write). `ghclient`, `ghpush`, `ghsecret` sit beneath it.
- `schedule`, `priority`, `stats` — pure logic: slot picking with anti-starvation aging, the 0–100 scheduling
  score, rollup delta arithmetic + OTel taps.
- `labels` — the trimmed human-facing label vocabulary the issue projection renders (one-way; never parsed back
  into state).
- `templates` — the finding handoff/issue body, both stage prompts, and the PR body, rendered from embedded
  templates with golden tests.
- `webhook`, `telemetry`, `cli`, `version` — service plumbing (the webhook server is used by
  integration-controller only).
- `web` (+ `web/auth`, `web/authz`) — the status-server backend: wire types mirroring the SPA's
  `ui/src/types.ts` (keep the two in lockstep), the action handlers, SSE broker + cache-informer watcher, and
  the embedded UI (`internal/web/ui`, Vite/Preact, single-file build embedded behind the `withui` tag; `mise run
  ui` builds it, bare `go build` compiles a stub). `auth` = who you are (OIDC/none/anonymous/unconfigured,
  cookie sessions, zero k8s imports); `authz` = what you may do (SubjectAccessReviews for the custom verbs
  approve/retry/expedite/suspend/resume + native get).
- `ghas`, `enhancers` — the built-in `pkg/source` and `pkg/enhance` implementations.
- `harness`, `runner` — adapted from evolve: harness builds argv, runner executes (observe-and-collect with a
  token-budget kill switch), harness parses stdout. Keep that separation.
- `agentrun` — the in-pod stage flow (`investigate` | `remediate`); `report`/`envelope` are its contracts
  (frontmatter schemas in, JSONL events out); `agentresult` converts envelope results onto CR status.
- `jobs` — the Kubernetes Job the agent runs in. The isolation model lives here: no credential of any kind in
  the pod; the init container fetches the digest-verified artifact tarball.
- `artifact` — the tarball store + HTTP handler source-controller serves agent fetches from.
- `ghpush` — replays the agent's changeset through the GitHub Git Data API (blob → tree → commit → ref); the
  only place a write credential is exercised. No git binary anywhere controller-side.

## Conventions

- Go 1.26; cobra + viper (`PATCHY_` env prefix); `log/slog` to stderr (stdout is reserved — agent-runner's event
  stream lives there); OpenTelemetry with an otelslog fanout that never fails startup.
- Every package has a `doc.go`; every file starts with the MIT SPDX header (enforced by revive + addlicense).
- Table-driven stdlib tests, no testify; fakes over mocks; controller-runtime fake client for reconciler tests;
  envtest suites skip without `KUBEBUILDER_ASSETS` (`mise run envtest`, `mise run e2e`).
- Conventional Commits; release-please + goreleaser drive releases; `make pr` is the local gate.
- The harness/runner packages are adapted from `../evolve` (`internal/harness`, `internal/runner`) — keep their
  "harness builds argv, runner executes, harness parses stdout" separation intact.

## State machine (the heart of the system)

`api/v1alpha1` owns the phase taxonomy and legal transitions (`transitions.go`: `CanTransition`, `Terminal`,
`SetPhase`); no phase edge has two writer components. The flow:
`Opened → Enhanced → Investigating → Queued → Remediating → InReview → Remediated`, with `AwaitingApproval`
before `Queued` on holds, and `Dismissed`/`HandedOff`/`Failed` terminal (`HandedOff` revivable by approval,
`Failed` by human retry back to the pre-failure state; `spec.expedite` skips accumulation/min-age and jumps both
schedulers' queues).
Accumulation is a condition (`AccumulationComplete`), not a phase. See DESIGN.md for the full flow and
.claude/plans/ for the transition table with writers.
