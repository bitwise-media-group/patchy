# Agent orientation

A quick map of this repository, so a new session can act without exploring it again. For _what_ the system must do
(the requirements, the state machine and the end-to-end flow), read [DESIGN.md](DESIGN.md). For end-user usage, read
[README.md](README.md). This file says where things are; DESIGN.md says what the system must do.

## What this is

`patchy` is an end-to-end pipeline (module `github.com/bitwise-media-group/patchy`) for triaging and fixing security
findings. **Kubernetes custom resources are the state machine.** The `patchy.bitwisemedia.uk/v1alpha1` kinds carry
all state, etcd is the only state store, and GitHub issues are a one-way projection for humans. Alerts arrive from code
scanners and cloud security tools (GitHub code scanning, Google Cloud SCC, Wiz and generic webhooks). They collect into
`Finding` resources for an hour and get context added. Then a sandboxed agent run (`claude -p` or `codex exec`)
investigates each one. High-confidence verdicts are fixed in priority order as pull requests, and everything else goes
to humans. Completed findings expire on a TTL, and `FindingRollup` resources keep the all-time statistics.

Many binaries, one module. "Not monolithic" means separate binaries and deployments that share `internal/` code:

- `cmd/integration-controller`: the single internet-facing entry point, driven by `Integration` CRs. It validates
  provider webhooks (`/github/webhooks` with HMAC, `/google-cloud/webhooks` with a Pub/Sub OIDC token, `/wiz/webhooks`
  with a bearer token, and the `/generic/{name}/webhooks` wildcard with an HMAC per name; each Integration has its own
  secrets). It turns scanner alerts into Findings (accumulation, duplicate merge) and projects Findings out as tracking
  issues. For non-github sources, trackingRef falls back to the namespace's Integration that has issues enabled. It
  applies human signals (issue close, `/approve`, PR merge) back onto Findings, and POSTs dismissal verdicts to generic
  integrations' resolver endpoints. It hosts the `BackfillRequest` reconciler (walks requests one at a time, the single
  writer of their status, deletes finished and undecided ones after `--backfill-request-ttl`). On its internal listener
  (`:9792`, the status server's projected token only) it serves the repository-access check and the GitHub-user
  endpoints behind "Connect GitHub" (code exchange, refresh, maintained-repository list, live role check); it alone
  reads the App's `clientSecret`.
- `cmd/source-controller`: the `Forge` and `Repository` reconcilers. It validates forge credentials, pins each
  Repository's head SHA once, downloads the tarball at that SHA (plain HTTP, no git binary), and serves it from the
  artifact endpoint (`:9790`). Agent pods fetch from there without a credential.
- `cmd/context-controller`: runs the enhancer chain over `Opened` Findings, writes enrichments and owners to status,
  and moves them to `Enhanced`. The chain is:
  - a CMDB placeholder;
  - the Cloud Asset Inventory lookup, configured by the `cloudAssetInventory` block on the `google-cloud` Integration;
  - the AWS and Azure resource-tags lookups, configured by the `resourceTags` blocks on the `aws` and `azure`
    Integrations;
  - the generic HTTP fan-out: one signed, synchronous call per `generic` Integration with `enhance` on, each result
    credited to that Integration's name.

  It has no GitHub access at all. It reads Integrations (read-only) and the generic signing Secrets by name.

- `cmd/investigation-controller`: the gate and the analysis scheduler. The gate admits findings that have finished
  accumulating and are old enough, and creates the Repository and one immutable `Investigation` per attempt. The
  scheduler limits concurrency, launches agent Jobs, and routes verdicts onto the Finding.
- `cmd/remediation-controller`: queue admission (approvals and revivals), the priority scheduler, remediation agent
  Jobs, and the changeset push and PR through `internal/forge` (the only write credential). It also hosts the rollup
  and TTL loop.
- `cmd/agent-runner`: the coding-agent runtime inside the pod. It runs one stage per Job (`investigate` or
  `remediate`) through `claude -p` or `codex exec`, and writes results as a `PATCHY-EVENT:` JSONL stream on stdout. It
  never talks to GitHub or the Kubernetes API. A claude or codex pod holds no credential at all. Model traffic goes
  through the egress broker, authenticated by a projected SA token that is read again for each stage. codex reaches the
  broker through a `config.toml` model provider that agent-runner writes before each stage.
- `cmd/egress-broker`: the egress credential broker. It is NOT a controller (no reconcilers, no leases). It is the
  reverse proxy all claude and codex model traffic goes through, with one route per provider:
  - claude: anthropic (an API key, or a `claude setup-token` bearer, chosen with `--anthropic-auth`), bedrock (SigV4),
    vertex (OAuth) and foundry (key or Entra);
  - codex: openai (bearer key) and azure (api-key or Entra).

  It validates caller tokens with TokenReview (its only Kubernetes access), strips them, adds or signs the model
  credential on the way out, streams SSE with idle keep-alive pings, and writes one slog audit line per request. The
  engine is in `internal/broker`. The chart deploys it exactly when a claude or codex runner is enabled. There is no
  mode that puts the credential in the pod: claude or codex always means the broker.

- `cmd/evaluation-controller`: OPTIONAL, and off by default in the chart. It runs skill evaluations remotely for
  evolve. It hosts the HTTP API (the `pkg/evaluation` wire contract), which uses bearer auth:
  - workspace upload, streamed to source-controller's `:9791` blob endpoint;
  - submission, snapshot, SSE monitoring and cancel;
  - OIDC verification plus a SubjectAccessReview on the `evaluations` resource (native verbs only).

  It also runs the reconcilers. The gate turns an Evaluation into EvaluationUnit children. The unit scheduler limits
  concurrency over the same sandboxed agent-Job machinery; its pods run `evolve exec-unit` and emit `EVOLVE-EVENT:`
  JSONL. The TTL loop expires old evaluations. Patchy never learns what an evaluation means. Short summaries land on
  the unit's status, and the opaque results entry goes in a ConfigMap for each unit.

- `cmd/status-server`: the human-facing status page. It is NOT a controller (no reconcilers, no leases). It serves:
  - the embedded SPA and a JSON view of Findings and FindingRollups, plus an SSE signal to refetch;
  - OIDC sign-in;
  - the approve, retry, expedite, suspend and resume actions, each gated by an access review;
  - the user-menu demo tools (replay sets Integration `spec.replay`; reset sets Integration `spec.reset`);
  - backfills: it creates `BackfillRequest`s (at once on `backfill`, at once on `backfill-maintained` for a repository
    the linked GitHub identity maintains, or as a request any viewer may file), with one open request per repository,
    a per-repository cooldown (`--repository-backfill-cooldown`) and 10 waiting requests per user; operators approve or
    deny on the same page (`spec.decision`);
  - the optional "Connect GitHub" link (`/oauth2/github/<integration>/…`, `internal/web/ghlink`), and the scoped
    maintainer verbs `approve-maintained`, `expedite-maintained` and `backfill-maintained`, which only it honours
    (live GitHub role check on every POST; the CLI and the admission policy ignore them).

  Every data route needs an authorised identity (metrics: `get` on findingrollups; findings: `get` on findings). Only
  auth mode none opens them, and with no auth config the server serves nothing. It writes SPEC only (`spec.approval`,
  `spec.retry`, `spec.expedite`, `spec.suspend`, and Integration `spec.replay` and `spec.reset`), plus new
  `BackfillRequest`s and their one-time `spec.decision`. It never writes status or a phase, and never calls GitHub:
  every GitHub call goes through the integration-controller.

- `cmd/patchy`: the workstation CLI, and the only binary that is not deployed. It runs `patchy <verb> <noun>` with the
  caller's own kubeconfig, not through any controller: get, describe, review, browse and can-i, plus the five action
  verbs. Like status-server, it writes SPEC only, plus the `BackfillRequest`s it files and decides (`patchy backfill`,
  `patchy approve|deny backfillrequest`). For direct API writes, the custom verbs are enforced by the
  ValidatingAdmissionPolicies in `deploy/kustomize/base/admission-policy.yaml` (`patchy-backfill-requests` covers
  BackfillRequests). The CLI's own SelfSubjectAccessReview is
  only there to give a clearer error early.

  It also carries three command groups that need no cluster: `dev` (the generic-integration test harness), `mirror`
  (mirroring vendored charts and artifacts, over `internal/mirror`) and `build` (building, scanning, publishing and
  signing first-party images and Flux manifests, over `internal/build`). The kubeconfig flags do nothing for these,
  and they never touch git. All three read the layered patchy config (`cmd/patchy/internal/config`):
  - up to seven locations, from `/etc/patchy/config.<ext>` to `<workdir>/patchy.<ext>`, in yaml, yml, json or toml;
  - maps merge, lists replace, and null deletes; each layer's keys are checked strictly;
  - the engines only ever receive typed structs (`File.MirrorConfig()`, `File.BuildConfig()`) and never look for
    files themselves.

  It builds for windows too, and ships a `kubectl-patchy` alias (`cmd/kubectl-patchy`). It has no container image. It
  ships as its own `patchy-cli` release archive (separate from the cluster binaries' `patchy` archive) and as a
  Homebrew cask in bitwise-media-group/homebrew-tap.

## Layout

```text
api/v1alpha1/       The CRD types: one <kind>_types.go per kind, transitions.go (the phase table and
                    SetPhase), conditions.go, and generated deepcopy. `mise run codegen` regenerates
                    deepcopy and the CRD manifests (kustomize and helm); CI fails if they drift.
cmd/<binary>/       package main, kept thin: build the root command and hand off to internal/cli.Execute.
                    cmd/patchy is the exception. It is the CLI, with its own internal/ tree
                    (cli = one file per VERB, render = one file per NOUN). The two are separate
                    on purpose, since `get` looks nouns up in a registry at runtime.
                    cmd/patchy/internal/tools/docgen is dev tooling, not a binary: it renders
                    docs/cli from the cobra tree. It lives there because Go's internal rule puts
                    cmd/patchy/internal/cli out of reach of the repo root. It is NOT under cmd/
                    because hack/build.sh builds everything there.
internal/           All private code, one package per concern (see "Packages" below).
pkg/                PUBLIC plugin interfaces only: pkg/source (finding sources), pkg/enhance (context
                    enhancers), pkg/generic (the generic integration's HTTP wire contract, importable
                    by external processes), and pkg/evaluation (the remote-evaluation wire contract:
                    submissions, the in-pod EVOLVE-EVENT stream and the SSE monitor; stdlib-only,
                    imported by evolve). Exported signatures must not reference internal/ types.
deploy/             kustomize base and overlays; deploy/README.md is the operator doc. The container
                    Dockerfile.* files live at the repo root (goreleaser dockers_v2 builds them).
charts/             Helm rendering of the same stack, pushed to ghcr OCI on release
                    (.github/workflows/helm.yaml): charts/patchy (CRDs and controllers) and
                    charts/patchy-config (the Integration and Forge CRs). That is a separate chart
                    because helm validates CRs against CRDs that must already exist. release-please
                    stamps both Chart.yaml versions. Lint and render with `mise run helm-lint`.
patchy.yaml         `patchy build`'s config for patchy's own EDGE builds. On every push to main that cuts
                    no release, release.yaml's edge jobs publish the images (built from dist/images/,
                    which `mise run images-context` cross-compiles) and both charts to ghcr as
                    <next version>-edge.<UTC timestamp>.g<commit> (images with a v), through
                    github-workflows' patchy-publish action. Releases stay goreleaser's, not promoted.
e2e/                A SEPARATE Go module. envtest provides the CRDs, the real binaries run against it,
                    fakegithub (an in-memory API) stands in at the network edge, and recorded webhook
                    fixtures plus the replay tool drive it (`make e2e`). fakeoidc (a Google issuer)
                    and fakes3 (an S3 that verifies SigV4, header and presigned) stand in at the
                    other edges; fakes3's own tests run without envtest.
docs/ overrides/    Zensical docs site (zensical.toml at the root). The theme follows the status
                    page's design system: docs/stylesheets/tokens.css is a vendored copy of the
                    UI's tokens.css re-scoped to Material's schemes (copy it again when the palette
                    changes), extra.css maps the tokens onto Material, and overrides/ holds the
                    header, the inline PatchMark partial and the home and demo templates. Moved
                    pages keep their old URL through zensical.toml's redirect_maps. Run
                    `mise run serve` to preview and `mise run docs-build` to build; the shared
                    release workflow publishes it to GitHub Pages (oss.bitwisemedia.uk/patchy).
                    uv installs zensical (pyproject.toml / uv.lock). docs/whats-new/ is the
                    per-release highlights (see "What's new" below). docs/cli/ is GENERATED
                    (docgen, above): edit the commands' Short, Long and Example, not the markdown.
                    docs/operate/cli.md is the hand-written tour.
completions/        GENERATED shell completions. They are committed so the Homebrew cask installs
                    them as static files (they stay deterministic, and the cask never runs the
                    binary at install time). `mise run docs` writes both this and docs/cli/.

                    kubectl_complete-patchy is the exception. It is hand-written, and it is why
                    `kubectl patchy` completes: kubectl ignores a plugin's completion script and
                    runs kubectl_complete-<plugin> from PATH instead.
.mise/              Shared toolchain submodule (bitwise-media-group/toolchain): pinned dev CLIs and
                    the common and go task sets. Go itself is pinned in the root mise.toml. The
                    Makefile is a one-line forwarder. Tasks specific to this repo (multi-binary
                    build, e2e, envtest, codegen, replay) live in tasks.toml.
.claude/plans/      The living implementation plan (git-ignored).
```

## Packages (`internal/`)

- `controller/`: one engine per controller binary; the binaries are thin wiring over these.
  - `controller/integration`: receiver, ingest, projection, human signals, the BackfillRequest walker and the
    internal repository-access and GitHub-user handlers.
  - `controller/source`: the Forge and Repository reconcilers.
  - `controller/context`: the enhancer chain.
  - `controller/investigation`: the gate and the analysis scheduler.
  - `controller/remediation`: the spawner, the priority scheduler, and push and PR.
  - `controller/rollup`: all-time stats and the finding TTL. The remediation binary hosts it.
  - `controller/evaluation`: the Evaluation gate, the unit scheduler and the evaluation TTL. It is the only writer for
    both evaluation kinds. Their phases are local enums and never part of the Finding transition table.
- `kube`: the controller-runtime manager wrapper: scheme, kubeconfig or in-cluster config, leader election, the
  multi-namespace cache, health probes, and the logr to slog bridge. Secrets are never cached.
- `forge`: shared forge logic. It finds the `Forge` CR that covers a repository URL (host, then orgs, then repo
  regexes; the most specific match wins) and mints scoped read and write tokens. source uses it to read and
  remediation to write. `ghclient`, `ghpush` and `ghsecret` sit beneath it.
- `schedule`, `priority`, `stats`: pure logic. Slot picking with aging so nothing starves, the 0–100 scheduling score,
  and rollup delta arithmetic with OTel taps.
- `labels`: the small set of human-facing labels the issue projection writes. It is one-way: labels are never parsed
  back into state.
- `templates`: the finding handoff and issue body, both stage prompts, and the PR body. They are rendered from embedded
  templates and covered by golden tests.
- `webhook`, `telemetry`, `cli`, `version`: service plumbing. Only integration-controller uses the webhook server.
- `action`: the human-action vocabulary (the custom verbs) and the state-machine checks behind each one. `Apply`
  changes spec, and `Available` reports what a finding currently allows. It lives here so the status server and the
  CLI cannot drift apart; `web/authz` re-exports the verb constants. It decides whether an action makes sense, never
  whether the caller may take it. `MaintainedVerbs` maps each scoped maintainer verb onto the base verb it stands
  in for.
- `web` (with `web/auth`, `web/authz` and `web/ghlink`): the status-server backend.
  - Wire types that mirror the SPA's `ui/src/types.ts`. Keep the two in step.
  - The action handlers, the SSE broker and the cache-informer watcher.
  - The embedded UI (`internal/web/ui`, Preact on bun). `build.ts` bundles one inlined `index.html` with Bun.build,
    embedded behind the `withui` tag. `mise run ui` builds it, `mise run ui-dev` serves it with hot reload against a
    running status server, and a bare `go build` compiles a stub.
  - `auth` is who you are: OIDC, none, anonymous or unconfigured, plus cookie sessions, with no k8s imports. It also
    derives the GitHub link-cookie key (`LinkKey`: from the OIDC client secret, or `linkKeyFile` in modes anonymous and
    none). `authz` is what you may do: SubjectAccessReviews for the custom verbs (approve, retry, expedite, suspend,
    resume, backfill, replay, reset, and the scoped `*-maintained` verbs) and native get, 14 per identity.
  - `ghlink` is the optional "Connect GitHub" link: the GitHub App user-to-server OAuth flow (PKCE) on
    `/oauth2/github/<integration>/{authorize,callback,disconnect}`, one sealed `HttpOnly` cookie per Integration bound
    to the patchy username, and lazy single-flight token refresh. Every GitHub call goes through `ghuser`. Sign-in
    stays on the configured provider (dex issues its own tokens and never passes the GitHub token through).
- `ghas`, `enhancers`: the built-in `pkg/source` and `pkg/enhance` implementations.
- `generic`: the generic integration's behaviour over the `pkg/generic` wire contract. It holds the validating source
  handler (the source id is the Integration's NAME, so many integrations can coexist) and the HMAC-signing outbound
  client used by both the verdict resolver and the enhancer call. The enhancer fan-out itself is
  `enhancers.DynamicGeneric` / `MultiEnhancer`. That is internal; `pkg/enhance` stays one plugin, one identity.
- `harness`, `runner`: adapted from evolve. The harness builds argv, the runner executes it (it watches and collects,
  with a token-budget kill switch), and the harness parses stdout. Keep that separation.
- `agentrun`: the stage flow inside the pod (`investigate` or `remediate`). `report` and `envelope` are its contracts
  (frontmatter schemas in, JSONL events out). `agentresult` turns envelope results into CR status.
- `jobs`: the Kubernetes Job the agent runs in. The isolation model lives here, and the broker made it stronger:
  - A brokered (claude, codex) pod holds no credential of any kind. Its projected SA token (audience-bound, in the
    agent container only, never the init) is proof of identity, not a capability.
  - The SecretKeyRef credential channel survives only in the generic Runner shape. No shipped runner uses it.
  - The init container fetches the digest-verified artifact tarball with no identity at all.
  - `reservedEnv` covers every credential channel plus the provider gateway names. `Runner.Env` is the gateway env for
    each runner; it wins over `Config.Env` and can never name a credential.
  - `eval.go` is the evaluation flavour of the Job. It has the same security posture, but runs `evolve exec-unit`
    instead of `agent-runner` (wrapped in a one-time token export when brokered), with no git init and a unit.json
    handoff.
  - `ResultLines` is the log reader that ignores the envelope format; the evaluation collector decodes its own events
    from it.
- `broker`, `provider`: the egress-broker engine, and the pure logic for brokered claude and codex runners.
  - `broker`: TokenReview auth with a cache of results, credential strategies for each route, an SSE-safe reverse
    proxy, and the audit log.
  - `provider`: the claude gateway env; the codex `config.toml` render, carried as `PATCHY_CODEX_CONFIG`; the maps from
    canonical model ids to provider ids, with defaults derived for each provider; and the `PATCHY_MODEL_MAP` codec
    that both the controllers and agentrun use. `provider.BrokerTokenHeader` is the one definition of the caller-token
    header.
- `archive`, `runarchive`: optional long-term run archival (docs/deployment/archival.md).
  - `archive` is the cloud seam, with no k8s imports: a create-only `Store` (Put, Get, Presign) over S3
    (aws-sdk-go-v2, `If-None-Match: *`) or GCS (`DoesNotExist`). A retry finding the same sha256 succeeds; a different
    one is `ErrConflict`. `archivetest` is the in-memory fake.
  - `runarchive` assembles one run's objects under `<prefix>/<ns>/<finding>/<run>/<uid>/` (uncompacted transcript,
    full report, patch, changeset, manifest last, push.json after the push), byte-deterministic so collection retries
    converge. Its `Collector` is the decision both run controllers share: archive and record `stage.archive` (no
    ConfigMap), defer within the retry window, then fall back (fail open) or fail the run (`archive_failed`).
- `artifact`: the tarball store and the HTTP handler source-controller serves agent fetches from. It also holds the
  content-addressed workspace blobs:
  - bundles named by sha256 (64-hex files next to the 32-hex repo tarballs);
  - an index rebuilt from disk on restart, and a cleanup that removes blobs by last access;
  - the `:9791` internal upload handler, and the `Client` other processes use to reach it.
- `evalapi`: the evaluation controller's HTTP surface (the `pkg/evaluation` contract). It covers:
  - bearer OIDC verification (claims through `web/auth.MapClaims`) and SubjectAccessReview checks
    (`web/authz.ResourceReviewer`, native verbs on `evaluations`);
  - the workspace upload proxy and submission validation;
  - the snapshot, and the SSE monitor (replay, re-sending on change, and an explicit `end`).

  `evalresults` is the results ConfigMap store for each unit (a sibling of `transcriptstore`).

- `ghuser`: the wire contract of the integration-controller's internal GitHub-user endpoints
  (`/internal/v1/github-user/{exchange,refresh,repositories,authorize-repository}`) and the client the status server
  calls them with. Stdlib-only (plus `repoaccess`), so the status server never links the GitHub client. User tokens
  travel in bodies only and are never logged or stored by the controller. The handlers are
  `controller/integration/ghuser.go`.
- `ghpush`: replays the agent's changeset through the GitHub Git Data API (blob, tree, commit, ref). It is the only
  place a write credential is used. There is no git binary anywhere on the controller side.
- `mirror`: the engine behind `patchy mirror`. Only the CLI uses it; no controller does. It mirrors vendored upstream
  helm charts and OCI artifacts into one or more platform registries.
  - The patchy config's top-level `registries` lists them, with `signing` and the `scan` defaults next to it. The
    engine receives a resolved `spec.Config`. Every entry publishes to all registries, and lock files record the
    targets for each registry name. Each registry may carry its own `signing` override that replaces the default
    whole. `sync --registry` limits a run to the named registries.
  - The engine never touches git: the calling pipeline owns commits, branches and PRs. Only the upgrade path may read
    the wall clock, which keeps `validate`'s byte-identity check deterministic.
  - cosign (v3 or later) and osv-scanner (v2) are runtime dependencies of the verbs that use them. They are run as
    separate programs, never linked.

  It has one subpackage per concern:
  - `spec`: the manifest and lock schema, plus glob discovery (the directory tree is the list of entries). It also
    holds the shared `Config.Resolve` and the one `scan` block: config defaults, the per-entry `ScanOverride`, and
    `EffectiveScanPolicy`, which resolves entry, then config, then the built-in default.
  - `imageref`: parses, rewrites and glob-matches image references as strings.
  - `semverpick`: version constraints and cooldown walks.
  - `yamledit`: comment-preserving edits that splice bytes and never re-encode.
  - `helmchart`: pull, extract, tree diff and push.
  - `render`: a byte-stable equivalent of `helm template`. The helm SDK is pinned and upgraded only on purpose,
    because the validate check compares the output byte for byte.
  - `discover`: image discovery in four passes.
  - `distro`: turns distribution manifests into the generated images.extra.yaml sidecar.
  - `verify` and `sign`: run the cosign binary. Upstream provenance comes in, and signatures go out as bundle
    referrers. KMS support depends on the installed cosign build. `sign.Ensurer` is the shared verify-then-sign step.
  - `scan`: pluggable scanners (osv, grype, kubescape), all run as separate programs, by registry ref or local docker
    archive. `scan.Blocking` is the shared policy filter. Every image scanner is off by default, and an image scan
    with none enabled is a hard error.
  - `allowlist`: derives allowlists, keeping expiry dates and dropping stale entries, plus the shared `Lint`, `Active`
    and `Regenerate`.
  - `ocireg`: also has `Write` / `WriteIndex` for local builds.

- `build`: the engine behind `patchy build`. Only the CLI uses it.
  - It builds the config's `build.images` (docker buildx to an OCI layout) and `build.manifests` (a staged tree to a
    deterministic Flux OCI artifact). Each image's `secrets` reach buildx as `--secret` references: an env name or a
    file path, never a value.
  - It scans BEFORE any push. It publishes to every registry (`<url>/<repository>:<tag>`) and signs for each one. Tags
    never move: each result is pushed, skipped-current or skipped-tag-exists.
  - `--dry-run` needs no registries (`File.BuildPlanConfig`), so public repos commit none.
  - `Promote` (`patchy build promote`) adds a tag to the newest signed build that matches a glob, without rebuilding,
    and reports its revision annotation.
  - It reuses `mirror/{ocireg,scan,sign,spec,allowlist}` rather than the mirror engine itself. Image allowlists live
    at `security/allowlists/<name>.yaml`.

  Subpackages:
  - `expand`: pure templating for `${VAR}`, `${VAR:-d}`, `${VAR:o:l}` and `$$`, plus the `PATCHY_TIMESTAMP` built-in.
    It is fuzzed.
  - `buildx`: argv and a runner over `scan.ToolRunner`.
  - `fluxartifact`: staging with gitignore patterns, a deterministic tar.gz, and the Flux manifest with a `{}` config.

## Conventions

- Go 1.27; cobra and viper (`PATCHY_` env prefix); `log/slog` to stderr. stdout is reserved, because agent-runner's
  event stream lives there. OpenTelemetry with an otelslog fanout that never fails startup.
- Every package has a `doc.go`. Every file starts with the MIT SPDX header (enforced by revive and addlicense).
- Table-driven stdlib tests, no testify. Prefer fakes to mocks. Use the controller-runtime fake client for reconciler
  tests. envtest suites skip without `KUBEBUILDER_ASSETS` (`mise run envtest`, `mise run e2e`).
- Conventional Commits. release-please and goreleaser drive releases. `make pr` is the local gate.
- The harness and runner packages are adapted from `../evolve` (`internal/harness`, `internal/runner`). Keep their
  "harness builds argv, runner executes, harness parses stdout" separation intact.

## What's new (maintain with every feature)

The docs site has a **What's new** section: one page per release that sells what the release adds, with screenshots,
benchmark numbers and upgrade notes. It is not the changelog. release-please writes `CHANGELOG.md` from the commits;
this page picks the highlights and says why they matter. `docs/whats-new/index.md` is the current release's page and
the section's landing page (`/whats-new/`). Earlier releases are frozen at `docs/whats-new/<version>.md` and listed in
the `zensical.toml` nav under "What's new", newest first. The docs site publishes only on a release, so readers always
see a released version there.

**Every `feat` commit updates it, in the same commit.** So does a `fix` or `perf` commit a user would notice. Add or
extend a section on `index.md`:

- Lead with what the reader gains, then how. Use plain words and link the reference docs for the details.
- Back claims with numbers you measured: benchmarks (`mise run bench`) or load tests (`mise run load`). Say how they were
  measured, and never invent a figure.
- Show UI changes. Declare each shot in `index.md`'s front matter `screenshots` list (a `name`, the `hash` route to
  load in the demo, and optionally text to `type` into the findings filter), and reference its dark and light images as
  `../assets/images/whats-new/<version>/<name>.jpg#only-dark` and `<name>-light.jpg#only-light`, `<version>` being the
  front matter `version`. **Never commit screenshots.** The release PR's workflow (`release-screenshots.yaml`) shoots
  every declared shot and the docs' standing screenshots, and commits them to the release PR, so the images do not
  exist until then. The paths are git-ignored, and `mise run ui-screenshots` writes a local preview to the git-ignored
  `internal/web/ui/screenshots/` (it needs Chrome, which the Claude Code sandbox blocks). Images already tracked are
  not covered by the ignore, so if a run with `--out docs/assets/images` changes them, restore them before committing.
  The screenshots are stored in Git LFS (`.gitattributes`), so install git-lfs to see them in a local docs preview.
- Breaking changes get an entry under "Upgrading" that says what to change.

**The version is release-please's.** The page's version lives in its front matter, twice: `version`, and the `title`
that Zensical renders as the page heading. Both lines carry an `x-release-please-version` comment, as does the home
page's "What's new" button (`overrides/home.html`), and release-please's `extra-files` rewrite those lines when it opens
a release PR. Keep every other version string, image paths included, off the marked lines.

**A release freezes its page automatically.** When a release is cut, `release.yaml` calls
`.github/workflows/whats-new.yaml`, which runs `hack/whats-new-freeze.sh <version>`:

- It copies `index.md` to `docs/whats-new/<version>.md`, without the markers and with a link to the release.
- It lists the copy in the `zensical.toml` nav, newest first.
- It resets `index.md` to a placeholder (a one-paragraph maintenance-release page) for the next patch version.

The result arrives as a pull request ("docs: freeze What's new for `<version>`") labelled `auto-merge`, signed by the
FF Merge App. Approve it before the next release PR merges, or that release would be stamped onto the frozen release's
page. A release with nothing to highlight (fixes and Renovate updates) publishes the placeholder, stamped with its
version, instead of relabelling the last release's highlights. It gets no versioned page: when the page is still the
untouched placeholder at release time, the script changes nothing and no pull request opens. Detection is an exact
match on the placeholder text the script writes, so leave that text as it is until you replace it.

**Before you edit `index.md`, compare its front matter `version` with `.release-please-manifest.json`:**

- **Newer than the manifest:** the page is the next release's. Add to it, replacing the maintenance paragraph with your
  highlights if it is still there. A breaking change bumps the minor version before 1.0 (any other change bumps the
  patch), so set both front matter lines to match; the release PR corrects them either way.
- **Equal to the manifest, and the page is still the placeholder:** the last release had nothing to highlight. Replace
  the placeholder with your highlights and set both front matter lines to the next version.
- **Equal to the manifest, with highlights:** the freeze pull request has not merged. Merge it first. If there is none,
  rerun the What's New workflow from the Actions tab with that version, or run `hack/whats-new-freeze.sh <version>`
  locally and commit the result.

## State machine (the heart of the system)

`api/v1alpha1` owns the phases and the legal transitions (`transitions.go`: `CanTransition`, `Terminal`, `SetPhase`).
Every phase edge has exactly one writer component. The flow is
`Opened → Enhanced → Investigating → Queued → Remediating → InReview → Remediated`:

- `AwaitingApproval` comes before `Queued` when a finding is held.
- `Dismissed`, `HandedOff` and `Failed` are terminal. `HandedOff` can be revived by an approval. `Failed` can be
  revived by a human retry, which returns it to the state before the failure.
- `spec.expedite` skips accumulation and the minimum age, and moves the finding to the front of both schedulers'
  queues.

Accumulation is a condition (`AccumulationComplete`), not a phase. `BackfillRequest` phases (`AwaitingApproval`,
`Pending`, `Complete`, `Failed`, `Denied`) are a local enum with the integration-controller as their only writer, like
the evaluation kinds, and never part of the Finding transition table. See DESIGN.md for the full flow, and
[docs/concepts/lifecycle.md](docs/concepts/lifecycle.md) for the transition table with its writers.
