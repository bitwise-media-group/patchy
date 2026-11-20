# Webhook exposure

A GitHub App has only **one** webhook URL, and GitHub POSTs every subscribed event to it. In patchy that URL points at
the **integration-controller**. It is the only component that faces the internet and the only webhook receiver in the
system:

```text
https://<webhook.host>/github/webhooks
```

The controller first checks each delivery's HMAC signature against the `webhookSecret` of your configured `Integration`
resources. If no Integration's secret matches, it rejects the delivery with `401`. There is no routing tier and nothing
to fan deliveries out to. This one controller turns scanner events into `Finding` resources and applies human signals
(issue close, `/approve`, PR merge) to them.

This has two consequences:

- **Losing a delivery does not lose work.** The webhook path carries only new alerts and human-in-the-loop signals.
  Pipeline progress comes from the controllers' watch-driven reconcile loops. No delivery could announce those events
  anyway ("accumulation closed", "older than an hour", "a slot freed"). A rejected delivery (`503` on a full queue, or
  downtime) stays in GitHub's 30-day delivery log, and the
  [redelivery sweep](../integrations/sources/github.md#the-failed-delivery-sweep) replays it.
- **Exposure needs nothing unusual.** Any plain Ingress or Gateway API implementation works as it is. You need no header
  matching, rewrites or mirroring. Expose only the provider webhook paths: `/github/webhooks`, plus
  `/google-cloud/webhooks`, `/wiz/webhooks` and the `/generic/` prefix when you configure those Integrations. The probes
  stay inside the cluster on port 8081.

The integration-controller's Deployment holds no GitHub credential. It reads the `Integration` and `Forge` Secrets on
demand through the Kubernetes API. The component that uses write credentials (remediation-controller, for push and PR)
never faces the internet.

## Missed deliveries

GitHub does **not** retry a failed webhook delivery. It records the attempt in the App's 30-day delivery log and moves
on. The redelivery sweep, the replay action and the manual backfill recover what the receiver missed. They belong to the
Integration, not to exposure, so see [the GitHub source](../integrations/sources/github.md#the-failed-delivery-sweep).

## Expose it

Enable one option under the chart's `webhook` value. Then point the App's webhook URL at
`https://<host>/github/webhooks`.

**Plain Ingress** (`webhook.ingress`) works with any ingress controller:

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

GitHub should always deliver over HTTPS. Set `tls` (put cert-manager annotations in `webhook.ingress.annotations`), or
terminate TLS in front of the Ingress.

**Gateway API** (`webhook.httpRoute`) creates one `HTTPRoute`. It attaches to a `Gateway` you supply through
`parentRefs`. The Gateway listener handles TLS and certificates:

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

None of this is specific to patchy. The chart creates standard resources and adds no implementation-specific annotations
of its own. Add what your controller needs through `webhook.ingress.annotations` or `webhook.httpRoute.annotations`. As
a guide:

- **GKE**: both options work by default. Use the built-in GKE Ingress (`gce` class), or the
  [GKE Gateway controller](https://cloud.google.com/kubernetes-engine/docs/concepts/gateway-api) with a `gke-l7-*`
  Gateway. Enable the Gateway controller with `--gateway-api=standard`; Google manages its CRDs and controller.
- **EKS**: install the [AWS Load Balancer Controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/).
  Its `alb` IngressClass covers the Ingress option. Its
  [GA Gateway API support](https://aws.amazon.com/blogs/networking-and-content-delivery/aws-load-balancer-controller-adds-general-availability-support-for-kubernetes-gateway-api/)
  covers `httpRoute` (install the Gateway API CRDs alongside it) and provisions an ALB with the certificate from ACM.
- **AKS**:
  [Application Gateway for Containers](https://learn.microsoft.com/en-us/azure/application-gateway/for-containers/overview)
  implements both the Ingress and Gateway APIs. Enable its ALB Controller as an
  [AKS managed add-on](https://learn.microsoft.com/en-us/azure/application-gateway/for-containers/quickstart-deploy-application-gateway-for-containers-alb-controller-addon)
  (this needs workload identity and Azure CNI). The AKS _application routing_ add-on (managed NGINX) also covers the
  Ingress option.

Any other conformant implementation (ingress-nginx, Istio, Envoy Gateway, Cilium, ...) works the same way.

## Kustomize

The base ships the integration-controller Deployment and its ClusterIP Service (`patchy-integration-controller:8080`).
It ships no Ingress on purpose. Put your environment's Ingress or Gateway in front of that Service in your own overlay.
The dev overlay exposes it in two ways at once:

- NodePort 30079. Point a webhook tunnel (smee.io, ngrok, `gh webhook forward`) or `mise run replay` here.
- An Ingress for the same path with no host and no class, which any default ingress controller serves.

On [Colima](colima.md) that Ingress works by default.
