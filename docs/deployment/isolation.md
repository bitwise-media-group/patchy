# Isolation model

Patchy hands untrusted inputs — repository contents and security-alert text — to a coding agent. The design calls for an
agent with "no internet access and no GitHub credentials"; taken literally the first half is unachievable, because
`claude -p` **is** a network client of `api.anthropic.com`. What is actually delivered is layered — credential absence,
RBAC, pod security, and network egress — and the first layer is the one that matters.

## Credential absence — the real control

The agent pod holds **no forge credential at all, in any container** — there is no init-container clone token, because
there is no clone. The repository arrives as a tarball from the source-controller's in-cluster artifact server: the URL
carries an unguessable 128-bit id, the Job pins the sha256 digest, and the init container verifies it before extracting
and synthesizing the local git base. The per-Job Secret carries only handoff markdown, and `internal/jobs` lists
`GITHUB_TOKEN` as a reserved env name so no configuration can smuggle a credential in.

| Credential                                                     | Where it lives                     | Who sees it                                                                                                                |
| -------------------------------------------------------------- | ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| GitHub App key / token, webhook secret                         | `patchy-github` Secret, release ns | Read on demand through the API by the controllers the `Integration`/`Forge` CRs point at — never mounted into a Deployment |
| Read token (single repo, archive download)                     | Minted on demand, never stored     | source-controller only                                                                                                     |
| Write token (single repo, push + PR)                           | Minted on demand, never stored     | remediation-controller only                                                                                                |
| Model credential (API key or `claude setup-token` OAuth token) | `patchy-anthropic`, agent ns       | The agent container — **the only secret value in the pod**                                                                 |

All GitHub side effects — issue projection, alert dismissal, branch push, pull requests — happen controller-side with
short-lived, per-repository scoped tokens. An agent that reaches `github.com` reaches it as an anonymous member of the
public.

## RBAC

Every controller identity gets a verb-by-verb `Role` in the release namespace (its own CRs and status subresources,
`get` on Secrets, its leader-election Lease); the two job controllers additionally share a Role in `patchy-agents`
covering exactly what `internal/jobs` uses — create/get/list/watch/delete on `batch/jobs`, get/list/watch pods,
`pods/log` (the `PATCHY-EVENT:` stream), and create/get/delete on the per-Job handoff `secrets`.

The agent ServiceAccount (`patchy-agent`) has **no Role whatsoever** and the pods run with
`automountServiceAccountToken: false` — Kubernetes API access from the agent pod would let a prompt-injected agent read
the very Secrets the isolation model depends on.

## Pod security

Both namespaces enforce the `restricted` Pod Security Standard (the chart labels the agent namespace it creates;
[label the release namespace yourself](../getting-started/install.md#create-the-namespaces)). Every pod — controllers
and agent Jobs — runs as non-root uid 65532 with a read-only root filesystem, all capabilities dropped, and the
`RuntimeDefault` seccomp profile; writable paths are emptyDir mounts (`/tmp`, `/workspace`). Agent Jobs run with
`backoffLimit: 0` and `restartPolicy: Never` — retries belong to the state machine, not to Kubernetes — under an
`activeDeadlineSeconds` kill switch (`--job-deadline`).

## Network egress — the floor and the fence

**The baseline NetworkPolicy is the floor.** `patchy-agents` is default-deny in both directions. Egress is re-permitted
for DNS, the artifact port (9790) to source-controller only, and TCP 443 with the cluster's own ranges and the cloud
metadata endpoint (`169.254.169.254`) excluded — adjust `agent.networkPolicy.clusterCIDRs` (Helm) or the `except:` CIDRs
in `base/networkpolicy.yaml` (kustomize) to your cluster's pod/service/node CIDRs. Be honest about what this is: **a
plain NetworkPolicy is L3/L4 and cannot match a hostname**, so "TCP 443" means every HTTPS host on the internet, not
just Anthropic's.

Pinning egress to hostnames takes one of three optional layers, selected by `agent.networkPolicy.mode` (Helm) or by the
matching kustomize component. The allowlist is deliberately short: `api.anthropic.com` and the in-cluster artifact
endpoint. **No GitHub hosts appear anywhere in it** — the pod never talks to a forge, so there is nothing to allow.

- **Cilium** (`mode: cilium`, or the kustomize `components/cilium` — what the prod overlay uses) — a
  `CiliumNetworkPolicy` with `toFQDNs: api.anthropic.com`, plus a DNS rule constraining what names the pod may resolve
  at all (`*.anthropic.com` and `*.svc.cluster.local`, so the artifact fetch still resolves). Requires Cilium with the
  DNS proxy.
- **GKE Dataplane V2** (`mode: gke`, or `components/gke-fqdn`) — an `FQDNNetworkPolicy` (`networking.gke.io/v1alpha1`)
  naming the same host on 443. Dataplane V2 _is_ Cilium, but it has not honoured the `CiliumNetworkPolicy` CRD since
  1.21.5-gke.1300 and rejects every L7 rule, so the Cilium layer above is inert there — this is its equivalent, with
  `anetd` snooping DNS answers in place of Cilium's proxy. Requires the cluster to be created or updated with
  `--enable-fqdn-network-policy` (Preview; kube-dns or Cloud DNS; at most 50 resolved addresses per name). It cannot
  express DNS or a ClusterIP destination, so DNS and the artifact fetch stay with the base policy — which also means it
  does **not** close the DNS exfiltration channel described below.
- **Istio** (`mode: istio`, or `components/istio`) — the same allowlist as a `Sidecar` in `REGISTRY_ONLY` mode (exposing
  only the `api.anthropic.com` ServiceEntry and the release namespace's artifact Service), matched by SNI. Two hard
  requirements: **native sidecars** (Kubernetes ≥ 1.29 and istiod with `ENABLE_NATIVE_SIDECARS=true` — a classic sidecar
  never terminates, hanging the Job, and blackholes the init container's artifact fetch), and the **Istio CNI node
  agent** (the `restricted` PSS rejects `istio-init`'s NET_ADMIN/NET_RAW).

The default, `mode: auto`, resolves this from the cluster's own API surface on every render against a live cluster, so
one set of manifests can serve a GKE cluster and a Cilium cluster unmodified. It picks `gke` when the
`FQDNNetworkPolicy` CRD is present, `cilium` when a real Cilium is (never on GKE, where a `CiliumNetworkPolicy` would
enforce nothing while reading as protection), and nothing otherwise. It never picks `istio`: CRD presence says nothing
about native sidecars. An off-cluster `helm template` sees no cluster and resolves to `none`.

!!! danger "Network policies are additive"

    A packet matching *any* policy that selects the pod is allowed, and no policy can subtract from another. So an FQDN
    allowlist rendered next to a NetworkPolicy that already permits 443 to `0.0.0.0/0` constrains **nothing** — the
    union is still "443 to anywhere". Whenever an FQDN layer is active the chart therefore drops that broad rule
    (`agent.networkPolicy.broadEgress: auto`), and the kustomize components patch it out of the base policy. If you
    assemble these by hand, do the same, or the fence is decorative. `broadEgress: always` keeps it deliberately while
    soaking a newly enabled layer — a layer that turns out not to enforce then fails open rather than blackholing every
    Job.

Enabling both Cilium and Istio fails the chart render. If you have none of the three, the base policy still applies and
is then the whole of the L3/L4 story.

### Why Cilium is preferred

All three render the same allowlist, but they are not equivalent, and the prod overlay picks Cilium deliberately (on GKE
the choice is made for you — `gke` is the only one Dataplane V2 enforces):

- **DNS exfiltration.** Istio enforces at the TCP/TLS layer, but the pod still needs UDP/53 to the cluster resolver, and
  the proxy does not constrain what names it may resolve. A prompt-injected agent can encode data into query names
  (`<chunk>.attacker.example`) and walk it out through the resolver with every other route blocked. Cilium's transparent
  DNS proxy answers only the allowlisted patterns and drops everything else, closing that channel; the learned IPs then
  bound the L3/L4 rules, so skipping DNS and dialling a raw address is blocked too.
- **Enforcement point.** The sidecar and its traffic redirection live inside the pod's own network namespace, where a
  sufficiently privileged process could bypass them. Cilium enforces in eBPF on the node, outside anything the workload
  can touch.
- **Job friction.** The native-sidecar and CNI-node-agent requirements exist purely to make the mesh coexist with a
  short-lived Job; Cilium adds nothing to the pod.

Either way, do not mistake the FQDN policy for the boundary. The missing credential is the boundary; the egress layers
narrow what a compromised agent can talk to, not what it can do to your forge.

!!! warning "kind is not a sandbox"

    kind's default CNI (kindnet) ignores NetworkPolicy entirely. The dev overlay applying cleanly does not mean the
    egress fence works — verify isolation on a CNI that enforces it (k3s on [Colima](colima.md) enforces the L3/L4
    floor; the FQDN layer needs a real Cilium or Istio cluster).

## What leaves the pod

The agent's only output channel is the `PATCHY-EVENT:` JSONL stream on stdout (parsed by the owning controller from the
pod log), which carries the remediation as a size-capped structured changeset (`PATCHY_CHANGESET_MAX_BYTES`, 5 MiB) —
the changed files' contents against the pinned base SHA, not git objects. The remediation-controller — not the agent —
verifies the commit claim, replays the changeset through the GitHub Git Data API to create the `patchy/<finding>`
branch, and opens the pull request, so every forge side effect passes through code that validates the state machine
first.
