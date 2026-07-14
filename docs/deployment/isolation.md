# Isolation model

Patchy hands untrusted inputs — repository contents and security-alert text — to a coding agent. The deployment is built
so that a prompt-injected or misbehaving agent cannot reach GitHub, the cluster, or anything else it wasn't given. The
controls are layered: credential separation, RBAC, pod security, and network egress.

## Credential separation

The core control is what the agent pod **doesn't** have:

| Credential                      | Where it lives                         | Who sees it                                                     |
| ------------------------------- | -------------------------------------- | --------------------------------------------------------------- |
| GitHub App private key          | `patchy-github-app` Secret, release ns | Controllers only — never the internet-facing webhook-controller |
| Webhook HMAC secret             | `patchy-webhook-secret`, release ns    | Controllers + webhook-controller (its only credential)          |
| Clone token (read, single repo) | Per-Job Secret, agent ns               | **Init container only** — unset after clone                     |
| Push token (write, single repo) | Minted on demand, never stored         | remediation-controller only                                     |
| Anthropic API key               | `patchy-anthropic`, agent ns           | Agent container (`ANTHROPIC_API_KEY`)                           |

The remediation-controller mints a short-lived installation token scoped to the one repository with `contents: read`,
places it in an owner-referenced per-Job Secret (garbage-collected with the Job), and mounts it into the **init
container** only. The init script clones, unsets `GITHUB_TOKEN`, and resets the remote URL — no GitHub credential exists
anywhere in the agent container's environment or filesystem. The branch push happens later, controller-side, with a
separately minted `contents: write` token.

## RBAC

Only the remediation-controller talks to the Kubernetes API, and only inside the agent namespace:

```text
batch/jobs      create, get, list, watch, delete
pods            get, list, watch
pods/log        get
secrets         create, get, delete      # its own per-Job Secret, by name — no list/watch
configmaps      create, get, delete
```

The source- and context-controllers run with `automountServiceAccountToken: false` and no Role. The agent ServiceAccount
(`patchy-agent`) has no Role whatsoever and no mounted token — Kubernetes API access from the agent pod would let it
read the very Secrets that isolate it.

## Pod security

Both namespaces enforce the `restricted` Pod Security Standard (the chart labels the agent namespace it creates;
[label the release namespace yourself](../getting-started/install.md#create-the-namespaces)). Every pod — controllers
and agent Jobs — runs as non-root uid 65532 with a read-only root filesystem, all capabilities dropped, and the
`RuntimeDefault` seccomp profile; writable paths are emptyDir mounts (`/tmp`, `/workspace`). Agent Jobs run with
`backoffLimit: 0` and `restartPolicy: Never` — retries belong to the state machine, not to Kubernetes — under an
`activeDeadlineSeconds` kill switch (`--job-deadline`).

## Network egress

The baseline NetworkPolicies (always rendered) hold the agent namespace to cluster-external egress only — the RFC-1918
and link-local ranges in `agent.networkPolicy.clusterCIDRs` are excluded, so the agent cannot reach cluster services.
But plain L3/L4 policies cannot name hostnames; pinning egress to exactly the model API and the GitHub clone hosts takes
one of two optional layers:

- **Cilium** (`agent.networkPolicy.cilium.enabled: true`) — a `CiliumNetworkPolicy` with DNS-aware FQDN rules for
  `api.anthropic.com`, `github.com`, `codeload.github.com`, and `objects.githubusercontent.com`. Requires Cilium with
  the DNS proxy. This is what the prod kustomize overlay uses.
- **Istio** (`agent.networkPolicy.istio.enabled: true`) — a `Sidecar` in REGISTRY_ONLY mode plus `ServiceEntry` objects
  for the same hosts. Two hard requirements: **native sidecars** (Kubernetes ≥ 1.29 and istiod with
  `ENABLE_NATIVE_SIDECARS=true` — a classic sidecar never terminates, hanging the Job, and blackholes the init
  container's clone), and the **Istio CNI node agent** (the `restricted` PSS rejects `istio-init`'s NET_ADMIN/NET_RAW).

Enabling both fails the chart render.

!!! warning "kind is not a sandbox"

    kind's default CNI (kindnet) ignores NetworkPolicy entirely. The dev overlay applying cleanly does not mean the
    egress fence works — verify isolation on a CNI that enforces it.

## What leaves the pod

The agent's only output channels are the `PATCHY-EVENT:` JSONL stream on stdout (parsed by the controller from the pod
log) and a size-capped git bundle (`PATCHY_BUNDLE_MAX_BYTES`, 5 MiB) carrying the changeset. The controller — not the
agent — unpacks the bundle, pushes the branch, and opens the pull request, so every GitHub side effect passes through
code that validates the state machine first.
