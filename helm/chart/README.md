<!--
Copyright 2026 Bitwise Media Group Ltd.
SPDX-License-Identifier: MIT
-->

# patchy Helm chart

Deploys the patchy stack: the `patchy.bitwisemedia.uk` CRDs and the five controllers (integration, source, context,
investigation, remediation) into the release namespace, plus the agent sandbox namespace, RBAC, ConfigMaps, Services,
and NetworkPolicies. It is the Helm rendering of [`deploy/kustomize`](../../deploy/kustomize) — same resources, same
defaults, same isolation model — published to OCI on every release.

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
    --version <X.Y.Z> --namespace patchy --create-namespace
```

The chart version tracks the app release 1:1, and the default image tag is `v<appVersion>` — installing chart `X.Y.Z`
runs images `vX.Y.Z`.

## Architecture

The custom resources are the state machine: findings, investigations, and remediations live as CRs in the release
namespace, and the Kubernetes API is the only state store (`kubectl get patchy -n <namespace>` shows the pipeline). The
five controllers split the work:

| Controller                   | Role                                                                                                                                                                                           |
| ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **integration-controller**   | The single internet-facing entry point: provider webhook receivers on :8080 (GitHub: `POST /github/webhooks`), scanner-alert ingestion into Findings, tracking-issue projection, human signals |
| **source-controller**        | Forge/Repository reconcilers plus the artifact server on :9790 — SHA-pinned repository tarballs the agent pods fetch credential-lessly                                                         |
| **context-controller**       | The enhancer chain over Opened findings (the CMDB placeholder); no external access at all                                                                                                      |
| **investigation-controller** | The gate and the analysis scheduler: launches analysis agent Jobs, routes the verdict edges                                                                                                    |
| **remediation-controller**   | Queue admission, the remediation agent Jobs, push/PR, and the rollup/TTL loop                                                                                                                  |

All five run as singletons (`replicas: 1` + `Recreate`, with leader election as rollout insurance) and mount their
service-account tokens; [`templates/rbac.yaml`](templates/rbac.yaml) pins verb-by-verb what each identity may do. Only
two Services exist — the integration-controller's :8080 and the source-controller's :9790; every other port is a
kubelet-probed :8081.

The CRDs render as templates gated by `crds.install` — living in `templates/crds/` rather than the chart's
install-only `crds/` directory means `helm upgrade` keeps them current — with `helm.sh/resource-policy: keep` stamped
when `crds.keep` is true so uninstall never deletes them — and with them the all-time FindingRollup statistics.

## Switching the pipeline on

The controllers idle until two custom resources exist: an **Integration** (where findings come from, where the tracking
issues go, webhook validation) and a **Forge** (how repositories are cloned and pushed). Render them from values:

```yaml
integrations:
  - name: github
    spec:
      provider: github
      secretRef:
        name: patchy-github
      interval: 10m
      github:
        issues:
          enabled: true
          approveComment: /approve
        codeScanningAlerts:
          enabled: true

forges:
  - name: github
    spec:
      provider: github
      secretRef:
        name: patchy-github
      interval: 10m
```

Each entry's `spec` is rendered verbatim and validated server-side by the CRD schema — see
[`deploy/kustomize/base/crs.example.yaml`](../../deploy/kustomize/base/crs.example.yaml) for the full field walkthrough
(GHES base URLs, org allowlists, repository regexes).

## Secrets

Created out of band (SOPS, external-secrets, or `kubectl` for dev) — the chart references them and refuses to own them.
See [`deploy/kustomize/base/secrets.example.yaml`](../../deploy/kustomize/base/secrets.example.yaml) for shapes and
one-liners:

| Secret               | Namespace         | Keys                                                 | What                                                                                                                                                       |
| -------------------- | ----------------- | ---------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| e.g. `patchy-github` | release namespace | `appID` + `privateKey` (or `token`), `webhookSecret` | The forge/provider credential, named by each Integration/Forge CR's `spec.secretRef` — **not** mounted into any Deployment; read on demand through the API |
| `patchy-anthropic`   | `patchy-agents`   | `api-key`                                            | The model credential for the agent Jobs — an Anthropic API key, or a `claude setup-token` OAuth token with `anthropic.secretEnv: CLAUDE_CODE_OAUTH_TOKEN`  |

One GitHub Secret may serve both CRs, or you can split read and write identities across two GitHub Apps. The provider
has exactly one webhook URL; point it at `https://<webhook.host>/github/webhooks` and enable one flavour of the chart's
entry point — `webhook.ingress` (plain Ingress, works anywhere) or `webhook.httpRoute` (Gateway API). Both front the
**integration-controller**, which validates each delivery against the matching Integration's `webhookSecret`. See the
[webhook exposure docs](../../docs/deployment/webhook.md) for details and per-platform (EKS, AKS, GKE) notes.

## Agent isolation

The agent Jobs run in their own namespace (`agent.namespace`, created by the chart with the `restricted` Pod Security
labels; `helm uninstall` deletes it, killing any running agent Job). The isolation model, in order of load-bearing:

1. **Credential absence** — the agent pod holds no forge credential at all, not even in an init container. The
   repository arrives as a digest-verified tarball fetched from the source-controller's in-cluster artifact server
   (:9790); the only Secrets in the pod are the model credential and the per-Job handoff markdown. The agent
   ServiceAccount has no Role and its token is not mounted.
2. **NetworkPolicy** (`agent.networkPolicy.create`) — default-deny both directions, re-permitting only DNS, the artifact
   server, and TCP 443 externally (`claude -p` → `api.anthropic.com`), with `clusterCIDRs` excluded.
3. **Hostname policy** (defence in depth) — enable exactly one of `agent.networkPolicy.cilium.enabled` (FQDN policy,
   needs Cilium's DNS proxy) or `agent.networkPolicy.istio.enabled` (REGISTRY_ONLY sidecar, needs native sidecars + the
   Istio CNI node agent). Both allow only `api.anthropic.com` and the in-cluster artifact endpoint — no GitHub hosts,
   because the pod never talks to GitHub.

## Values worth knowing

Per-controller blocks — `integrationController`, `sourceController`, `contextController`, `investigationController`,
`remediationController` — all share one shape:

- `<controller>.image` — key-by-key override of the global `image.*` prefix/tag; a `digest` pins that image.
- `<controller>.config` — the `PATCHY_*` keys that binary binds (integration: `accumulationWindow`; investigation:
  `findingMinAge`, `maxConcurrentInvestigations`, `confidenceThreshold`; remediation: `maxConcurrentRemediations`,
  `findingTTL`), rendered into a per-controller ConfigMap. `config.*` holds the shared keys (`logLevel`, `maxAttempts`,
  `priorityAgingInterval`, `priorityAgingCap`), each overridable per controller by repeating it under
  `<controller>.config`; `config.extra` and `<controller>.config.extra` render arbitrary `PATCHY_*` keys and win over
  anything the chart derives.
- `<controller>.serviceAccount` / `networkPolicy` — that controller's identity and L3/L4 policy; `service` exists only
  on the two controllers anything dials (integration :8080, source :9790; NodePort covers the kind/dev flow).
- `<controller>.resources`, `podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity` — per-controller
  pod tuning.

The genuinely shared settings stay global:

- `image.*` — repository prefix (registry included), tag (default `v<appVersion>`), pull policy, pull secrets.
- `integrations` / `forges` — the pipeline switch-on CRs (above).
- `webhook.*` — the single external entry point (`host`, plus one of `ingress` / `httpRoute`) in front of the
  integration-controller; a provider has one webhook URL, so exposure is a chart-level concern.
- `anthropic.*` — the model-credential Secret name/key and the env var it is injected as.
- `agent.*` — the sandbox: namespace, service account, `agent.image` (the agent-runner image both job controllers stamp
  into every Job as `PATCHY_AGENT_IMAGE` — pinning its digest is one knob, unlike kustomize's two),
  `jobDeadline`/`jobTTL`, and the two stages' limits: `modelAllowlist`, `investigate.*` (absolute), `remediate.*`
  (`maxTurns`/`tokenBudget` are ceilings the investigation report's requests are clamped to).
- `agent.networkPolicy.*` — the sandbox policies (above).
- `commonLabels` / `commonAnnotations` — stamped on every object the chart renders (annotations reach the pods too;
  per-object annotations win key-by-key).
- `crds.install` / `crds.keep` — CRD lifecycle.

Do not scale the controllers: all five are singletons by construction, so the Deployments hardcode `replicas: 1` with
`strategy: Recreate`; the leader-election Lease is insurance against a botched rollout, not a scaling mechanism.

## Publishing

`helm/chart` is packaged and pushed to `oci://ghcr.io/bitwise-media-group/patchy/charts` by
[`.github/workflows/helm.yaml`](../../.github/workflows/helm.yaml) when a release is published; release-please stamps
`version`/`appVersion` in `Chart.yaml` as part of the release PR. Lint locally with `mise run helm-lint`.
