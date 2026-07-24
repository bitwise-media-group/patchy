# Kustomize

The Helm chart is the primary deployment surface, but the same stack — identical resources, defaults, and isolation
model — renders from the kustomize tree in `deploy/`. Use it when your platform standardises on kustomize or you want
overlay-style patching; `deploy/README.md` in the repository is the full operator document.

```text
deploy/
├── kustomize/
│   ├── base/                  # CRDs (rendered first), namespaces, serviceaccounts,
│   │                          #   RBAC, the shared ConfigMap, five Deployments,
│   │                          #   Services, network policies
│   ├── components/cilium/     # optional FQDN egress (CiliumNetworkPolicy)
│   ├── components/gke-fqdn/   # optional FQDN egress (GKE Dataplane V2 FQDNNetworkPolicy)
│   ├── components/istio/      # optional Sidecar + ServiceEntry + netpol
│   └── overlays/
│       ├── dev/               # kind/colima: NodePort 30079, throwaway secrets + CRs,
│       │                      #   2m windows, fake harness, fake CMDB
│       └── prod/              # digest-pinned images + the cilium component
└── README.md
```

Apply an overlay (render first with `kubectl kustomize` if you want to review):

```sh
kubectl apply -k deploy/kustomize/overlays/dev
kubectl apply -k deploy/kustomize/overlays/prod
```

The runbook order for a fresh cluster: apply the overlay (CRDs render first), create the
[two Secrets](../getting-started/install.md#create-the-secrets), apply your `Integration`/`Forge` resources
(`base/crs.example.yaml` is the commented walkthrough; the dev overlay ships working placeholders), then point the
GitHub App's webhook at the integration-controller.

## Configuration

Everything is `PATCHY_*` environment in one ConfigMap (`base/configmap.yaml`), consumed with `envFrom`. A key a binary
does not bind is inert, which is why one ConfigMap serves all five controllers — the
[configuration reference](../configuration/index.md) maps every key to its flag.

!!! warning "The agent image is pinned in two places"

    The per-harness `PATCHY_<HARNESS>_AGENT_IMAGE` keys are the strings the job controllers stamp into the Jobs they
    create, and kustomize's `images:` transformer does **not** rewrite ConfigMap values. An overlay that pins a runner
    image must patch both the `images:` entry and the matching key — the prod overlay does exactly that.

## Human access: RBAC and the admission policy

Two things consume the custom verbs `approve` / `retry` / `expedite` / `suspend` / `resume` on
`findings.patchy.bitwisemedia.uk`: the [status page](../status-ui.md), which resolves them per signed-in user via
SubjectAccessReview, and the [CLI](../cli.md), which acts as the user's own kubeconfig identity.

Those two need different enforcement. The status page writes as its own ServiceAccount, so it can check the verb itself.
The CLI writes as the user, so the API server authorizes it — and RBAC has no notion of a field, meaning `update` on
findings grants the _whole_ object. Granting a developer permission to suspend a finding would also let them rewrite its
severity or forge an approval.

`base/admission-policy.yaml` closes that gap with a `ValidatingAdmissionPolicy` binding each spec field to its verb. It
is part of the base, so both overlays get it. Because it runs in the API server's admission chain it applies to every
client equally — `patchy`, `kubectl edit`, `kubectl patch`, server-side apply, raw `curl`. There is no path around it
short of privileges that already exceed the grant being protected.

It enforces, for everyone except the patchy controllers:

- each human-writable spec field changes only with its own verb (`spec.suspend` needs `suspend` to set and `resume` to
  clear; `spec.approval` needs `approve`; and so on);
- every other spec field is frozen — the pipeline owns it;
- `metadata.finalizers`, `ownerReferences` and `labels` are frozen, because the rollup finalizers are what guarantee
  spend is aggregated before deletion and the selector labels are what accumulation and child lookup key on.

`findings/status` is deliberately not matched, so controller status writes and phase transitions are unaffected.

!!! warning "Requires Kubernetes 1.30+"

    `ValidatingAdmissionPolicy` reached GA in 1.30. On an older cluster this resource will not apply and
    enforcement degrades **silently** to "whoever holds `update` owns the whole resource". Confirm with
    `kubectl api-resources | grep validatingadmissionpolic` before relying on the verb ladder.

`base/rbac.users.example.yaml` is documentation rather than an applied resource: copy the role ladder into your overlay
and bind it to your own users or SSO groups. Roles above viewer grant `update` on findings — that is what lets the CLI
write at all, and the admission policy is what makes it safe. `create` and `delete` are deliberately withheld, so nobody
can launder an approval by deleting a finding and recreating it with one preset.

## The overlays

- **dev** targets a local kind or [Colima](colima.md) cluster: local `patchy/*:dev` images (`make snapshot`, retag,
  `kind load docker-image` — Colima skips the load), a NodePort webhook on 30079 (point your tunnel or `mise run replay`
  at `/github/webhooks`; kind needs `extraPortMappings`), a host-less dev Ingress for the same path, minutes instead of
  hours (2m accumulation and min-age, 30m finding TTL), the static-file fake CMDB enhancer mounted from a ConfigMap, the
  `fake` harness so no tokens are spent, placeholder `Integration`/`Forge` CRs, and tiny resource requests. Two caveats:
  the placeholder GitHub credential fails every GitHub call until you overwrite the `patchy-github` Secret with a PAT
  (`GITHUB_TOKEN=<pat> make dev-colima` does it for you), and kind's kindnet ignores NetworkPolicy — a green dev apply
  is not evidence of a working sandbox.
- **dev-fake** layers on dev for a fully credential-less end to end: the e2e suite's fake GitHub runs in-cluster
  (`patchy-fakegithub`, with a NodePort on 30990 for host-side inspection), the `Integration`/`Forge` CRs point at its
  Service, the agent image is a scripted stand-in (`hack/fake-agent`) that needs no model key, and one extra egress rule
  reaches the fake. The whole CR pipeline — ingestion through pull request, rollups, and the TTL — runs against it; the
  [Colima walkthrough](colima.md#credential-less-end-to-end-the-dev-fake-overlay) shows the full loop.
- **prod** uses the real intervals (1h accumulation and min-age, the 14-day TTL), the `claude` harness, the Cilium FQDN
  component, production-sized requests, and **digest-pinned images** — the checked-in `sha256:0000…` values are
  placeholders to replace with your release's published digests, including the `PATCHY_CLAUDE_AGENT_IMAGE` value in the
  ConfigMap patch. Bring real Secrets and CRs with SOPS or external-secrets, and put your Ingress or Gateway in front of
  `patchy-integration-controller:8080` in your own overlay — the base deliberately ships none.

The base's `secrets.example.yaml` and `crs.example.yaml` are documentation, not resources — the dev overlay's throwaway
values exist so the pods schedule and the CR state machine runs, not so GitHub calls succeed.
