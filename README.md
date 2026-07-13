# patchy

An end-to-end workflow for triaging and remediating security findings from multiple sources, using GitHub issues as the
state machine.

Findings (GitHub Advanced Security / CodeQL first) arrive via webhook and accumulate into GitHub issues for an hour.
Issues are enhanced with ownership context, then a sandboxed coding agent classifies each one — false positives are
dismissed, low-confidence or human-only work is assigned to the repository owner, and high-confidence remediations are
attempted automatically and opened as pull requests for human review.

## Components

| Binary                   | Concern                                                                 |
| ------------------------ | ----------------------------------------------------------------------- |
| `source-controller`      | Receive findings via webhooks, open + accumulate issues (1-hour window) |
| `context-controller`     | Enhance new issues with CMDB ownership / infrastructure context         |
| `remediation-controller` | Run agent Jobs in Kubernetes and apply all GitHub side effects          |
| `agent-runner`           | In-pod coding-agent runtime: classify, then remediate via `claude -p`   |

See [DESIGN.md](DESIGN.md) for the full requirements and label taxonomy, and [AGENTS.md](AGENTS.md) for a map of the
repository.

## Building

The toolchain comes from the `.mise/` submodule ([mise](https://mise.jdx.dev) provisions the pinned tools):

```sh
git submodule update --init
make build   # all four binaries into bin/
make test    # unit tests with race + coverage
make lint    # golangci-lint, govulncheck, license headers, prose linters
make pr      # the full local gate before a pull request
```

## Testing

```sh
make test    # unit tests (race + coverage)
make e2e     # the real binaries, driven by recorded webhooks against an in-memory GitHub
```

## Deploying

Container images and kustomize manifests live in [`deploy/`](deploy/); `deploy/README.md` covers the GitHub App setup,
the secrets to create, and how the agent's isolation actually works.

```sh
kubectl apply -k deploy/kustomize/overlays/dev
```

A Helm chart rendering the same stack is published to OCI on every release — see [`helm/chart`](helm/chart/README.md):

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy --namespace patchy --create-namespace
```

## Status

All four components are implemented and tested end to end. The context enhancer is a deliberate placeholder — the plugin
seam (`pkg/enhance`) and a YAML-backed static enhancer ship, and a real CMDB integration implements the same interface.
