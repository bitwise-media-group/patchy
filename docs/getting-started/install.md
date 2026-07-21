# Install with Helm

The chart is published to GHCR as an OCI artifact on every release and installs the whole stack: the
`patchy.bitwisemedia.uk` CRDs, five controller Deployments with their RBAC and ConfigMaps, the two Services, the agent
namespace, and the baseline network policies.

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

# The model credential — in the AGENT namespace; pre-create the namespace if
# you want the secret in place before the first install
kubectl create namespace patchy-agents --dry-run=client -o yaml | kubectl apply -f -
kubectl -n patchy-agents create secret generic patchy-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

No Anthropic API key? A Claude subscription works too: mint a long-lived OAuth token with
[`claude setup-token`](https://code.claude.com/docs/en/cli-reference), store it in the same secret, and set
`anthropic.secretEnv: CLAUDE_CODE_OAUTH_TOKEN` (Helm) or `PATCHY_ANTHROPIC_SECRET_ENV=CLAUDE_CODE_OAUTH_TOKEN`
(kustomize) so the Job builder injects it under the env var the `claude` CLI expects:

```sh
kubectl -n patchy-agents create secret generic patchy-anthropic \
  --from-literal=api-key="$(claude setup-token)"
```

| Secret             | Namespace       | Keys                                                 | Consumed by                                                                                       |
| ------------------ | --------------- | ---------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `patchy-github`    | `patchy`        | `appID` + `privateKey` (or `token`), `webhookSecret` | The `Integration`/`Forge` CRs' `spec.secretRef` — read on demand through the API, never mounted   |
| `patchy-anthropic` | `patchy-agents` | `api-key`                                            | Agent Job pods only (`ANTHROPIC_API_KEY`, or `CLAUDE_CODE_OAUTH_TOKEN` via `anthropic.secretEnv`) |

A `token` key (a personal access token) is the dev-only fallback and wins over App auth when set. One GitHub Secret may
serve both CRs, or you can split read and write identities across two GitHub Apps and two Secrets.

!!! warning "The Anthropic secret is not optional"

    The Job builder wires the credential (`ANTHROPIC_API_KEY` by default) into every agent pod via a
    `secretKeyRef`. A missing `patchy-anthropic` secret means every agent Job sits in
    `CreateContainerConfigError` — even with the fake harness.

There is deliberately **no** GitHub credential in the agent namespace — not even a per-Job one. The repository arrives
as a digest-verified tarball from the source-controller's in-cluster artifact server. See the
[isolation model](../deployment/isolation.md).

## Install the chart, switch the pipeline on

The controllers idle until two custom resources exist: an **Integration** (where findings come from, where the tracking
issues go, webhook validation) and a **Forge** (how repositories are fetched and pushed). The chart renders them from
values:

```yaml
# values.yaml
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

agent:
  networkPolicy:
    cilium:
      enabled: true # FQDN egress for the agent sandbox (or istio.enabled)
```

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
  --version <X.Y.Z> --namespace patchy -f values.yaml
```

Each entry's `spec` is rendered verbatim and validated server-side by the CRD schema —
[`deploy/kustomize/base/crs.example.yaml`](https://github.com/bitwise-media-group/patchy/blob/main/deploy/kustomize/base/crs.example.yaml)
is the full field walkthrough (GHES base URLs, org allowlists, repository regexes). Prefer applying the CRs yourself?
Leave `integrations`/`forges` empty and `kubectl apply` the same objects after the install.

The chart's `appVersion` is stamped 1:1 with each release, and the default image tag is derived from it — installing
chart `X.Y.Z` runs images `vX.Y.Z`. The rendered `NOTES.txt` recaps the webhook URL and the Secrets it expects. The full
values surface is on the [Helm chart page](../deployment/helm.md).

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
