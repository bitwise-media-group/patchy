# Getting started

**Patchy** turns GitHub Advanced Security findings into reviewed pull requests. CodeQL alerts arrive via webhook and
accumulate into GitHub issues for an hour, ownership context is added, and a sandboxed coding agent classifies each
finding — false positives are dismissed, human-only work is assigned to the repository owner, and high-confidence
remediations are opened as pull requests for human review. Labels on the issue carry all of the state; GitHub is the
only state store.

Deploying the stack takes three steps, each with its own page:

1. **[Create the GitHub App](github-app.md)** — register the App, grant four repository permissions, subscribe four
   webhook events, and collect the App ID, private key and webhook secret.
2. **[Install with Helm](install.md)** — create the three Kubernetes Secrets and install the chart from the OCI
   registry. (A [kustomize tree](../deployment/kustomize.md) renders the same stack if you prefer.)
3. **[Verify the pipeline](verify.md)** — follow one finding from alert to pull request.

## What you need

| Prerequisite         | Why                                                                                                     |
| -------------------- | ------------------------------------------------------------------------------------------------------- |
| Kubernetes ≥ 1.25    | The chart's `kubeVersion` floor; both namespaces enforce the `restricted` Pod Security Standard.        |
| Helm ≥ 3.8           | OCI registry support for `helm install oci://…`.                                                        |
| GitHub org admin     | To register the GitHub App and install it on the repositories patchy should watch.                      |
| GHAS / CodeQL        | Code scanning must be enabled on the watched repositories — its alerts are the finding source.          |
| An Anthropic API key | The agent classifies and remediates via the `claude` CLI inside the sandbox pod.                        |
| Inbound HTTPS        | GitHub must reach the webhook-controller — enable the chart's `webhook.ingress` or `webhook.httpRoute`. |

!!! tip "Hostname-level egress needs Cilium or Istio"

    The chart always renders baseline `NetworkPolicy` objects, but plain L3/L4 policies cannot match hostnames. To
    enforce the agent sandbox's egress allowlist (`api.anthropic.com` and the GitHub clone hosts) at the DNS level,
    enable exactly one of `agent.networkPolicy.cilium.enabled` or `agent.networkPolicy.istio.enabled`. See the
    [isolation model](../deployment/isolation.md) for what each requires.

## The moving parts you will deploy

| Component                | Runs as                   | Concern                                                               |
| ------------------------ | ------------------------- | --------------------------------------------------------------------- |
| `source-controller`      | Deployment (1 replica)    | Receive `code_scanning_alert` webhooks, open + accumulate issues      |
| `context-controller`     | Deployment (1 replica)    | Enhance new issues with ownership / infrastructure context            |
| `remediation-controller` | Deployment (1 replica)    | Run agent Jobs in Kubernetes and apply **all** GitHub side effects    |
| `agent-runner`           | Ephemeral Job per finding | In-pod coding-agent runtime: classify, then remediate via `claude -p` |

All three controllers are deliberate singletons — the state machine is GitHub issue labels and there is no leader
election, so do not scale the Deployments.
