# Kustomize

The Helm chart is the main way to deploy patchy. The same stack also renders from the kustomize tree in `deploy/`, with
the same resources, defaults and isolation model. Use it when your platform standardises on kustomize or you want
overlay-style patching. `deploy/README.md` in the repository is the full operator document.

```text
deploy/
├── kustomize/
│   ├── base/                  # CRDs (rendered first), namespaces, serviceaccounts,
│   │                          #   RBAC, the shared ConfigMap, eight Deployments,
│   │                          #   Services, network policies, the admission policy
│   ├── components/cilium/     # optional FQDN egress (CiliumNetworkPolicy)
│   ├── components/gke-fqdn/   # optional FQDN egress (GKE Dataplane V2 FQDNNetworkPolicy)
│   ├── components/istio/      # optional Sidecar + ServiceEntry + netpol
│   └── overlays/
│       ├── dev/               # kind/colima: NodePort 30079, throwaway secrets + CRs,
│       │                      #   2m windows, fake harness, fake CMDB
│       ├── dev-fake/          # dev + an in-cluster fake GitHub: no credentials at all
│       └── prod/              # release-pinned images + the cilium component
└── README.md
```

The base deploys one Deployment per component: integration-controller, source-controller, context-controller,
investigation-controller, remediation-controller, evaluation-controller, status-server and egress-broker. The Helm chart
turns the evaluation-controller off by default, but the kustomize base includes it. Delete it in an overlay if you never
submit remote evaluations.

Apply an overlay. If you want to review it first, render it with `kubectl kustomize`.

```sh
kubectl apply -k deploy/kustomize/overlays/dev
kubectl apply -k deploy/kustomize/overlays/prod
```

On a fresh cluster, follow this order:

1. Apply the overlay. The CRDs render first.
2. Create the [two Secrets](../getting-started/install.md#create-the-secrets).
3. Apply your `Integration`/`Forge` resources. `base/crs.example.yaml` is the commented walkthrough, and the dev overlay
   ships working placeholders.
4. Point the GitHub App's webhook at the integration-controller.

## Configuration

All configuration is `PATCHY_*` environment variables in one ConfigMap (`base/configmap.yaml`), read with `envFrom`. A
binary ignores any key it does not bind, so one ConfigMap can serve every binary. The
[configuration reference](../configuration/index.md) maps every key to its flag.

!!! warning "The agent image is pinned in two places"

    The job controllers copy the per-harness `PATCHY_<HARNESS>_AGENT_IMAGE` values into the Jobs they create. Kustomize's
    `images:` transformer does **not** rewrite ConfigMap values. So an overlay that pins a runner image must patch both
    the `images:` entry and the matching key. The prod overlay does both.

## Human access: RBAC and the admission policy

Two tools use the custom verbs `approve` / `retry` / `expedite` / `suspend` / `resume` on
`findings.patchy.bitwisemedia.uk`:

- The [status page](../operate/status-page.md) checks them for each signed-in user with a SubjectAccessReview.
- The [CLI](../operate/cli.md) acts as the identity in the user's own kubeconfig.

These two need different enforcement. The status page writes as its own ServiceAccount, so it can check the verb itself.
The CLI writes as the user, so the API server authorizes it. RBAC has no concept of a field, so `update` on findings
grants write access to every field of the object. A developer allowed to suspend a finding could also rewrite its
severity or forge an approval.

`base/admission-policy.yaml` closes that gap. It is a `ValidatingAdmissionPolicy` that binds each spec field to its
verb. It is part of the base, so every overlay gets it. It runs in the API server's admission chain, so it applies to
every client in the same way: `patchy`, `kubectl edit`, `kubectl patch`, server-side apply and raw `curl`. The only way
around it is to hold privileges that already exceed the grant it protects.

For everyone except the patchy controllers, it enforces these rules:

- Each human-writable spec field changes only with its own verb. `spec.suspend` needs `suspend` to set and `resume` to
  clear, `spec.approval` needs `approve`, and so on.
- Every other spec field is frozen, because the pipeline owns it.
- `metadata.finalizers`, `ownerReferences` and `labels` are frozen. The rollup finalizers make sure spend is added to
  the totals before deletion, and accumulation and child lookup select on the labels.

The policy does not match `findings/status` on purpose, so controller status writes and phase transitions are not
affected.

Two more policies in the same file cover the other human writes. `patchy-integration-actions` ties `spec.replay` and
`spec.reset` on Integrations to the `replay` and `reset` verbs. `patchy-backfill-requests` covers
[BackfillRequests](../integrations/sources/github.md#who-may-run-one): creating one that runs at once needs `backfill`
on `integrations`, one with `spec.via: requested` needs no verb and waits for an operator, `spec.requestedBy` must be
the creating user, a `maintained` request can only come from the status server, and only a `backfill` holder may set
`spec.decision`. The status page's scoped maintainer verbs (`approve-maintained`, `expedite-maintained`,
`backfill-maintained`) appear in no policy: only the status page honours them, so they never authorize a direct write.

!!! warning "Requires Kubernetes 1.30+"

    `ValidatingAdmissionPolicy` reached GA in 1.30. On an older cluster this resource will not apply, and enforcement
    falls back **silently** to "whoever holds `update` owns every field". Check with
    `kubectl api-resources | grep validatingadmissionpolic` before you rely on the verb ladder.

`base/rbac.users.example.yaml` is documentation, not an applied resource. Copy the role ladder into your overlay and
bind it to your own users or SSO groups. Roles above viewer grant `update` on findings. That grant lets the CLI write at
all, and the admission policy makes it safe. The roles leave out `create` and `delete` on purpose. Otherwise someone
could delete a finding and recreate it with an approval already set. The one `create` the roles carry is on
`backfillrequests`: the admin role's runs a backfill (with `update` to decide other people's), and the lower roles' can
only file a request for an operator, because the policy holds it to `spec.via: requested`. The `patchy-maintainer` role
grants the scoped maintainer verbs to a broad SSO group, such as a dex group of every engineer; see
[the status page](../operate/status-page.md#access-model).

## The overlays

### dev

The dev overlay targets a local kind or [Colima](colima.md) cluster. It sets up:

- local `patchy/*:dev` images (`make snapshot`, retag, `kind load docker-image`; Colima skips the load)
- a NodePort webhook on 30079 (point your tunnel or `mise run replay` at `/github/webhooks`; kind needs
  `extraPortMappings`)
- a dev Ingress with no host for the same path
- minutes instead of hours (2m accumulation and min-age, 30m finding TTL)
- the static-file fake CMDB enhancer, mounted from a ConfigMap
- the `fake` harness, so no tokens are spent
- placeholder `Integration`/`Forge` CRs
- tiny resource requests

There are two caveats. First, the placeholder GitHub credential fails every GitHub call until you overwrite the
`patchy-github` Secret with a PAT (`GITHUB_TOKEN=<pat> make dev-colima` does this for you). Second, kind's kindnet
ignores NetworkPolicy, so a successful dev apply does not prove the sandbox works.

### dev-fake

The dev-fake overlay builds on dev to run end to end with no credentials:

- The e2e suite's fake GitHub runs in the cluster (`patchy-fakegithub`, with a NodePort on 30990 so you can inspect it
  from the host).
- The `Integration`/`Forge` CRs point at its Service.
- The agent image is a scripted stand-in (`hack/fake-agent`) that needs no model key.
- One extra egress rule lets pods reach the fake.

Every stage of the CR pipeline runs against it, from ingestion to pull request, plus rollups and the TTL. The
[demo-tooling walkthrough](dev-fake.md#credential-less-end-to-end-the-dev-fake-overlay) shows the full loop.

### prod

The prod overlay uses the real intervals (1h accumulation and min-age, the 14-day TTL), the `claude` harness, the Cilium
FQDN component, production-sized requests and **release-pinned images**. Every image names the `vX.Y.Z` tag of the
release it ships in, including the `PATCHY_CLAUDE_AGENT_IMAGE` value in the ConfigMap patch. Release-please bumps these
tags, so apply the overlay from a release tag. For immutable pins, add `digest:` entries in your own overlay. Bring real
Secrets and CRs with SOPS or external-secrets. Put your Ingress or Gateway in front of
`patchy-integration-controller:8080` in your own overlay, because the base ships none.

The base's `secrets.example.yaml` and `crs.example.yaml` are documentation, not resources. The dev overlay's throwaway
values exist so the pods schedule and the CR state machine runs. They do not make GitHub calls succeed.
