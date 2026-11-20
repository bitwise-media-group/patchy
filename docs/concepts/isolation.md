# Isolation model

Patchy hands untrusted inputs (repository contents and security-alert text) to a coding agent. The design calls for an
agent with "no internet access and no GitHub credentials". Both shipped runners (claude and codex) meet both halves of
that. Their model traffic goes through the in-cluster [egress credential broker](../configuration/egress-broker.md), so
the pod connects to no external host at all. The protection comes in layers: credential absence, RBAC, pod security, and
network egress. The first layer matters most.

## Credential absence: the main protection

The agent pod holds **no credential of any kind, in any container**. There is no clone token in the init container,
because nothing is cloned. The repository arrives as a tarball from the source-controller's in-cluster artifact server:

- The URL carries an unguessable 128-bit id.
- The Job pins the tarball's sha256 digest.
- The init container checks the digest before it extracts the tarball and builds the local git base.

The claude and codex runners hold no model key either. The broker adds or signs the model credential on the way out. The
pod authenticates to the broker with an audience-bound projected ServiceAccount token. That token **identifies the pod
but grants nothing by itself**: it names the pod to the broker, and nothing else accepts it. If an attacker steals it,
all they get is model access through the broker, which audits every request.

The per-Job Secret carries only handoff markdown. `internal/jobs` lists `GITHUB_TOKEN` (and every other credential
channel) as a reserved env name, so no configuration can slip a credential in.

| Credential                                          | Where it lives                                                                     | Who sees it                                                                                                                |
| --------------------------------------------------- | ---------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| GitHub App key / token, webhook secret              | `patchy-github` Secret, release ns                                                 | Read on demand through the API by the controllers the `Integration`/`Forge` CRs point at. Never mounted into a Deployment. |
| Read token (single repo, archive download)          | Minted on demand, never stored                                                     | source-controller only                                                                                                     |
| Write token (single repo, push + PR)                | Minted on demand, never stored                                                     | remediation-controller only                                                                                                |
| Claude model credential (API key or cloud identity) | `patchy-anthropic` Secret, **release ns**, or the broker's cloud workload identity | The egress broker only. **Never an agent pod.**                                                                            |
| Codex model credential (API key or Entra identity)  | `patchy-openai` Secret, **release ns**, or the broker's Azure workload identity    | The egress broker only. **Never an agent pod.**                                                                            |

All GitHub side effects (issue projection, alert dismissal, branch push, pull requests) happen in the controllers, using
short-lived tokens scoped to one repository. An agent that reaches `github.com` reaches it as an anonymous member of the
public.

## RBAC

Every controller identity gets a `Role` in the release namespace that lists each verb it needs: its own CRs and status
subresources, `get` on Secrets, and its leader-election Lease. The two job controllers also share a Role in
`patchy-agents`. It covers only what `internal/jobs` uses:

- create/get/list/watch/delete on `batch/jobs`
- get/list/watch on pods
- `pods/log` (the `PATCHY-EVENT:` stream)
- create/get/delete on the per-Job handoff `secrets`

The agent ServiceAccount (`patchy-agent`) has **no Role at all**, and the pods run with
`automountServiceAccountToken: false`. If the agent pod could reach the Kubernetes API, a prompt-injected agent could
read the Secrets the isolation model depends on. A brokered (claude, codex) pod does mount a **projected**
ServiceAccount token, but it is bound to the broker's audience (`patchy-egress-broker`). The API server rejects it as a
Kubernetes API credential. The broker validates it with `TokenReview` and is the only party that accepts it. The
broker's own identity is equally narrow: its only RBAC permission is `create tokenreviews`. It has no Secret API access
of any kind, because its credential files arrive as mounted volumes.

## Pod security

Both namespaces enforce the `restricted` Pod Security Standard. The chart labels the agent namespace it creates;
[label the release namespace yourself](../getting-started/install.md#create-the-namespaces). Every pod (controllers and
agent Jobs) runs:

- as non-root uid 65532
- with a read-only root filesystem
- with all capabilities dropped
- with the `RuntimeDefault` seccomp profile

Writable paths are emptyDir mounts (`/tmp`, `/workspace`). Agent Jobs run with `backoffLimit: 0` and
`restartPolicy: Never`, because retries belong to the state machine, not to Kubernetes. An `activeDeadlineSeconds` limit
(`--job-deadline`) kills a Job that runs too long.

## Network egress: the floor and the fence

**The baseline NetworkPolicy is the floor.** `patchy-agents` denies all traffic in both directions by default. It then
allows these outbound connections again:

- DNS
- the artifact port (9790) to source-controller
- the broker port (8080) to the egress broker
- TCP 443, excluding the cluster's own ranges and the cloud metadata endpoint (`169.254.169.254`)

Set `agent.networkPolicy.clusterCIDRs` (Helm) or the `except:` CIDRs in `base/networkpolicy.yaml` (kustomize) to your
cluster's pod, service and node CIDRs. A **claude** or **codex** pod uses only the first three: DNS, the artifact
server, and the broker. All of its egress stays inside the cluster. No runner patchy ships needs the 443 rule any more.
It remains as the L3 floor for a future runner that does not use the broker. Be aware of its limit: **a plain
NetworkPolicy works at L3/L4 and cannot match a hostname**. So "TCP 443" means every HTTPS host on the internet, not
only the model vendor's.

To restrict egress by hostname you need one of three optional layers: **Cilium**, **GKE Dataplane V2**, or **Istio**.
Select it with `agent.networkPolicy.mode` (Helm) or the matching kustomize component. The brokered runners have **no
external hosts to allowlist**. So for claude and codex these layers do two things today:

- They drop the base policy's broad 443 rule.
- Cilium also restricts DNS to cluster-local names, which closes the DNS exfiltration channel described
  [below](#why-cilium-is-preferred).

**No GitHub hosts appear in any of them.** The pod never talks to a forge, so there is nothing to allow.

The default, `mode: auto`, picks the layer from the cluster's own API on every render against a live cluster:

- `gke` when the `FQDNNetworkPolicy` CRD is present.
- `cilium` when a real Cilium is present. It never picks `cilium` on GKE, where a `CiliumNetworkPolicy` would enforce
  nothing but would look like protection.
- Nothing otherwise.

It never picks `istio`, because the CRD being present says nothing about native sidecars. An off-cluster `helm template`
sees no cluster and resolves to `none`.

??? note "The three hostname layers in detail"

    Each layer renders one policy per runner. The per-host rules exist for a runner that does not use the broker; no
    runner patchy ships uses them.

    - **Cilium** (`mode: cilium`, or the kustomize `components/cilium`, which the prod overlay uses): one
      `CiliumNetworkPolicy` per runner. Its `toFQDNs` names that runner's model hosts. A DNS rule limits which names the
      pod may resolve: the runner's `dnsPatterns` and `*.svc.cluster.local`, so the artifact fetch still resolves. The
      claude and codex policies allow only cluster traffic: DNS, the artifact server and the broker. They have no
      `toFQDNs` and no external names resolve at all. Requires Cilium with the DNS proxy.
    - **GKE Dataplane V2** (`mode: gke`, or `components/gke-fqdn`): an `FQDNNetworkPolicy` (`networking.gke.io/v1alpha1`)
      naming the runner's hosts on 443. It is skipped for the brokered runners. Dataplane V2 _is_ Cilium, but it has not
      honoured the `CiliumNetworkPolicy` CRD since 1.21.5-gke.1300 and rejects every L7 rule. This policy is its
      equivalent, with `anetd` reading DNS answers in place of Cilium's proxy. It requires a cluster created or updated
      with `--enable-fqdn-network-policy` (Preview; kube-dns or Cloud DNS; at most 50 resolved addresses per name). It
      cannot express DNS or a ClusterIP destination, so DNS and the artifact fetch stay with the base policy. It does
      **not** close the DNS exfiltration channel.
    - **Istio** (`mode: istio`, or `components/istio`): the same allowlist as a `Sidecar` in `REGISTRY_ONLY` mode. It
      exposes only that runner's model ServiceEntry (none for claude or codex) and the release namespace's Services,
      matched by SNI. It has two hard requirements:
        - **Native sidecars** (Kubernetes ≥ 1.29 and istiod with `ENABLE_NATIVE_SIDECARS=true`). A classic sidecar
          never terminates, which hangs the Job, and it blocks the init container's artifact fetch.
        - The **Istio CNI node agent**, because the `restricted` PSS rejects `istio-init`'s NET_ADMIN/NET_RAW.

!!! danger "Network policies are additive"

    A packet matching *any* policy that selects the pod is allowed, and no policy can subtract from another. So an FQDN
    allowlist next to a NetworkPolicy that already permits 443 to `0.0.0.0/0` restricts **nothing**. The union is
    still "443 to anywhere". So whenever an FQDN layer is active, the chart drops that broad rule
    (`agent.networkPolicy.broadEgress: auto`), and the kustomize components patch it out of the base policy. If you
    assemble these by hand, do the same, or the fence does nothing. Set `broadEgress: always` to keep the broad rule while you
    try out a newly enabled layer. If that layer turns out not to enforce, traffic still flows instead
    of every Job being cut off.

Enabling both Cilium and Istio fails the chart render. If you have none of the three, the base policy still applies and
is your only L3/L4 control.

### Why Cilium is preferred

All three render the same allowlist, but they are not equivalent. The prod overlay picks Cilium for the reasons below.
(On GKE you have no choice: `gke` is the only one Dataplane V2 enforces.)

- **DNS exfiltration.** Istio enforces at the TCP/TLS layer, but the pod still needs UDP/53 to the cluster resolver, and
  the proxy does not limit which names it may resolve. A prompt-injected agent can encode data into query names
  (`<chunk>.attacker.example`) and send it out through the resolver while every other route is blocked. Cilium's
  transparent DNS proxy answers only the allowlisted patterns and drops everything else, which closes that channel. The
  IPs it learns then limit the L3/L4 rules, so skipping DNS and dialling a raw address is blocked too.
- **Enforcement point.** The sidecar and its traffic redirection live inside the pod's own network namespace, where a
  process with enough privilege could bypass them. Cilium enforces in eBPF on the node, outside anything the workload
  can touch.
- **Job friction.** The native-sidecar and CNI-node-agent requirements exist only to make the mesh work with a
  short-lived Job. Cilium adds nothing to the pod.

Either way, the FQDN policy is not the boundary. The missing credential is the boundary. The egress layers limit what a
compromised agent can talk to. They do not limit what it can do to your forge. The missing credential does that.

!!! warning "kind is not a sandbox"

    kind's default CNI (kindnet) ignores NetworkPolicy entirely. The dev overlay applying cleanly does not mean the
    egress fence works. Verify isolation on a CNI that enforces it. k3s on [Colima](../deployment/colima.md) enforces
    the L3/L4 floor; the FQDN layer needs a real Cilium or Istio cluster.

## Egress proxies

The **controllers'** NetworkPolicies (separate from the agent sandbox above) allow only their declared egress: DNS,
443/6443, and each controller's specific peers. You can route forge traffic through a corporate forward proxy with the
chart's [`proxy` values](../getting-started/install.md#egress-proxy) or a `spec.proxy` on a Forge/GitHub Integration.
That adds a new destination. So a proxy listening on a port like 3128 or 8080 needs a matching
`<controller>.networkPolicy.extraEgress` rule on every controller that talks to GitHub (integration, source,
remediation).

Get `NO_PROXY` right when you set the environment-wide proxy. In-cluster traffic must not go out through an external
proxy. That includes the artifact endpoint on source-controller (:9790), the egress broker, and the Kubernetes API
server. The chart derives `localhost,127.0.0.1,.svc,.cluster.local` automatically and appends your `noProxy`. If you set
the variables another way (a kustomize patch of the ConfigMap, for instance), add those suffixes yourself. A
`spec.proxy` on a resource applies to everything: it routes **all** of that resource's GitHub traffic through the named
proxy, and the environment's `NO_PROXY` makes no exceptions.

## What leaves the pod

The agent's only output channel is the `PATCHY-EVENT:` JSONL stream on stdout. The owning controller parses it from the
pod log. The stream carries the remediation as a size-capped structured changeset (`PATCHY_CHANGESET_MAX_BYTES`, 5 MiB):
the changed files' contents against the pinned base SHA, not git objects. The remediation-controller then does the forge
work, and the agent does none of it:

1. It verifies the commit claim.
2. It replays the changeset through the GitHub Git Data API to create the `patchy/<finding>` branch.
3. It opens the pull request.

So every forge side effect passes through code that checks the state machine first.
