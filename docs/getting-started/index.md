# Getting started

**Patchy** turns GitHub Advanced Security findings into reviewed pull requests. CodeQL alerts arrive via webhook and
accumulate into `Finding` custom resources for an hour — each projected to a GitHub tracking issue — ownership context
is added, and a sandboxed coding agent investigates each finding: false positives are dismissed, human-only work is
handed to the repository owner, and high-confidence remediations are queued in priority order, attempted automatically,
and opened as pull requests for human review. The CRs carry all of the state; the Kubernetes API is the only state
store.

Deploying the stack takes three steps, each with its own page:

1. **[Create the GitHub App](github-app.md)** — register the App, grant four repository permissions, subscribe four
   webhook events, and collect the App ID, private key and webhook secret.
2. **[Install with Helm](install.md)** — create the two Kubernetes Secrets, install the chart from the OCI registry, and
   switch the pipeline on with an `Integration` and a `Forge`. (A [kustomize tree](../deployment/kustomize.md) renders
   the same stack if you prefer.)
3. **[Verify the pipeline](verify.md)** — follow one finding from alert to pull request.

## What you need

| Prerequisite            | Why                                                                                                                                    |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| Kubernetes ≥ 1.34       | The chart's `kubeVersion` floor (the oldest line not yet end-of-life); both namespaces enforce the `restricted` Pod Security Standard. |
| Helm ≥ 3.8              | OCI registry support for `helm install oci://…`.                                                                                       |
| GitHub org admin        | To register the GitHub App and install it on the repositories patchy should watch.                                                     |
| GHAS / CodeQL           | Code scanning must be enabled on the watched repositories — its alerts are the finding source.                                         |
| An Anthropic credential | An API key or a `claude setup-token` OAuth token — the agent investigates and remediates via the `claude` CLI inside the sandbox pod.  |
| Inbound HTTPS           | GitHub must reach the integration-controller's `/github/webhooks` — enable the chart's `webhook.ingress` or `webhook.httpRoute`.       |

!!! tip "Hostname-level egress needs Cilium or Istio"

    The chart always renders baseline `NetworkPolicy` objects, but plain L3/L4 policies cannot match hostnames. To
    pin the agent sandbox's egress to `api.anthropic.com` at the DNS level, enable exactly one of
    `agent.networkPolicy.cilium.enabled` or `agent.networkPolicy.istio.enabled`. There are deliberately no GitHub
    hosts in the allowlist — the agent pod never talks to a forge. See the
    [isolation model](../deployment/isolation.md) for what each layer requires.

## The moving parts you will deploy

| Component                  | Runs as                   | Concern                                                                                        |
| -------------------------- | ------------------------- | ---------------------------------------------------------------------------------------------- |
| `integration-controller`   | Deployment (1 replica)    | The one internet-facing entry point: webhooks in, Findings ingested, tracking issues projected |
| `source-controller`        | Deployment (1 replica)    | `Forge`/`Repository` reconcilers; SHA-pinned repository tarballs served to the agents          |
| `context-controller`       | Deployment (1 replica)    | Enhance freshly opened findings with ownership / infrastructure context                        |
| `investigation-controller` | Deployment (1 replica)    | Gate eligible findings, run analysis agent Jobs, route the verdicts                            |
| `remediation-controller`   | Deployment (1 replica)    | Priority-ordered remediation Jobs, branch push + pull requests, rollup stats and the TTL       |
| `agent-runner`             | Ephemeral Job per attempt | In-pod coding-agent runtime: investigate or remediate via `claude -p`                          |

All five controllers are singletons by construction — `replicas: 1` with `strategy: Recreate`; the leader-election Lease
each one takes is insurance against a botched rollout, not a scaling mechanism. Do not scale the Deployments.
