# Webhook exposure

A GitHub App has exactly **one** webhook URL, and GitHub POSTs every subscribed event to it — but each patchy controller
runs its own receiver (`POST /webhook` on port 8080). The **webhook-controller** resolves this: it is the single
internet-facing component, and the one URL points at it:

```text
https://<webhook.host>/webhook
```

The webhook-controller validates each delivery against the shared HMAC secret, then routes it — signature intact — to
the controllers that consume its `X-GitHub-Event` type:

| Event                 | Routed to                | Why                                           |
| --------------------- | ------------------------ | --------------------------------------------- |
| `code_scanning_alert` | `source-controller`      | Open / accumulate finding issues              |
| `issues`              | `context-controller`     | Enhance newly opened finding issues           |
| `issue_comment`       | `remediation-controller` | The `/approve` escape hatch                   |
| `pull_request`        | `remediation-controller` | Close the loop when the remediation PR merges |
| anything else         | `source-controller`      | It owns the `pkg/source` plugin seam          |

Two properties follow:

- **No credential faces the internet.** The webhook-controller holds only the webhook secret, which cannot mint GitHub
  tokens; the controllers, which hold the App key, only ever see deliveries that already passed HMAC validation (and
  each one re-validates the forwarded signature itself — the webhook-controller is not trusted).
- **Exposure needs nothing exotic.** Routing happens in the webhook-controller, not in routing infrastructure, so any
  plain Ingress or Gateway API implementation works as-is — no header matching, no rewrites, no mirroring.

The chart derives the routing table from its own Service names; override it wholesale with
`webhookController.config.extra.PATCHY_FORWARD_ROUTES` (the format is documented in
[Configuration → webhook-controller](../configuration/webhook-controller.md)). Forwarding is best-effort by design: a
failed forward is logged and dropped, because every controller pairs its webhook fast path with a reconcile loop
(`config.reconcileInterval`, default `60s`) that picks up anything missed. The webhook-controller is also stateless, so
unlike the singleton controllers it runs two replicas by default (`webhookController.replicas`).

## Expose it

Enable one flavour under the chart's `webhook` value and point the App's webhook URL at `https://<host>/webhook`. Both
expose only the `/webhook` path — the probes stay cluster-internal.

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

The base ships the webhook-controller Deployment and its ClusterIP Service (`patchy-webhook-controller`) but
deliberately no Ingress — put your environment's Ingress or Gateway in front of that Service in your own overlay. The
dev overlay exposes it as NodePort 30079 for kind, which is where a webhook tunnel (smee.io, ngrok,
`gh webhook forward`) should point.
