# Kubescape exceptions

`exceptions.json` is auto-loaded by the kubescape gates — `.mise/hack/lint-helm.sh` and `.mise/hack/lint-kustomize.sh`
(the `.grype.yaml` of those gates). JSON carries no comments, so the rationale for each accepted finding lives here.
Every entry is an accepted design decision or a scanner false positive — none is a deferred fix. Resource `name`
attributes are regexes; the leading `.*` absorbs the release-name prefix.

## controllers-c0211-selinux-options-are-host-specific (C-0211)

The deployments already set the full security context: `runAsNonRoot`, non-zero uid/gid, `fsGroup` +
`fsGroupChangePolicy`, seccomp `RuntimeDefault`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem`, all
capabilities dropped. The only field C-0211 still wants is `seLinuxOptions`, and an MCS level is a property of the node
OS / cluster policy, not something a portable chart can invent.

## remediation-controller-needs-the-kubernetes-api (C-0190, C-0034, C-0053, C-0261)

The remediation-controller's entire job is driving the Kubernetes API: it creates the ephemeral agent Jobs and reads
their pod logs (internal/jobs, internal/remedctrl). Its token mount is deliberate and unique — every other
ServiceAccount in the chart (source, context, webhook, agent) sets `automountServiceAccountToken: false`. The scanner
flags any mounted token; it cannot know this one is load-bearing.

## remediation-controller-rbac-is-the-minimal-job-lifecycle (C-0007, C-0015, C-0037, C-0186, C-0267)

The Role (templates/rbac.yaml) is namespace-scoped to the agent namespace and its verbs are exactly what the Job
lifecycle needs: create/get/delete on jobs and the per-Job Secret/ConfigMap (always addressed by name — no
`list`/`watch` on secrets), update to owner-reference them to their Job, delete for cleanup. "Cluster takeover" / "list
secrets" / "CoreDNS poisoning" are the scanner's worst-case reading of `create`+`update`+`delete` — C-0037 in particular
assumes any configmap write could reach kube-system's `coredns` ConfigMap, which a Role bound only in the agent
namespace cannot; the blast radius is one namespace that holds only the agent pods.

## configmaps-c0012-key-name-regex-false-positives (C-0012)

C-0012 pattern-matches key NAMES against `secret|key|token|...`. Covers the per-controller helm ConfigMaps and the
kustomize base's shared `patchy-config`. Every flagged key holds a non-credential value: `*_FILE` keys are mount paths,
`PATCHY_ANTHROPIC_SECRET`/`_KEY`/`_ENV` name the Secret object, its key, and the env var the agent Job should use, and
`PATCHY_*_TOKEN_BUDGET` are integer LLM token budgets. The actual credentials (webhook HMAC, App private key) are
file-mounted Secrets and never appear in a ConfigMap.

## controllers-c0207-github-app-id-is-not-a-credential (C-0207)

`PATCHY_GITHUB_APP_ID` arrives as env via `secretKeyRef`. The App ID is a public identifier (it appears in webhook
payloads and on the App's public page); it lives in the App Secret only so the App's identity is one object. The
sensitive half — the private key — is file-mounted (`PATCHY_GITHUB_APP_PRIVATE_KEY_FILE`), which is exactly what this
control asks for.

## controllers-c0255-c0258-mount-their-own-credentials (C-0255, C-0258)

Fires only in the dev overlay, the one variant that renders real `Secret` objects (`secret-dev.yaml`, throwaway kind
credentials) and the static-context ConfigMap — giving the scanner Secret/ConfigMap resources to correlate against the
controllers' mounts. The mounts are the design: every controller file-mounts the webhook HMAC secret, and the
GitHub-facing three mount the App key (charts/patchy values and base manifests do the same; those scans just have no
Secret objects in scope). Flagging "workload can read a secret it mounts" is tautological here.

## agents-egress-c0299-selects-runtime-created-job-pods (C-0299)

`patchy-agents-egress` selects the agent pods (`app.kubernetes.io/name: patchy-agent`), which no manifest declares: the
investigation/remediation/evaluation controllers create those Jobs at runtime (internal/jobs). A static scan of the
rendered manifests therefore never finds a workload the policy matches and reports it as dangling. The policy is the
agents' only egress allowance under the namespace's default deny — removing it would cut the sandbox off from the
artifact server and the egress broker.

## images-c0236-c0237-signed-but-scanner-cannot-see-it (C-0236, C-0237)

The images ARE signed: GoReleaser's `docker_signs` runs keyless cosign in the release workflow (GitHub Actions OIDC
identity), which as of cosign 3.x attaches signatures in the new sigstore bundle format
(`application/vnd.dev.sigstore.bundle.v0.3+json`) via the OCI referrers API — not the legacy `sha256-<digest>.sig` tag
scheme kubescape's check looks for. Verify for yourself:

    cosign verify \
      --certificate-identity-regexp '^https://github.com/bitwise-media-group/' \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com \
      ghcr.io/bitwise-media-group/patchy/source-controller:v0.3.0

C-0236 (verify the signature) fails for the same reason C-0237 (a signature exists) does: it finds no legacy `.sig` tag
to verify. Drop this entry if/when kubescape's signature rules learn the bundle format.
