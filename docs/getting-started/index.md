# Getting started

**Patchy** turns security findings into reviewed pull requests. Alerts arrive by webhook (GitHub code scanning, Google
Cloud Security Command Center, Wiz, or your own scanner through the
[generic integration](../integrations/sources/generic.md)) and accumulate into `Finding` custom resources for an hour,
each projected to a GitHub tracking issue. Ownership context is added, and a sandboxed coding agent investigates each
finding: false positives are dismissed, human-only work is handed to the repository owner, and high-confidence
remediations are queued in priority order, attempted automatically, and opened as pull requests for human review. The
CRs carry all of the state; the Kubernetes API is the only state store.

Deploying the stack takes three steps, each with its own page:

1. **[Create the GitHub App](github-app.md)** — register the App, grant five repository permissions, subscribe four
   webhook events, and collect the App ID, private key and webhook secret.
2. **[Install with Helm](install.md)** — create the two Kubernetes Secrets, install the chart from the OCI registry, and
   switch the pipeline on with an `Integration` and a `Forge`. (A [kustomize tree](../deployment/kustomize.md) renders
   the same stack if you prefer.)
3. **[Verify the pipeline](verify.md)** — follow one finding from alert to pull request.

## What you need

| Prerequisite       | Why                                                                                                                                                                                                                                         |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Kubernetes ≥ 1.34  | The chart's `kubeVersion` floor (the oldest line not yet end-of-life); both namespaces enforce the `restricted` Pod Security Standard.                                                                                                      |
| Helm ≥ 3.8         | OCI registry support for `helm install oci://…`.                                                                                                                                                                                            |
| GitHub org admin   | To register the GitHub App and install it on the repositories patchy should watch.                                                                                                                                                          |
| A finding source   | GitHub code scanning (GHAS / CodeQL) on the watched repositories is the path this guide follows; [other sources](../integrations/index.md) plug in the same way.                                                                            |
| A model credential | An Anthropic API key or `claude setup-token` OAuth token by default; Amazon Bedrock, Google Vertex AI, Microsoft Foundry, or OpenAI and Azure OpenAI for the codex runner also work. See [Models and credentials](../deployment/models.md). |
| Inbound HTTPS      | GitHub must reach the integration-controller's `/github/webhooks` — enable the chart's `webhook.ingress` or `webhook.httpRoute`.                                                                                                            |

!!! tip "Hostname-level egress needs Cilium, GKE Dataplane V2 or Istio"

    The chart always renders baseline `NetworkPolicy` objects, but plain L3/L4 policies cannot match hostnames. The
    claude and codex runners need no hostname layer at all — their model traffic goes through the in-cluster egress
    credential broker, so their egress is entirely cluster-local. Where a hostname layer is wanted (it drops the base
    policy's broad 443 rule), `agent.networkPolicy.mode` selects the dialect your infrastructure enforces — `cilium`,
    `gke` or `istio` — and defaults to `auto`, which detects it from the cluster itself. There are deliberately no GitHub hosts in the
    allowlist — the agent pod never talks to a forge. See the [isolation model](../concepts/isolation.md) for what
    each layer requires.

## The moving parts you will deploy

| Component                  | Runs as                   | Concern                                                                                        |
| -------------------------- | ------------------------- | ---------------------------------------------------------------------------------------------- |
| `integration-controller`   | Deployment (1 replica)    | The one internet-facing entry point: webhooks in, Findings ingested, tracking issues projected |
| `source-controller`        | Deployment (1 replica)    | `Forge`/`Repository` reconcilers; SHA-pinned repository tarballs served to the agents          |
| `context-controller`       | Deployment (1 replica)    | Enhance freshly opened findings with ownership and infrastructure context                      |
| `investigation-controller` | Deployment (1 replica)    | Gate eligible findings, run analysis agent Jobs, route the verdicts                            |
| `remediation-controller`   | Deployment (1 replica)    | Priority-ordered remediation Jobs, branch push and pull requests, rollup stats and the TTL     |
| `egress-broker`            | Deployment (1 replica)    | Holds the model credential; every agent model request goes through it                          |
| `status-server`            | Deployment (1 replica)    | The [status page](../operate/status-page.md): findings, metrics and human actions in a browser |
| `agent-runner`             | Ephemeral Job per attempt | In-pod coding-agent runtime: investigate or remediate with `claude -p` or `codex exec`         |

The optional [evaluation-controller](../concepts/evaluation.md) is off by default. Every Deployment is a singleton by
construction (`replicas: 1` with `strategy: Recreate`); the controllers' leader-election Lease is insurance against a
botched rollout, not a scaling mechanism. Do not scale the Deployments.
