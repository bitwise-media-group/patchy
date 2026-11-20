# Kubescape exceptions

`exceptions.json` lists the kubescape findings this repository accepts. JSON has no comments, so the reason for each
entry lives here, under a heading per entry name. Every entry is either a design decision or a scanner false positive;
none is a fix put off for later.

## How the exceptions are used

- `mise run lint-kustomize` (`.mise/common/scripts/lint-kustomize.sh`) runs `kubescape scan` on every kustomization that
  is not a Component: `deploy/kustomize/base` and the `dev`, `dev-fake` and `prod` overlays. It passes
  `--exceptions .kubescape/exceptions.json` whenever the file exists, and fails on findings at or above
  `KUBESCAPE_SEVERITY` (default `high`).
- The Helm charts under `charts/` are not scanned. The shared `lint-helm` script only looks for `helm/*/Chart.yaml`, and
  `mise run helm-lint` (`hack/helm-lint.sh`) runs `helm lint` and renders, without kubescape.
- Each entry matches resources by `kind`, `name` and `namespace` attributes. `name` is a regex. The leading `.*` in most
  patterns also covers a Helm release-name prefix, so the entries would still match if the charts were scanned.

To see what each entry matches, scan without the exceptions and compare (the control set is cached in `~/.kubescape`, so
this works offline):

```sh
KUBECONFIG=/dev/null kubescape scan framework allcontrols --use-from ~/.kubescape/allcontrols.json \
  deploy/kustomize/overlays/dev --format json --output /tmp/ks.json
```

The notes below on what each entry matches come from that scan with kubescape v4.0.20, across all four kustomize
targets.

## Security context: `seLinuxOptions` (C-0211)

Entries: `controllers-c0211-selinux-options-are-host-specific`, `fakegithub-c0211-selinux-options-are-host-specific`,
`status-server-c0211-selinux-options-are-host-specific`, `egress-broker-c0211-selinux-options-are-host-specific`.

They match the six controller Deployments, the status server and the egress broker in every target, and the fake GitHub
Deployment in `dev-fake`.

These Deployments already set a full security context: `runAsNonRoot`, a non-zero uid and gid, `fsGroup`, the
`RuntimeDefault` seccomp profile, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem`, and every capability
dropped. The only field C-0211 still wants is `seLinuxOptions`. An MCS level depends on the node OS and the cluster's
policy, so a portable manifest cannot pick one.

## Service account tokens (C-0034, C-0053, C-0190, C-0261)

Entries: `controllers-need-the-kubernetes-api`, `status-server-needs-the-kubernetes-api`,
`egress-broker-needs-the-kubernetes-api`.

They match the Deployments and ServiceAccounts of the six controllers, the status server and the egress broker in every
target.

These controls fire on any workload that mounts a ServiceAccount token. Each of these workloads needs one:

- The controllers keep all state in the CRs in the release namespace, and the Job-launching controllers create and watch
  agent Jobs.
- The status server reads Findings and FindingRollups, writes spec fields, and runs SubjectAccessReviews for the
  signed-in user.
- The egress broker validates each caller's token with a TokenReview. That is its only Kubernetes access.

The one workload that needs no token is the agent pod. Its ServiceAccount (`patchy-agent` in
`deploy/kustomize/base/serviceaccount.yaml`) sets `automountServiceAccountToken: false`. The only token an agent
container gets is the audience-bound projected token for the egress broker, which is not a Kubernetes API credential.

## RBAC (C-0007, C-0015, C-0037, C-0186, C-0267)

Entry: `controllers-rbac-is-the-minimal-cr-and-job-lifecycle`.

It matches the six controllers' ServiceAccounts and Deployments in every target. The status server and egress broker
pass these controls, so they need no entry.

The controls read RBAC as a worst case: "roles with delete capabilities" (C-0007), "list Kubernetes secrets" (C-0015,
C-0186), "CoreDNS poisoning" (C-0037), and "cluster takeover roles" (C-0267). The roles in
`deploy/kustomize/base/rbac.yaml` are narrower than that:

- Every role is a namespaced Role, except the review-only ClusterRoles that allow `create` on SubjectAccessReviews
  (status server, evaluation controller) and TokenReviews (egress broker).
- In the `patchy` namespace, the controllers read named Secrets (the Forge and Integration credentials) with `get` only.
  Nothing lists or watches Secrets.
- The investigation, remediation and evaluation controllers create, get and update ConfigMaps in the `patchy` namespace:
  the per-run transcripts and the per-unit evaluation results, each addressed by a fixed name. C-0037 assumes a
  ConfigMap write could reach the `coredns` ConfigMap in `kube-system`, which a Role in another namespace cannot do.
- In the `patchy-agents` namespace, those three controllers share the `patchy-agent-jobs` Role. It allows exactly the
  Job lifecycle: create, get, list, watch and delete on Jobs; get, list and watch on pods; get on pod logs; and create,
  get, update and delete on Secrets. Those Secrets are the per-Job handoff Secrets, which hold the issue markdown and no
  credentials. `update` owner-references each one to its Job, and `delete` cleans up when creating the Job fails.
- The `delete` verbs on Findings and related CRs serve the TTL cleanup and the status page's reset action.

## Mounted Secrets and ConfigMaps (C-0255, C-0258)

Entries: `controllers-c0255-c0258-mount-their-own-credentials`, `status-server-c0255-c0258-mounts-its-own-auth-config`,
`egress-broker-c0255-c0258-mounts-the-model-credential`.

These fire only in the `dev` and `dev-fake` overlays. Those are the only targets that include real Secret and ConfigMap
objects (`deploy/kustomize/overlays/dev/secret-dev.yaml` and `static-context.yaml`), which kubescape can then link to
the mounts. They match:

- the context-controller, which mounts the `patchy-static-context` ConfigMap (the dev static context map, C-0258);
- the status server, which mounts the `patchy-status-auth` Secret (its auth config, C-0255);
- the egress broker, which mounts the `patchy-anthropic` Secret (the model credential, C-0255).

Each workload mounting its own configuration is the intended design. The broker is the one place a model credential
lives, and it is mounted as a file. Flagging "this workload can read the Secret it mounts" adds nothing here.

## configmaps-c0012-key-name-regex-false-positives (C-0012)

It matches the shared `patchy-config` ConfigMap in every target. The name pattern also covers the per-component
`patchy-<name>-config` ConfigMaps the Helm chart renders.

C-0012 matches key names against words such as `secret`, `key` and `token`. Every key it flags holds a value that is not
a credential:

- `PATCHY_ANTHROPIC_API_KEY_FILE` is a mount path (`/etc/patchy/anthropic/api-key`).
- `PATCHY_ANTHROPIC_AUTH` is the auth mode (`key`).
- `PATCHY_INVESTIGATE_TOKEN_BUDGET` and `PATCHY_REMEDIATE_{AUTO,MANUAL}_TOKEN_BUDGET` are LLM token budgets (integers).
- In the Helm chart, `PATCHY_TOKEN_AUDIENCE` is the audience brokered agent tokens are bound to.

The real credentials (webhook secrets, the GitHub App key, model API keys) are Secrets, and never appear in a ConfigMap.

## images-c0236-c0237-signed-but-scanner-cannot-see-it (C-0236, C-0237)

It matches every Deployment in every target (C-0237; C-0236 does not fire with the cached control set).

The images are signed. The `docker_signs` step in `.goreleaser.yaml` runs keyless cosign in the release workflow, using
the GitHub Actions OIDC identity. cosign 3.x attaches signatures in the sigstore bundle format
(`application/vnd.dev.sigstore.bundle.v0.3+json`) through the OCI referrers API. Kubescape still looks for the legacy
`sha256-<digest>.sig` tag, so it finds nothing to check. To verify a release image yourself:

```sh
cosign verify \
  --certificate-identity-regexp '^https://github.com/bitwise-media-group/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/bitwise-media-group/patchy/source-controller:vX.Y.Z
```

Remove this entry once kubescape's signature rules understand the bundle format.

## dev-webhook-ingress-c0263-tls-terminates-at-the-tunnel (C-0263)

It matches the `patchy-integration` Ingress in the `dev` and `dev-fake` overlays
(`deploy/kustomize/overlays/dev/ingress-integration.yaml`).

This Ingress only exists on dev clusters. GitHub's HTTPS connection ends at the tunnel that forwards webhooks to the
cluster (see `docs/deployment/colima.md`), so the Ingress itself has no TLS. Production installs put their own Ingress
or Gateway, with TLS, in front of the integration-controller.

## agents-egress-c0299-selects-runtime-created-job-pods (C-0299)

It matches the `patchy-agents-egress` NetworkPolicy in `base`, `dev` and `dev-fake`. (`prod` uses the Cilium component,
which replaces this policy.)

The policy selects the agent pods (`app.kubernetes.io/name: patchy-agent`). No manifest declares those pods: the
investigation, remediation and evaluation controllers create the Jobs at runtime (`internal/jobs`). A static scan
therefore finds no workload that the policy selects, and reports it as dangling. Under the namespace's default deny,
this policy is the agents' only egress allowance. Without it, agent pods could not reach the artifact server or the
egress broker.
