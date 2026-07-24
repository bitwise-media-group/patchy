# patchy

An end-to-end workflow for triaging and remediating security findings from multiple sources, using Kubernetes custom
resources as the state machine and GitHub issues as a human-facing projection.

Findings (GitHub Advanced Security / CodeQL first) arrive via webhook and accumulate into `Finding` resources for an
hour, each projected to a GitHub tracking issue. Findings are enhanced with ownership context, then a sandboxed coding
agent investigates each one — false positives are dismissed, low-confidence or human-only work is handed to the
repository owner, and high-confidence remediations are queued in priority order, attempted automatically, and opened as
pull requests for human review. Completed findings expire on a TTL while `FindingRollup` resources keep the all-time
statistics.

## Components

| Binary                     | Concern                                                                                                                                    |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `integration-controller`   | The one internet-facing entry point: validate provider webhooks, ingest alerts into Findings, project tracking issues, apply human signals |
| `source-controller`        | `Forge`/`Repository` reconcilers: resolve credentials, pin the head SHA, serve the repository tarball agents fetch credential-lessly       |
| `context-controller`       | Enhance freshly opened Findings with CMDB ownership / infrastructure context                                                               |
| `investigation-controller` | Gate eligible findings, run analysis agent Jobs, route the verdicts                                                                        |
| `remediation-controller`   | Queue admission, priority-ordered remediation Jobs, changeset push + pull requests, rollup statistics and the finding TTL                  |
| `agent-runner`             | In-pod coding-agent runtime: investigate or remediate via `claude -p`                                                                      |
| `status-server`            | The human-facing status page: findings/rollup projection, OIDC sign-in, the RBAC-gated actions                                             |
| `patchy`                   | The workstation CLI: list, review and act on findings over your own kubeconfig — never through a controller                                |

See [DESIGN.md](DESIGN.md) for the full requirements and the state machine, and [AGENTS.md](AGENTS.md) for a map of the
repository. Full documentation — getting started, configuration and deployment references — lives at
[oss.bitwisemedia.uk/patchy](https://oss.bitwisemedia.uk/patchy/) (source under [`docs/`](docs/); build locally with
`mise run serve`).

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

Container images and kustomize manifests live in [`deploy/`](deploy/); `deploy/README.md` covers the GitHub App setup,
the secrets and custom resources to create, and how the agent's isolation actually works.

```sh
kubectl apply -k deploy/kustomize/overlays/dev
```

Helm charts rendering the same stack are published to OCI on every release — [`charts/patchy`](charts/patchy/README.md)
is the stack; [`charts/patchy-config`](charts/patchy-config/README.md) carries the Integration/Forge custom resources,
installed as a second release once the CRDs exist:

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy --namespace patchy --create-namespace
```

## Status

All components are implemented and tested end to end. The context enhancer is a deliberate placeholder — the plugin seam
(`pkg/enhance`) and a YAML-backed static enhancer ship, and a real CMDB integration implements the same interface.
