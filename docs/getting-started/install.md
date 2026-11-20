# Install with Helm

Every release publishes two charts to GHCR as OCI artifacts:

- `patchy` installs the full stack. That means the `patchy.bitwisemedia.uk` CRDs, the controller, egress-broker and
  status-server Deployments with their RBAC, ConfigMaps and Services, the agent namespace, and the baseline network
  policies. See [Helm charts](../deployment/helm.md) for the full list.
- `patchy-config` installs the `Integration`/`Forge` custom resources that switch the pipeline on. You install it as a
  second release once the CRDs exist.

## Create the namespaces

The chart creates the agent namespace (`patchy-agents`) itself, but you create the release namespace. Both must carry
the `restricted` Pod Security Standard. The chart labels the agent namespace; you label the release namespace:

```sh
kubectl create namespace patchy
kubectl label namespace patchy \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/warn=restricted
```

## Create the secrets

Patchy references two Secrets but never creates or manages them. Create them yourself (with SOPS, external-secrets, or
plain `kubectl` for a first run). Both live in the release namespace; the agent namespace holds no credential:

```sh
# The GitHub credential + webhook secret (all three values from the previous page)
kubectl -n patchy create secret generic patchy-github \
  --from-literal=appID=123456 \
  --from-file=privateKey=./patchy.private-key.pem \
  --from-literal=webhookSecret="$WEBHOOK_SECRET"

# The claude model credential. It goes in the RELEASE namespace, where the
# egress credential broker (the only thing that ever holds it) runs. It never
# enters an agent pod.
kubectl -n patchy create secret generic patchy-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

No Anthropic API key? A Claude subscription works too:

1. Mint a long-lived OAuth token with [`claude setup-token`](https://code.claude.com/docs/en/cli-reference).
2. Store it in the same secret.
3. Set `egressBroker.anthropicAuth: token` (Helm) or `PATCHY_ANTHROPIC_AUTH=token` (kustomize), so the broker sends it
   as a bearer token instead of an API key.

```sh
kubectl -n patchy create secret generic patchy-anthropic \
  --from-literal=api-key="$(claude setup-token)"
```

If you run claude against Amazon Bedrock, GCP Vertex AI, or Microsoft Foundry instead, you need no Anthropic Secret. See
[Models and credentials](../deployment/models.md#provider-credentials).

To let the investigation choose OpenAI models for remediation, enable the codex runner and create its credential:

- Enable the runner with `agent.runners.codex.enabled: true` (Helm), or add `codex` to `PATCHY_HARNESSES` and set
  `PATCHY_OPENAI_API_KEY_FILE` (kustomize).
- The credential is an OpenAI platform API key, used by the egress broker. A ChatGPT-plan token cannot be brokered.
  Azure OpenAI is the alternative, set through `agent.runners.codex.provider`.

```sh
kubectl -n patchy create secret generic patchy-openai --from-literal=api-key="$OPENAI_API_KEY"
```

| Secret             | Namespace | Keys                                                 | Consumed by                                                                                                                                  |
| ------------------ | --------- | ---------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `patchy-github`    | `patchy`  | `appID` + `privateKey` (or `token`), `webhookSecret` | The `Integration`/`Forge` CRs' `spec.secretRef`. Read on demand through the API, never mounted.                                              |
| `patchy-anthropic` | `patchy`  | `api-key`                                            | The egress broker only, never an agent pod. An API key, or a `claude setup-token` OAuth token with `egressBroker.anthropicAuth: token`.      |
| `patchy-openai`    | `patchy`  | `api-key`                                            | The egress broker only, never an agent pod. An OpenAI platform API key. Needed only when the codex runner is enabled with provider `openai`. |

A `token` key (a personal access token) is the dev-only fallback. When set, it wins over App auth. One GitHub Secret can
serve both CRs, or you can split read and write identities across two GitHub Apps and two Secrets.

!!! warning "Model credentials live with the broker, not the agent namespace"

    You enable the claude and codex harnesses through configuration alone. Their credentials live with the egress
    broker, and a missing key shows up in the broker's readiness probe. No model credential is ever wired into an agent
    pod. The `fake` harness (dev only) needs no credential.

The agent namespace holds **no** GitHub credential, not even a per-Job one. The repository arrives as a digest-verified
tarball from the source-controller's in-cluster artifact server. See the [isolation model](../concepts/isolation.md).

## Install the chart

The `patchy` chart installs the stack (CRDs, controllers, agent sandbox) and nothing pipeline-specific:

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

Each release stamps the chart's `appVersion` to match the release version, and the default image tag comes from it. So
installing chart `X.Y.Z` runs images `vX.Y.Z`. The rendered `NOTES.txt` recaps the webhook URL and the Secrets it
expects. The [Helm chart page](../deployment/helm.md) lists every value.

### Egress proxy

The controllers' outbound traffic may need to go through a corporate forward proxy, for example when GitHub Enterprise
Cloud uses an IP allowlist. In that case, set the chart's `proxy` values. They render
`HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` into every controller's ConfigMap:

```yaml
proxy:
  httpsProxy: http://proxy.corp.example:3128
  # httpProxy: ""
  # noProxy: internal.example
```

When either proxy is set, the chart automatically prefixes `NO_PROXY` with `localhost,127.0.0.1,.svc,.cluster.local`.
This keeps in-cluster traffic (the artifact endpoint, the egress broker, the Kubernetes API server) away from the proxy.
`noProxy` appends to that list. Two other settings can override this:

- A [`spec.proxy` on a Forge](../integrations/forges/github.md#egress-proxy) or
  [GitHub Integration](../integrations/sources/github.md#egress-proxy) overrides the environment for that resource's
  traffic.
- `config.extra` (per-controller: `<controller>.config.extra`) is the escape hatch, and it wins over both derived keys.

Reaching a proxy on a port like 3128 also needs `<controller>.networkPolicy.extraEgress`. See the
[isolation model](../concepts/isolation.md#egress-proxies).

## Switch the pipeline on: the `patchy-config` chart

The controllers stay idle until two custom resources exist:

- an [**Integration**](../integrations/sources/github.md): where findings come from, where the tracking issues go, and
  webhook validation
- a [**Forge**](../integrations/forges/github.md): how repositories are fetched and pushed

They ship as the separate `patchy-config` chart. Helm validates every manifest against the API server before applying
anything, so the CRs cannot install in the same first release as the CRDs they depend on. Install it **after** the
`patchy` chart, into the **same namespace**:

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

Each entry's `spec` is validated twice. First the chart's values schema checks it client-side. The schema is generated
from the CRDs, so a mistyped field fails the install before anything is applied. Then the CRD checks it server-side.
[`deploy/kustomize/base/crs.example.yaml`](https://github.com/bitwise-media-group/patchy/blob/main/deploy/kustomize/base/crs.example.yaml)
walks through every field (GHES base URLs, org allowlists, repository regexes). Prefer applying the CRs yourself? Skip
this chart and `kubectl apply` the same objects after the install.

## Expose the webhook

Expose the **integration-controller**, which is the only internet-facing component. Then point the GitHub App's webhook
URL at `https://<webhook.host>/github/webhooks`:

```yaml
webhook:
  host: patchy.example.com
  ingress: # or httpRoute: see the webhook exposure page
    enabled: true
    className: nginx
    tls:
      - secretName: patchy-webhook-tls
        hosts: [patchy.example.com]
```

[Deployment → Webhook exposure](../deployment/webhook.md) covers the flavours, TLS, and notes for EKS / AKS / GKE. The
other controllers stay cluster-internal. Every controller serves `/healthz` and `/readyz` probes on port 8081.

## The kustomize alternative

If you don't use Helm, the same stack renders from `deploy/kustomize`:

```sh
kubectl apply -k deploy/kustomize/overlays/dev    # kind/colima: throwaway secrets, fast loops, fake harness
kubectl apply -k deploy/kustomize/overlays/prod   # release-pinned images + Cilium FQDN egress
```

Bring the same two Secrets and your `Integration`/`Forge` resources.
[Deployment → Kustomize](../deployment/kustomize.md) covers the base and overlays.

## Verify provenance (optional)

Every chart version and container image carries a GitHub build-provenance attestation:

```sh
gh attestation verify --owner bitwise-media-group \
  oci://ghcr.io/bitwise-media-group/patchy/charts/patchy:X.Y.Z
```

Next: [follow one finding end to end](verify.md).
