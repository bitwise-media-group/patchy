# patchy

An end-to-end workflow for triaging and remediating security findings from multiple sources, using Kubernetes custom
resources as the state machine and GitHub issues as a human-facing projection.

Findings arrive by webhook from GitHub code scanning, Google Cloud Security Command Center, Wiz, or any scanner through
the generic HTTP integration. They accumulate into `Finding` resources for an hour, and each one is projected to a
GitHub tracking issue. Patchy adds ownership and infrastructure context to each finding. Then a sandboxed coding agent
investigates it:

- False positives are dismissed.
- Low-confidence or human-only work is handed to the repository owner.
- High-confidence fixes are queued in priority order, attempted automatically, and opened as pull requests for human
  review.

Completed findings expire on a TTL, and `FindingRollup` resources keep the all-time statistics.

## Components

| Binary                      | Concern                                                                                                                                    |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `integration-controller`    | The one internet-facing entry point: validate provider webhooks, ingest alerts into Findings, project tracking issues, apply human signals |
| `source-controller`         | `Forge`/`Repository` reconcilers: resolve credentials, pin the head SHA, serve the repository tarball agents fetch without credentials     |
| `context-controller`        | The enhancer chain: Google Cloud, AWS and Azure resource lookups, generic HTTP enhancers, and a static context file                        |
| `investigation-controller`  | Gate eligible findings, run analysis agent Jobs, route the verdicts                                                                        |
| `remediation-controller`    | Queue admission, priority-ordered remediation Jobs, changeset push + pull requests, rollup statistics and the finding TTL                  |
| `agent-runner`              | In-pod coding-agent runtime: investigate or remediate with `claude -p` or `codex exec`                                                     |
| `egress-broker`             | The proxy every agent model request goes through: holds the model credential, so agent pods hold none                                      |
| `status-server`             | The human-facing status page: findings and metrics, OIDC sign-in, the RBAC-gated actions                                                   |
| `evaluation-controller`     | Optional: runs evolve skill evaluations remotely on the same sandboxed agent Jobs                                                          |
| `patchy` / `kubectl-patchy` | The workstation CLI: list, review and act on findings with your own kubeconfig, never through a controller                                 |

See [DESIGN.md](DESIGN.md) for the full requirements and the state machine, and [AGENTS.md](AGENTS.md) for a map of the
repository. The full documentation (getting started, configuration and deployment references) is at
[oss.bitwisemedia.uk/patchy](https://oss.bitwisemedia.uk/patchy/). The source is under [`docs/`](docs/). To preview it
locally, run `mise run serve`.

## Building

The toolchain comes from the `.mise/` submodule ([mise](https://mise.jdx.dev) provisions the pinned tools):

```sh
git submodule update --init
make build   # every binary into bin/
make test    # unit tests with race + coverage
make lint    # golangci-lint, govulncheck, license headers, prose linters
make pr      # the full local gate before a pull request
```

## Testing

```sh
make test    # unit tests (race + coverage)
make e2e     # envtest + the real binaries, driven by recorded webhooks against an in-memory GitHub
```

## Deploying

Container images and kustomize manifests live in [`deploy/`](deploy/). `deploy/README.md` covers the GitHub App setup,
the secrets and custom resources to create, and how the agent's isolation works.

```sh
kubectl apply -k deploy/kustomize/overlays/dev
```

Helm charts that render the same stack are published to OCI on every release. [`charts/patchy`](charts/patchy/README.md)
is the stack. [`charts/patchy-config`](charts/patchy-config/README.md) carries the Integration/Forge custom resources,
and you install it as a second release once the CRDs exist:

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy --namespace patchy --create-namespace
```

## The CLI

The workstation CLI ships separately from the cluster components. You can get it as a Homebrew cask, or as the
`patchy-cli` release archive where brew is not available:

```sh
brew install bitwise-media-group/tap/patchy
```

It talks to the Kubernetes API with your own kubeconfig, so you can do only what your RBAC allows. The cask also
installs `kubectl-patchy`, so every command works as `kubectl patchy …`, with shell completion for both spellings. See
[`docs/operate/cli.md`](docs/operate/cli.md) for the tour and [`docs/cli/patchy.md`](docs/cli/patchy.md) for the
generated command reference.

## Status

All components are implemented and tested end to end. Ingestion (`pkg/source`) and context enhancement (`pkg/enhance`)
are plugin interfaces. The [extending guide](https://oss.bitwisemedia.uk/patchy/extending/) covers both. The generic
HTTP integration connects an external process without changing patchy.
