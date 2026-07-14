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

The GitHub App has exactly one webhook URL; point it at `https://<webhook.host>/webhook` and enable one flavour of the
chart's entry point — `webhook.ingress` (plain Ingress, works anywhere) or `webhook.httpRoute` (Gateway API). Both front
the **webhook-controller**, the only internet-facing component: it validates each delivery's HMAC signature and routes
it to the controllers that consume its event type, and it holds no GitHub credential. See the
[webhook exposure docs](../../docs/deployment/webhook.md) for details and per-platform (EKS, AKS, GKE) notes.

## Values worth knowing

Defaults mirror the kustomize base; see [`values.yaml`](values.yaml) for the full annotated list, validated by
[`values.schema.json`](values.schema.json). The layout follows the flux-operator convention: everything scoped to one
controller lives under its top-level key — `sourceController`, `contextController`, `remediationController` — and each
block has the same shape:

- `<controller>.image` — key-by-key override of the global `image.*` prefix/tag; a `digest` pins that image.
- `<controller>.config` — the `PATCHY_*` keys that binary binds (source: accumulation window; context: enhance grace;
  remediation: pickup age, confidence threshold, both agent stages' models/budgets), rendered into a per-controller
  ConfigMap. `config.*` holds the shared keys (log level, reconcile interval), each overridable per controller by
  repeating it under `<controller>.config`; `config.extra` and `<controller>.config.extra` render arbitrary `PATCHY_*`
  keys and win over anything the chart derives.
- `<controller>.serviceAccount` / `service` / `networkPolicy` — that controller's identity, Service (NodePort covers the
  kind/dev flow), and L3/L4 policy.
- `<controller>.resources`, `podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity` — per-controller
  pod tuning.

The genuinely shared settings stay global:

- `image.*` — repository prefix (registry included), tag (default `v<appVersion>`), pull policy, pull secrets.
- `webhook.*` — the single external entry point (`host`, plus one of `ingress` / `httpRoute`) in front of the
  webhook-controller; a GitHub App has one webhook URL, so exposure is a chart-level concern, not a per-controller one.
- `webhookController.*` — the routing entry point itself: image override, `replicas` (default 2 — it is stateless,
  unlike the controllers), `config.forwardTimeout`, and the usual serviceAccount/service/networkPolicy/scheduling knobs.
- `commonLabels` / `commonAnnotations` — stamped on every object the chart renders (annotations reach the pods too;
  per-object annotations win key-by-key).
- `agent.*` — the sandbox: namespace (created by the chart with the `restricted` Pod Security labels; `helm uninstall`
  deletes it, killing any running agent Job), the agent service account, and `agent.image`, the agent-runner image the
  remediation-controller stamps into every Job (`PATCHY_AGENT_IMAGE`) — pinning its digest is one knob, unlike
  kustomize's two.
- `agent.networkPolicy.*` — the sandbox policies (default-deny + TCP-443-only egress). For hostname-level egress enable
  exactly one of `agent.networkPolicy.cilium.enabled` (FQDN policy, needs Cilium's DNS proxy) or
  `agent.networkPolicy.istio.enabled` (REGISTRY_ONLY sidecar, needs native sidecars + the Istio CNI node agent). Adjust
  `clusterCIDRs` to your cluster and `hosts` for GHES. Either way, credential absence — the agent container never holds
  a GitHub token — is the real control.

Do not scale the controllers: all three are singletons by construction (the state machine is GitHub issue labels and
there is no leader election), so the Deployments hardcode `replicas: 1` with `strategy: Recreate`.

## Publishing

`helm/chart` is packaged and pushed to `oci://ghcr.io/bitwise-media-group/patchy/charts` by
[`.github/workflows/helm.yaml`](../../.github/workflows/helm.yaml) when a release is published; release-please stamps
`version`/`appVersion` in `Chart.yaml` as part of the release PR. Lint locally with `mise run helm-lint`.
