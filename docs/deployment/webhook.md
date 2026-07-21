# Webhook exposure

A GitHub App has exactly **one** webhook URL, and GitHub POSTs every subscribed event to it. In patchy that URL points
at the **integration-controller** — the single internet-facing component and the only webhook receiver in the system:

```text
https://<webhook.host>/github/webhooks
```

Each delivery's HMAC signature is validated against the `webhookSecret` of your configured `Integration` resources
before anything else happens; a delivery no Integration's secret matches is rejected with `401`. There is no routing
tier and nothing to fan deliveries out to — scanner events are ingested into `Finding` resources and human signals
(issue close, `/approve`, PR merge) are applied to them, all inside this one controller.

Two properties follow:

- **Losing a delivery is not losing work.** The webhook path carries ingestion and human-in-the-loop signals only;
  pipeline progress rides on the controllers' watch-driven reconcile loops, which no delivery can announce anyway
  ("accumulation closed", "older than an hour", "a slot freed"). A `503` from a full queue just means GitHub redelivers.
- **Exposure needs nothing exotic.** Any plain Ingress or Gateway API implementation works as-is — no header matching,
  no rewrites, no mirroring. Only `/github/webhooks` needs exposing; the probes stay cluster-internal on port 8081.

The credential story: the integration-controller holds no GitHub credential in its Deployment at all — the
`Integration`/`Forge` Secrets are read on demand through the Kubernetes API, and the components that exercise write
credentials (remediation-controller's push/PR) never face the internet.

## Expose it

Enable one flavour under the chart's `webhook` value and point the App's webhook URL at
`https://<host>/github/webhooks`.

**Plain Ingress** (`webhook.ingress`) — works with any ingress controller:

```yaml
webhook:
  host: patchy.example.com
  ingress:
    enabled: true
    className: nginx
    tls:
      - secretName: patchy-webhook-tls
        hosts:
          - patchy.example.com
```

GitHub should always deliver over HTTPS — set `tls` (cert-manager annotations go in `webhook.ingress.annotations`) or
terminate TLS in front of the Ingress.

**Gateway API** (`webhook.httpRoute`) — one `HTTPRoute` that attaches to a `Gateway` you bring via `parentRefs`; TLS and
certificates are the Gateway listener's concern:

```yaml
webhook:
  host: patchy.example.com
  httpRoute:
    enabled: true
    parentRefs:
      - name: my-gateway
        namespace: gateway-system
        sectionName: https
```

## Managed platform notes

Nothing here is patchy-specific — the chart emits standard resources with no implementation-specific annotations of its
own (add what your controller needs via `webhook.ingress.annotations` / `webhook.httpRoute.annotations`) — but for
orientation:

- **GKE** — both flavours work out of the box: the built-in GKE Ingress (`gce` class), or the
  [GKE Gateway controller](https://cloud.google.com/kubernetes-engine/docs/concepts/gateway-api) (enable with
  `--gateway-api=standard`; Google manages the CRDs and controller) with a `gke-l7-*` Gateway.
- **EKS** — install the [AWS Load Balancer Controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/):
  its `alb` IngressClass covers the Ingress flavour, and its
  [GA Gateway API support](https://aws.amazon.com/blogs/networking-and-content-delivery/aws-load-balancer-controller-adds-general-availability-support-for-kubernetes-gateway-api/)
  (Gateway API CRDs installed alongside) covers `httpRoute`, provisioning an ALB with the certificate from ACM.
- **AKS** —
  [Application Gateway for Containers](https://learn.microsoft.com/en-us/azure/application-gateway/for-containers/overview)
  implements both the Ingress and Gateway APIs; enable its ALB Controller as an
  [AKS managed add-on](https://learn.microsoft.com/en-us/azure/application-gateway/for-containers/quickstart-deploy-application-gateway-for-containers-alb-controller-addon)
  (requires workload identity and Azure CNI). The AKS _application routing_ add-on (managed NGINX) also covers the
  Ingress flavour.

Any other conformant implementation (ingress-nginx, Istio, Envoy Gateway, Cilium, ...) works the same way.

## Kustomize

The base ships the integration-controller Deployment and its ClusterIP Service (`patchy-integration-controller:8080`)
but deliberately no Ingress — put your environment's Ingress or Gateway in front of that Service in your own overlay.
The dev overlay exposes it two ways at once: NodePort 30079 (where a webhook tunnel — smee.io, ngrok,
`gh webhook forward` — or `mise run replay` should point) and a host-less, class-less Ingress for the same path that any
default ingress controller satisfies. On [Colima](colima.md) that Ingress is live out of the box.
