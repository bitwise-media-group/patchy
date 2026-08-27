# Install with Helm

Two charts are published to GHCR as OCI artifacts on every release: `patchy` installs the whole stack — the
`patchy.bitwisemedia.uk` CRDs, five controller Deployments with their RBAC and ConfigMaps, the two Services, the agent
namespace, and the baseline network policies — and `patchy-config` installs the `Integration`/`Forge` custom resources
that switch the pipeline on, as a second release once the CRDs exist.

## Create the namespaces

The chart creates the agent namespace (`patchy-agents`) itself, but the release namespace is yours. Both must carry the
`restricted` Pod Security Standard — the chart labels the agent namespace; label the release namespace yourself:

```sh
kubectl create namespace patchy
kubectl label namespace patchy \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/warn=restricted
```

## Create the secrets

Patchy references two Secrets but refuses to own them — create them out of band (SOPS, external-secrets, or plain
`kubectl` for a first run). One lives in the release namespace, one in the agent namespace:

```sh
# The GitHub credential + webhook secret (all three values from the previous page)
kubectl -n patchy create secret generic patchy-github \
  --from-literal=appID=123456 \
  --from-file=privateKey=./patchy.private-key.pem \
  --from-literal=webhookSecret="$WEBHOOK_SECRET"

# The claude model credential — in the RELEASE namespace, where the egress
# credential broker (the only thing that ever holds it) runs. It never
# enters an agent pod.
kubectl -n patchy create secret generic patchy-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

No Anthropic API key? A Claude subscription works too: mint a long-lived OAuth token with
[`claude setup-token`](https://code.claude.com/docs/en/cli-reference), store it in the same secret, and set
`egressBroker.anthropicAuth: token` (Helm) or `PATCHY_ANTHROPIC_AUTH=token` (kustomize) so the broker sends it as a
bearer rather than an API key:

```sh
kubectl -n patchy create secret generic patchy-anthropic \
  --from-literal=api-key="$(claude setup-token)"
```

Running claude against Amazon Bedrock, GCP Vertex AI, or Microsoft Foundry instead needs no Anthropic Secret at all —
see the [provider recipes](../deployment/helm.md#model-providers-brokered-claude).

To let the investigation choose OpenAI models for remediation, enable the codex runner
(`agent.runners.codex.enabled: true` / add `codex` to `PATCHY_HARNESSES`) and create its credential:

```sh
kubectl -n patchy-agents create secret generic patchy-openai --from-literal=api-key="$OPENAI_API_KEY"
```

The copilot runner (`agent.runners.copilot.enabled: true` / add `copilot` to `PATCHY_HARNESSES`) is the alternative to
both: it brokers Anthropic **and** OpenAI models, so one credential covers every model in the registry. That credential
is a GitHub token rather than a model API key, which is why it is off by default — scope it to Copilot with no
repository permissions (the CLI rejects classic PATs; use a fine-grained token):

```sh
kubectl -n patchy-agents create secret generic patchy-copilot --from-literal=token="$COPILOT_GITHUB_TOKEN"
```

| Secret             | Namespace       | Keys                                                 | Consumed by                                                                                                                              |
| ------------------ | --------------- | ---------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `patchy-github`    | `patchy`        | `appID` + `privateKey` (or `token`), `webhookSecret` | The `Integration`/`Forge` CRs' `spec.secretRef` — read on demand through the API, never mounted                                          |
| `patchy-anthropic` | `patchy`        | `api-key`                                            | The egress broker only — never an agent pod (an API key, or a `claude setup-token` OAuth token with `egressBroker.anthropicAuth: token`) |
| `patchy-openai`    | `patchy-agents` | `api-key`                                            | Codex runner Job pods (`OPENAI_API_KEY`) — only when the codex runner is enabled                                                         |
| `patchy-copilot`   | `patchy-agents` | `token`                                              | Copilot runner Job pods (`COPILOT_GITHUB_TOKEN`) — only when the copilot runner is enabled                                               |

A `token` key (a personal access token) is the dev-only fallback and wins over App auth when set. One GitHub Secret may
serve both CRs, or you can split read and write identities across two GitHub Apps and two Secrets.

!!! warning "Each enabled non-brokered harness needs its credential"

    A codex or copilot harness is enabled only when its credential Secret exists in the agent namespace (the
    controllers validate this at startup and refuse to start otherwise); the Job builder wires that credential into the
    pod via a `secretKeyRef`. The claude harness is different: it is enabled by configuration alone — its credential
    lives with the egress broker, whose readiness probe is where a missing key surfaces. The `fake` harness (dev only)
    needs no credential.

There is deliberately **no** GitHub credential in the agent namespace — not even a per-Job one. The repository arrives
as a digest-verified tarball from the source-controller's in-cluster artifact server. See the
[isolation model](../deployment/isolation.md).

## Install the chart

The `patchy` chart installs the stack — CRDs, controllers, agent sandbox — and nothing pipeline-specific:

```yaml
# values.yaml
agent:
  networkPolicy:
    # FQDN egress for the agent sandbox. `auto` (the default) detects the
    # dialect the cluster enforces; pin cilium / gke / istio to be explicit.
    mode: auto
```

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
  --version <X.Y.Z> --namespace patchy -f values.yaml
```

The chart's `appVersion` is stamped 1:1 with each release, and the default image tag is derived from it — installing
chart `X.Y.Z` runs images `vX.Y.Z`. The rendered `NOTES.txt` recaps the webhook URL and the Secrets it expects. The full
values surface is on the [Helm chart page](../deployment/helm.md).

### Egress proxy

If the controllers' outbound traffic must traverse a corporate forward proxy — the GitHub Enterprise Cloud IP-allowlist
estate — set the chart's `proxy` values; they render `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` into every controller's
ConfigMap:

```yaml
proxy:
  httpsProxy: http://proxy.corp.example:3128
  # httpProxy: ""
  # noProxy: internal.example
```

When either proxy is set, `NO_PROXY` is automatically prefixed with `localhost,127.0.0.1,.svc,.cluster.local` so
in-cluster traffic (the artifact endpoint, the egress broker, the Kubernetes API server) never detours through the
proxy; `noProxy` appends to that list. A [`spec.proxy` on a Forge](../integrations/forges/github.md#egress-proxy) or
[GitHub Integration](../integrations/sources/github.md#egress-proxy) overrides the environment for that resource's
traffic, and `config.extra` (per-controller: `<controller>.config.extra`) remains the escape hatch that wins over both
derived keys. Reaching a proxy on a port like 3128 also needs `<controller>.networkPolicy.extraEgress` — see the
[isolation model](../deployment/isolation.md#egress-proxies).

## Switch the pipeline on: the `patchy-config` chart

The controllers idle until two custom resources exist: an [**Integration**](../integrations/sources/github.md) (where
findings come from, where the tracking issues go, webhook validation) and a
[**Forge**](../integrations/forges/github.md) (how repositories are fetched and pushed). They ship as the separate
`patchy-config` chart — separate because Helm validates every manifest against the API server before applying anything,
so the CRs cannot install in the same first release as the CRDs they depend on. Install it **after** the `patchy` chart,
into the **same namespace**:

```yaml
# config-values.yaml
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

```sh
helm install patchy-config oci://ghcr.io/bitwise-media-group/patchy/charts/patchy-config \
  --version <X.Y.Z> --namespace patchy -f config-values.yaml
```

Each entry's `spec` is validated client-side by the chart's values schema (generated from the CRDs, so a typo'd field
fails the install before anything is applied) and again server-side by the CRD —
[`deploy/kustomize/base/crs.example.yaml`](https://github.com/bitwise-media-group/patchy/blob/main/deploy/kustomize/base/crs.example.yaml)
is the full field walkthrough (GHES base URLs, org allowlists, repository regexes). Prefer applying the CRs yourself?
Skip this chart and `kubectl apply` the same objects after the install.

## Expose the webhook

Expose the **integration-controller** — the single internet-facing component — and point the GitHub App's webhook URL at
`https://<webhook.host>/github/webhooks`:

```yaml
webhook:
  host: patchy.example.com
  ingress: # or httpRoute — see the webhook exposure page
    enabled: true
    className: nginx
    tls:
      - secretName: patchy-webhook-tls
        hosts: [patchy.example.com]
```

Flavours, TLS, and the EKS / AKS / GKE notes live in [Deployment → Webhook exposure](../deployment/webhook.md). The
other controllers stay cluster-internal; every controller serves `/healthz` and `/readyz` probes on port 8081.

## The kustomize alternative

The same stack renders from `deploy/kustomize` if Helm isn't your tool:

```sh
kubectl apply -k deploy/kustomize/overlays/dev    # kind/colima: throwaway secrets, fast loops, fake harness
kubectl apply -k deploy/kustomize/overlays/prod   # digest-pinned images + Cilium FQDN egress
```

Bring the same two Secrets and your `Integration`/`Forge` resources; the base and overlays are covered in
[Deployment → Kustomize](../deployment/kustomize.md).

## Verify provenance (optional)

Every chart version and container image carries a GitHub build-provenance attestation:

```sh
gh attestation verify --owner bitwise-media-group \
  oci://ghcr.io/bitwise-media-group/patchy/charts/patchy:X.Y.Z
```

Next: [follow one finding end to end](verify.md).
