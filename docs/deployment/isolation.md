# Isolation model

Patchy hands untrusted inputs — repository contents and security-alert text — to a coding agent. The deployment is built
so that a prompt-injected or misbehaving agent cannot reach GitHub, the cluster, or anything else it wasn't given. The
controls are layered: credential separation, RBAC, pod security, and network egress.

## Credential separation

The core control is what the agent pod **doesn't** have:

| Credential                                                     | Where it lives                         | Who sees it                                                                       |
| -------------------------------------------------------------- | -------------------------------------- | --------------------------------------------------------------------------------- |
| GitHub App private key                                         | `patchy-github-app` Secret, release ns | Controllers only — never the internet-facing webhook-controller                   |
| Webhook HMAC secret                                            | `patchy-webhook-secret`, release ns    | Controllers + webhook-controller (its only credential)                            |
| Clone token (read, single repo)                                | Per-Job Secret, agent ns               | **Init container only** — unset after clone                                       |
| Push token (write, single repo)                                | Minted on demand, never stored         | remediation-controller only                                                       |
| Model credential (API key or `claude setup-token` OAuth token) | `patchy-anthropic`, agent ns           | Agent container (`ANTHROPIC_API_KEY`, or the configured `--anthropic-secret-env`) |

The remediation-controller mints a short-lived installation token scoped to the one repository with `contents: read`,
places it in an owner-referenced per-Job Secret (garbage-collected with the Job), and mounts it into the **init
container** only. The init script fetches the pinned base commit (depth 1, no tags) with the token in a per-invocation
header and unsets `GITHUB_TOKEN` — no GitHub credential exists anywhere in the agent container's environment or
filesystem. The branch push happens later, controller-side, through the GitHub API with a separately minted
`contents: write` token.

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

### Why Cilium is preferred

Both layers render the same four-host allowlist, but they are not equivalent, and the prod overlay picks Cilium
deliberately:

- **DNS exfiltration.** Istio enforces at the TCP/TLS layer — the `Sidecar` + `ServiceEntry` pair matches HTTPS flows by
  SNI — but the pod still needs UDP/53 to the cluster resolver, and the proxy does not constrain what names it may
  resolve. A prompt-injected agent can encode data into query names (`<chunk>.attacker.example`) and walk it out through
  the resolver with every other route blocked. Cilium's transparent DNS proxy answers only the allowlisted names and
  drops everything else, closing that channel; the learned IPs then bound the L3/L4 rules, so skipping DNS and dialling
  a raw address is blocked too.
- **Enforcement point.** The sidecar and its traffic redirection live inside the pod's own network namespace, where a
  sufficiently privileged process could bypass them. Cilium enforces in eBPF on the node, outside anything the workload
  can touch.
- **Job friction.** The native-sidecar and CNI-node-agent requirements above exist purely to make the mesh coexist with
  a short-lived Job; Cilium adds nothing to the pod.

Use Istio only when the cluster already runs the mesh and Cilium is not an option, and treat its allowlist as the weaker
fence: it narrows HTTPS egress but leaves DNS open. Either way, the boundary remains the missing credential (see
[credential separation](#credential-separation)), not the egress layer.

!!! warning "kind is not a sandbox"

    kind's default CNI (kindnet) ignores NetworkPolicy entirely. The dev overlay applying cleanly does not mean the
    egress fence works — verify isolation on a CNI that enforces it.

## What leaves the pod

The agent's only output channel is the `PATCHY-EVENT:` JSONL stream on stdout (parsed by the controller from the pod
log), which carries the remediation as a size-capped structured changeset (`PATCHY_CHANGESET_MAX_BYTES`, 5 MiB) — the
changed files' contents, not git objects. The controller — not the agent — replays that changeset through the GitHub API
to create the branch and opens the pull request, so every GitHub side effect passes through code that validates the
state machine first.
