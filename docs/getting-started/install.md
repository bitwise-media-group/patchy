# Install with Helm

The chart is published to GHCR as an OCI artifact on every release and installs the whole stack: three controller
Deployments, Services, RBAC, the agent namespace, and baseline network policies.

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

Patchy references three Secrets but refuses to own them — create them out of band (SOPS, external-secrets, or plain
`kubectl` for a first run). Two live in the release namespace, one in the agent namespace:

```sh
# The GitHub App credentials (App ID + private key from the previous page)
kubectl -n patchy create secret generic patchy-github-app \
  --from-literal=app-id=123456 \
  --from-file=private-key.pem=./patchy.private-key.pem

# The webhook HMAC secret (the value pasted into the App's webhook-secret field)
kubectl -n patchy create secret generic patchy-webhook-secret \
  --from-literal=secret="$WEBHOOK_SECRET"

# The model API key — in the AGENT namespace; the chart creates it on first
# install, so pre-create the namespace if you want the secret in place first
kubectl create namespace patchy-agents --dry-run=client -o yaml | kubectl apply -f -
kubectl -n patchy-agents create secret generic patchy-anthropic \
  --from-literal=api-key="$ANTHROPIC_API_KEY"
```

| Secret                  | Namespace       | Keys                        | Consumed by                                     |
| ----------------------- | --------------- | --------------------------- | ----------------------------------------------- |
| `patchy-github-app`     | `patchy`        | `app-id`, `private-key.pem` | All three controllers                           |
| `patchy-webhook-secret` | `patchy`        | `secret`                    | All three controllers (webhook HMAC validation) |
| `patchy-anthropic`      | `patchy-agents` | `api-key`                   | Agent Job pods only (`ANTHROPIC_API_KEY`)       |

!!! warning "The Anthropic secret is not optional"

    The Job builder wires `ANTHROPIC_API_KEY` into every agent pod via a `secretKeyRef`. A missing
    `patchy-anthropic` secret means every agent Job sits in `CreateContainerConfigError` — even with the fake
    harness.

There is deliberately **no** GitHub credential in the agent namespace: the remediation-controller mints a short-lived,
single-repo clone token per Job, and it reaches only the init container. See the
[isolation model](../deployment/isolation.md).

## Install the chart

```sh
helm install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
  --version <X.Y.Z> --namespace patchy
```

The chart's `appVersion` is stamped 1:1 with each release, and the default image tag is derived from it — installing
chart `X.Y.Z` runs images `vX.Y.Z`. The rendered `NOTES.txt` recaps the webhook URL and the Secrets it expects.

Common values to override — see the [Helm chart reference](../deployment/helm.md) for the full surface:

```yaml
# values.yaml
github:
  baseURL: "" # set for GitHub Enterprise Server

remediationController:
  config:
    # model + budget knobs, rendered into this controller's ConfigMap as
    # PATCHY_* vars
    classify:
      model: claude-sonnet-5
    remediate:
      model: claude-sonnet-5
    modelAllowlist: claude-sonnet-5,claude-opus-4-8

agent:
  networkPolicy:
    cilium:
      enabled: true # FQDN egress for the agent sandbox (or istio.enabled)
```

```sh
helm upgrade --install patchy oci://ghcr.io/bitwise-media-group/patchy/charts/patchy \
  --version <X.Y.Z> --namespace patchy -f values.yaml
```

## Expose the webhook

Expose the **webhook-controller** — the single internet-facing component, which validates each delivery and routes it to
the controllers that consume its event type — and point the GitHub App's webhook URL at
`https://<webhook.host>/webhook`:

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
controllers themselves stay `ClusterIP`-only; every component serves `/healthz` and `/readyz` probes on port 8080.

## The kustomize alternative

The same stack renders from `deploy/kustomize` if Helm isn't your tool:

```sh
kubectl apply -k deploy/kustomize/overlays/dev    # kind/dev: fake secrets, fast loops, fake harness
kubectl apply -k deploy/kustomize/overlays/prod   # digest-pinned images + Cilium FQDN egress
```

Bring the same three Secrets; the base and overlays are covered briefly in
[Deployment → Kustomize](../deployment/kustomize.md).

## Verify provenance (optional)

Every chart version and container image carries a GitHub build-provenance attestation:

```sh
gh attestation verify --owner bitwise-media-group \
  oci://ghcr.io/bitwise-media-group/patchy/charts/patchy:X.Y.Z
```

Next: [follow one finding end to end](verify.md).
