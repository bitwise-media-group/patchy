<!--
Copyright 2026 Bitwise Media Group Ltd.
SPDX-License-Identifier: MIT
-->

# patchy Helm chart

Deploys the patchy stack: the three controllers (source, context, remediation) into the release namespace, plus the
agent sandbox namespace, RBAC, ConfigMap, Services, and NetworkPolicies. It is the Helm rendering of
[`deploy/kustomize`](../../deploy/kustomize) — same resources, same defaults, same isolation model — published to OCI on
every release.

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
    --version <X.Y.Z> --namespace patchy --create-namespace
```

The chart version tracks the app release 1:1, and the default image tag is `v<appVersion>` — installing chart `X.Y.Z`
runs images `vX.Y.Z`.

## Prerequisites

Three Secrets, created out of band (SOPS, external-secrets, or `kubectl` for dev) — the chart references them and
refuses to own them. See
[`deploy/kustomize/base/secrets.example.yaml`](../../deploy/kustomize/base/secrets.example.yaml) for shapes and
one-liners:

| Secret                  | Namespace         | Keys                        | What                                 |
| ----------------------- | ----------------- | --------------------------- | ------------------------------------ |
| `patchy-github-app`     | release namespace | `app-id`, `private-key.pem` | The GitHub App identity              |
| `patchy-webhook-secret` | release namespace | `secret`                    | The webhook HMAC secret              |
| `patchy-anthropic`      | `patchy-agents`   | `api-key`                   | The model API key for the agent Jobs |

Each controller then needs its own webhook URL configured on the GitHub App (`POST /webhook` on its Service, port 8080),
all signed with the same secret. Exposing them to GitHub is your cluster's business — put an Ingress or Gateway in
front; the chart deliberately ships none.

## Values worth knowing

Defaults mirror the kustomize base; see [`values.yaml`](values.yaml) for the full annotated list.

- `image.*` — registry/repository prefix, tag (default `v<appVersion>`), pull policy. Per-component overrides live at
  `controllers.<name>.image` and `agentRunner.image`; setting a `digest` pins that image. Pinning the agent-runner
  digest also updates `PATCHY_AGENT_IMAGE`, the string the remediation-controller stamps into every Job — one knob,
  unlike kustomize's two.
- `config.*` — the `PATCHY_*` settings surface (accumulation window, pickup age, confidence threshold, both agent
  stages' models/budgets). `config.extra` renders arbitrary `PATCHY_*` keys and wins over anything the chart derives.
- `agent.*` — the sandbox namespace (created by the chart with the `restricted` Pod Security labels; `helm uninstall`
  deletes it, killing any running agent Job) and the agent service account.
- `networkPolicy.*` — the base L3/L4 policies are always on (`enabled: true`). For hostname-level egress on the agent
  sandbox enable exactly one of `networkPolicy.cilium.enabled` (FQDN policy, needs Cilium's DNS proxy) or
  `networkPolicy.istio.enabled` (REGISTRY_ONLY sidecar, needs native sidecars + the Istio CNI node agent). Adjust
  `networkPolicy.clusterCIDRs` to your cluster and `networkPolicy.agentHosts` for GHES. Either way, credential absence —
  the agent container never holds a GitHub token — is the real control.
- `service.type` / `service.nodePorts` — `ClusterIP` by default; NodePort covers the kind/dev flow.

Do not scale the controllers: all three are singletons by construction (the state machine is GitHub issue labels and
there is no leader election), so the Deployments hardcode `replicas: 1` with `strategy: Recreate`.

## Publishing

`helm/chart` is packaged and pushed to `oci://ghcr.io/bitwise-media-group/patchy/charts` by
[`.github/workflows/helm.yaml`](../../.github/workflows/helm.yaml) when a release is published; release-please stamps
`version`/`appVersion` in `Chart.yaml` as part of the release PR. Lint locally with `mise run helm-lint`.
