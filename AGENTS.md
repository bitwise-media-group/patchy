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

Ten binaries, one module. "Not monolithic" means separate binaries/deployments with shared `internal/` code:

- `cmd/integration-controller` — the single internet-facing entry point, driven by `Integration` CRs: validates
  provider webhooks (`/github/webhooks` HMAC, `/google-cloud/webhooks` Pub/Sub OIDC, `/wiz/webhooks` bearer
  token, the `/generic/{name}/webhooks` wildcard with strictly per-name HMAC; per-Integration secrets), ingests
  scanner alerts into Findings (accumulation, duplicate merge), projects Findings out as tracking issues
  (trackingRef falls back to the namespace's issues-enabled Integration for non-github sources), applies human
  signals (issue close, `/approve`, PR merge) back onto Findings, and POSTs dismissal verdicts to generic
  integrations' resolver endpoints.
- `cmd/source-controller` — `Forge` + `Repository` reconcilers: validates forge credentials, pins
  each Repository's head SHA once, downloads the tarball archive at that SHA (pure HTTP, no git binary), and
  serves it from the artifact endpoint (`:9790`) agent pods fetch credential-lessly.
- `cmd/context-controller` — runs the enhancer chain (CMDB placeholder + the Cloud Asset Inventory lookup, whose
  config is the `cloudAssetInventory` block on the `google-cloud` Integration, + the AWS and Azure resource-tags
  lookups, whose configs are the `resourceTags` blocks on the `aws` and `azure` Integrations, + the generic HTTP
  fan-out — one signed synchronous call per `generic` Integration with `enhance` on, N instances, each attributed
  by its own name) over `Opened` Findings, writes enrichments/owners to status, transitions to `Enhanced`. No
  GitHub access at all; reads Integrations read-only plus generic signing Secrets by name.
- `cmd/investigation-controller` — the gate (admits accumulated, aged findings; creates the Repository and one
  immutable `Investigation` per attempt) plus the analysis scheduler (bounded concurrency, launches agent Jobs,
  routes verdicts onto the Finding).
- `cmd/remediation-controller` — queue admission (approvals/revivals), the priority scheduler, remediation agent
  Jobs, changeset push + PR via the forge write seam (the only write credential), and hosts the rollup/TTL loop.
- `cmd/agent-runner` — the in-pod coding-agent runtime: one stage per Job (`investigate` or `remediate`) via
  `claude -p`, results emitted as a `PATCHY-EVENT:` JSONL stream on stdout. Never talks to GitHub or the
  Kubernetes API; a claude pod holds no credential at all (model traffic goes through the egress broker,
  authenticated by a projected SA token read fresh per stage), a codex/copilot pod only its model key.
- `cmd/egress-broker` — the egress credential broker (NOT a controller: no reconcilers, no leases): the reverse
  proxy all claude model traffic goes through, one route per provider (anthropic — key or `claude setup-token`
  bearer via `--anthropic-auth` — plus bedrock SigV4, vertex OAuth, foundry key/entra). Validates caller
  tokens via TokenReview (its only Kubernetes access), strips them, injects/signs the model credential
  outbound, streams SSE with idle keep-alive pings, audits one slog line per request. Engine in
  `internal/broker`; deployed by the chart exactly when a claude runner is enabled (claude ⇒ broker;
  proxy-only, no in-pod credential mode).
- `cmd/evaluation-controller` — OPTIONAL (default-off in the chart): remote skill-evaluation execution for
  evolve. Hosts the bearer-authenticated HTTP API (`pkg/evaluation` wire contract: workspace upload streamed to
  source-controller's `:9791` blob endpoint, submission, snapshot, SSE monitoring, cancel; OIDC verify + SAR on
  the `evaluations` resource, native verbs only) and the reconcilers: gate (Evaluation → EvaluationUnit
  children), unit scheduler (bounded concurrency over the same sandboxed agent-Job machinery; pods run
  `evolve exec-unit` and emit `EVOLVE-EVENT:` JSONL), and the TTL loop. Patchy never learns eval semantics —
  bounded summaries land on unit status, the opaque results entry in a per-unit ConfigMap.
- `cmd/status-server` — the human-facing status page (NOT a controller: no reconcilers, no leases): the embedded
  SPA + JSON projection of Findings/FindingRollups, SSE refetch signal, OIDC sign-in, the access-review-gated
  approve/retry/expedite/suspend/resume actions, and the user-menu demo tooling (replay → Integration
  `spec.replay`; reset → delete all pipeline CRs). Rollup statistics are public; the findings surface always
  requires auth. Writes SPEC only (`spec.approval`, `spec.suspend`, `spec.replay`) — never status, never a phase.
- `cmd/patchy` — the workstation CLI (the only binary not deployed): `patchy <verb> <noun>` over the
  caller's own kubeconfig, no channel through any controller. get/describe/review/browse/can-i plus the five
  action verbs. Writes SPEC only, same as status-server; enforcement of the custom verbs for direct API
  writes is the ValidatingAdmissionPolicy in `deploy/kustomize/base/admission-policy.yaml`, NOT the CLI's
  own SelfSubjectAccessReview (that is ergonomics). Two cluster-free command groups ride along: `dev` (the
  generic-integration test harness) and `mirror` (vendored chart/artifact mirroring over `internal/mirror`;
  kubeconfig flags inert, git never touched). Builds for windows too, and ships a `kubectl-patchy`
  alias. Ships no container image: it is distributed as its own `patchy-cli` release archive (separate from
  the cluster binaries' `patchy` archive) and as a Homebrew cask in bitwise-media-group/homebrew-tap.

## Layout

```text
api/v1alpha1/       The CRD types: one <kind>_types.go per kind, transitions.go (the phase table +
                    SetPhase), conditions.go, generated deepcopy. `mise run codegen` regenerates
                    deepcopy + the CRD manifests (kustomize + helm); CI fails on drift.
cmd/<binary>/       package main, thin: build root command, delegate to internal/cli.Execute.
                    cmd/patchy is the exception — the CLI, with its own internal/ tree
                    (cli = one file per VERB, render = one file per NOUN; the two are separate
                    axes on purpose, since `get` resolves nouns through a registry at runtime).
                    cmd/patchy/internal/tools/docgen is dev tooling, not a binary: it renders
                    docs/cli from the cobra tree. It sits there because the internal rule puts
                    cmd/patchy/internal/cli out of reach of the repo root, and NOT under cmd/
                    because hack/build.sh builds everything there.
internal/           All private code, one package per concern (see "Packages" below).
pkg/                PUBLIC plugin seams only: pkg/source (finding sources), pkg/enhance (context
                    enhancers), pkg/generic (the generic integration's HTTP wire contract, importable
                    by external processes), pkg/evaluation (the remote-evaluation wire contract —
                    submissions, the in-pod EVOLVE-EVENT stream, the SSE monitor — stdlib-only,
                    imported by evolve). Exported signatures must not reference internal/ types.
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
                    (pyproject.toml / uv.lock). docs/cli/ is GENERATED (docgen, above) — edit
                    the commands' Short/Long/Example, not the markdown; docs/cli.md beside it
                    is the hand-written tour.
completions/        GENERATED shell completions, committed so the Homebrew cask installs them
                    as static files (executing a freshly-downloaded binary at install time
                    trips Gatekeeper). `mise run docs` writes both this and docs/cli/.
                    kubectl_complete-patchy is the exception — hand-written, and the reason
                    `kubectl patchy` completes: kubectl ignores a plugin's completion script
                    and instead runs kubectl_complete-<plugin> from PATH.
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
  (all-time stats + finding TTL; hosted by the remediation binary), `controller/evaluation` (Evaluation gate +
  unit scheduler + evaluation TTL; single writer for both evaluation kinds — their phases are local enums,
  never part of the Finding transition table).
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
- `action` — the human-action vocabulary (the custom verbs) and the state-machine gating behind each one:
  `Apply` mutates spec, `Available` reports what a finding currently admits. Owned here so the status
  server and the CLI cannot drift; `web/authz` re-exports the verb constants. It decides whether an action
  is MEANINGFUL, never whether the caller may take it.
- `web` (+ `web/auth`, `web/authz`) — the status-server backend: wire types mirroring the SPA's
  `ui/src/types.ts` (keep the two in lockstep), the action handlers, SSE broker + cache-informer watcher, and
  the embedded UI (`internal/web/ui`, Vite/Preact, single-file build embedded behind the `withui` tag; `mise run
  ui` builds it, bare `go build` compiles a stub). `auth` = who you are (OIDC/none/anonymous/unconfigured,
  cookie sessions, zero k8s imports); `authz` = what you may do (SubjectAccessReviews for the custom verbs
  approve/retry/expedite/suspend/resume + native get).
- `ghas`, `enhancers` — the built-in `pkg/source` and `pkg/enhance` implementations.
- `generic` — the generic integration's behavior over the `pkg/generic` wire contract: the validating source
  handler (source id = the Integration's NAME; N integrations coexist) and the HMAC-signing outbound client behind
  both the verdict resolver and the enhancer call. The enhancer fan-out itself is
  `enhancers.DynamicGeneric`/`MultiEnhancer` (internal seam; `pkg/enhance` stays one-plugin-one-identity).
- `harness`, `runner` — adapted from evolve: harness builds argv, runner executes (observe-and-collect with a
  token-budget kill switch), harness parses stdout. Keep that separation.
- `agentrun` — the in-pod stage flow (`investigate` | `remediate`); `report`/`envelope` are its contracts
  (frontmatter schemas in, JSONL events out); `agentresult` converts envelope results onto CR status.
- `jobs` — the Kubernetes Job the agent runs in. The isolation model lives here, and it STRENGTHENED with the
  broker: a brokered (claude) pod holds no credential of any kind — its projected SA token (audience-bound,
  agent container only, never the init) is an identity document, not a capability — while non-brokered runners
  keep the one SecretKeyRef; the init container fetches the digest-verified artifact tarball, identity-free.
  `reservedEnv` covers every credential channel plus the provider gateway names; `Runner.Env` is the per-runner
  gateway env (wins over `Config.Env`, can never name a credential). `eval.go` is the evaluation Job flavour
  (same posture; `evolve exec-unit` instead of `agent-runner` — wrapped in a capture-once token export when
  brokered — no git init, unit.json handoff); `ResultLines` is the envelope-agnostic log reader the evaluation
  collector decodes its own events from.
- `broker`, `provider` — the egress-broker engine (TokenReview auth + verdict cache, per-route credential
  strategies, SSE-safe reverse proxy, audit) and the pure logic of brokered claude runners (gateway env,
  canonical→provider model-id maps with per-provider derived defaults, the `PATCHY_MODEL_MAP` codec both the
  controllers and agentrun share). `provider.BrokerTokenHeader` is the one definition of the caller-token
  header.
- `artifact` — the tarball store + HTTP handler source-controller serves agent fetches from, plus the
  content-addressed workspace-blob side: sha256-named bundles (64-hex files beside the 32-hex repo tarballs),
  index rebuilt from disk on restart, last-access retention sweep, the `:9791` internal upload handler, and
  the `Client` other processes reach it with.
- `evalapi` — the evaluation controller's HTTP surface (`pkg/evaluation` contract): bearer OIDC verify (claims
  via `web/auth.MapClaims`), SAR authorization (`web/authz.ResourceReviewer`, native verbs on `evaluations`),
  workspace upload proxy, submission validation, snapshot, SSE monitor (replay + change re-emit + explicit
  `end`). `evalresults` is the per-unit results ConfigMap store (transcriptstore's sibling).
- `ghpush` — replays the agent's changeset through the GitHub Git Data API (blob → tree → commit → ref); the
  only place a write credential is exercised. No git binary anywhere controller-side.
- `mirror` — the engine behind `patchy mirror` (CLI-only; no controller consumes it): vendored mirroring of
  upstream helm charts and OCI artifacts into a platform registry. One concern per subpackage: `spec` (the
  mirror.yaml/manifest/lock schema + glob discovery — the tree is the registry of entries), `imageref`,
  `semverpick` (constraints + cooldown walks), `yamledit` (comment-preserving byte-splice edits, never
  re-encode), `helmchart` (pull/extract/tree-diff/push), `render` (byte-stable `helm template` equivalent —
  helm SDK pinned; upgrades are deliberate, the validate gate byte-compares output), `discover` (4-pass image
  discovery), `distro` (distribution-manifests → generated images.extra.yaml sidecar), `verify`/`sign`
  (cosign: upstream provenance in, bundle-referrers signatures out; KMS via sigstore providers), `scan`
  (pluggable: osv library + grype/kubescape shell-outs), `allowlist` (derive with keep-expiry/drop-stale
  rules). The engine never touches git — the calling pipeline owns commits/branches/PRs — and only the
  upgrade path may consult the wall clock, keeping `validate`'s byte-identity gate deterministic.

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
