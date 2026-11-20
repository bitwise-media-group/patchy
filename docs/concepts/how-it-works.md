# How it works

Patchy is a set of Go binaries that share one source of truth: the Kubernetes API. The `patchy.bitwisemedia.uk/v1alpha1`
custom resources carry all pipeline state:

- `Integration` and `Forge` (configuration)
- `Finding`, `Repository`, `Investigation` and `Remediation` (the pipeline)
- `FindingRollup` (the all-time statistics)
- the optional `Evaluation` and `EvaluationUnit` ([remote evaluation](evaluation.md))

etcd is the only state store. There is no separate database or queue. GitHub issues are a **one-way, human-facing
projection**: patchy renders their labels and comments from the Finding. Human actions come back in as spec writes.
Patchy never reads issue state back.

All webhooks enter through one component, the **integration-controller**. It serves one route per provider:
`/github/webhooks`, `/google-cloud/webhooks`, `/wiz/webhooks`, and `/generic/<name>/webhooks` for your own scanners. It
checks each delivery against the secrets of your configured `Integration` resources. No other component faces the
scanners.

Webhooks do **not** drive the pipeline forward. Its gates are "accumulation closed", "older than an hour" and "a free
remediation slot". No event can announce these, so the controllers' watch-driven reconcile loops move the pipeline
along. The webhook path carries only new alerts and human-in-the-loop signals.

```mermaid
flowchart TB
    SRC["Scanners<br/>GitHub · SCC · Wiz · generic"] -->|webhooks| IC[integration-controller]
    People(["People"]) -->|"approve · retry · expedite"| SS["status-server<br/>the status page"]
    IC -->|ingest Findings| K8S[("Kubernetes API<br/>the state store")]
    SS -->|spec writes| K8S
    K8S --> CC["context-controller<br/>enhancer chain"]
    K8S --> SC["source-controller<br/>pinned tarballs"]
    K8S --> VC["investigation-controller<br/>gate + analysis Jobs"]
    K8S --> RC["remediation-controller<br/>queue + fix Jobs + PRs"]
    VC -->|Job| AR["agent-runner<br/>sandboxed pod"]
    RC -->|Job| AR
    SC -->|digest-verified tarball| AR
    AR -->|model requests| EB["egress-broker<br/>adds the model credential"]
    IC -->|tracking issues| GH["GitHub<br/>issues + pull requests"]
    RC -->|branch push · pull request| GH
```

Each agent pod reports back to the controller that launched it with a `PATCHY-EVENT:` stream on stdout. The pod never
calls the Kubernetes API or GitHub itself.

The components:

| Component                | Role                                                                                      |
| ------------------------ | ----------------------------------------------------------------------------------------- |
| integration-controller   | Webhooks in, Findings ingested, tracking issues projected, human signals applied          |
| source-controller        | Forge credentials validated, repositories pinned, tarballs served to agent pods           |
| context-controller       | The enhancer chain: ownership and infrastructure context                                  |
| investigation-controller | The gate, analysis agent Jobs, verdict routing                                            |
| remediation-controller   | The priority queue, remediation agent Jobs, branch push and pull request, rollups and TTL |
| agent-runner             | The in-pod coding-agent runtime (`claude -p` or `codex exec`)                             |
| egress-broker            | The proxy every agent model request goes through; it holds the model credential           |
| status-server            | The [status page](../operate/status-page.md): findings, metrics and human actions         |
| `patchy`                 | The [workstation CLI](../operate/cli.md): the same view and actions from a terminal       |
| evaluation-controller    | Optional: runs evolve's [remote evaluations](evaluation.md) on the same sandbox           |

Patchy uses two namespaces, and the split between them is the security boundary. The controllers, the broker and the
status server run in `patchy`, one replica each. The short-lived agent Jobs run in `patchy-agents`, which holds no
credential of any kind. Claude and codex model traffic authenticates at the broker instead. See the
[isolation model](isolation.md).

## 1. Findings arrive and accumulate

The **integration-controller** validates each scanner delivery. A delivery can be a GitHub `code_scanning_alert`, an SCC
notification, a Wiz issue, or a generic integration's finding (see [Integrations](../integrations/index.md)). The
controller passes it to the matching [`pkg/source` handler](../extending.md). The handler normalizes the alert into
advisories (GHSA/CVE/CWE), severity, and locations or cloud resources.

The result goes into a `Finding` resource. A first alert creates a new Finding at phase `Opened`. During the
**accumulation window** (one hour by default), later alerts for the same repository and advisory family merge into that
existing Finding. Accumulation is a condition (`AccumulationComplete`), not a phase. So alerts keep merging in while
enhancement runs at the same time. Alerts that arrive after the window closes open a new Finding.

## 2. The tracking issue is a projection

The same controller projects every Finding out as a GitHub tracking issue. The issue holds the body, enrichment and
report comments, and a short [label vocabulary](issue-labels.md) (source, advisories, phase, severity, priority,
recommendation). The projection is one-way: nothing reads issue state back into the pipeline. Human signals do come
back:

- A human closing the issue hands the finding off.
- A `/approve` comment records an approval.
- The PR merge webhook completes the finding.

## 3. Context before code

The **context-controller** watches for `Opened` findings and runs the enhancer chain. It records ownership and
infrastructure context as enrichments on Finding status. The integration-controller projects these as issue comments.
Then the finding moves `Opened → Enhanced`. The chain runs in a fixed order, and each entry is skipped unless it is
turned on:

1. The cloud lookups: [Google Cloud Asset Inventory](../integrations/enhancers/google-cai.md),
   [AWS resource tags](../integrations/enhancers/aws.md) and [Azure resource tags](../integrations/enhancers/azure.md).
   For a cloud finding they also find the owning repository from labels on the resource. Each is turned on by its
   cloud's Integration.
2. The [generic HTTP fan-out](../integrations/enhancers/generic.md): one signed call to each generic Integration that
   has enhancement turned on.
3. The static context file, a YAML map that stands in for a CMDB. The context-controller's `--static-context-file` flag
   turns it on.

The context-controller has no GitHub access. Its only outbound calls are the lookups your Integrations enable. You can
add more enhancers through the [`pkg/enhance`](../extending.md) plugin interface.

## 4. Repositories are pinned once

The **source-controller** runs the `Forge` and `Repository` reconcilers. It validates forge credentials. Then for each
Repository it:

1. finds the `Forge` that covers the Repository
2. pins the head SHA once
3. downloads the tarball archive at that SHA over plain HTTP (no controller uses a git binary)
4. serves the tarball from an in-cluster artifact endpoint (`:9790`)

Agent pods fetch the tarball **without credentials**. The URL carries an id that cannot be guessed, and the Job pins the
sha256 digest. This guarantees that investigation and remediation see the same code.

## 5. The gate, then the investigation

The **investigation-controller** admits findings that meet all three conditions:

- they are `Enhanced`
- their accumulation window is closed
- they are older than `--finding-min-age` (one hour)

On admission it creates the `Repository` and one immutable `Investigation` per attempt. Creating that child resource
acts as the lease. The analysis scheduler then runs agent Jobs in severity order, with a limit on how many run at once.

Inside the pod, **agent-runner** runs the model's harness (`claude -p` or `codex exec`, whichever the model's provider
needs). It works over the pinned tree and a templated finding handoff. The agent writes a report with YAML frontmatter
that patchy parses. The frontmatter holds:

- `exploitability`, `likelihood` and `impact` ratings
- a `recommendation` (`ignore` | `remediate` | `manual`)
- `severity` and `priority`
- a `confidence` value in [0, 1]
- for `remediate` only: the model it wants for the fix (later clamped to an operator allowlist) and its **estimate** of
  the turns and tokens the fix will take

The estimate never makes the run smaller. Every remediation gets at least the operator's auto budget. An estimate
_above_ that budget holds the finding for human approval. Approving it grants the larger budget, up to the manual budget
(see [the remediation budget](../deployment/models.md#the-remediation-budget)). The agent always prefers
backwards-compatible fixes. If a better fix exists that would break compatibility, the report says so and the pipeline
holds the finding in the same way. The runtime never talks to GitHub or the Kubernetes API. Results leave the pod as a
`PATCHY-EVENT:` JSONL stream on stdout.

## 6. Verdicts route

The investigation-controller writes the results onto the Finding. The report is projected onto the tracking issue, and
the ratings feed a 0–100 scheduling priority. The controller then routes on the verdict:

- A false positive (`ignore`) is dismissed and its issue closed.
- `manual` work is handed to the repository owners.
- A confident `remediate` is queued.
- A `remediate` below the confidence threshold, or one that offers a breaking change, waits for human approval.

A failed stage is retried up to `--max-attempts` times, and then the finding fails. Patchy never trusts a partial
report. The full table is in [Verdict routing](lifecycle.md#verdict-routing).

## 7. Remediation in priority order

The **remediation-controller** admits queued findings, including approvals and revivals. It schedules them in priority
order with a limit on how many run at once. Aging stops low-priority findings from waiting forever. It then runs the
second agent stage with the finding markdown, the same pinned tree and the investigation report, under the budget the
spawner granted. The agent produces a summary report plus a `commit.sh`. The script must run cleanly and leave real
commits, and the controller checks this instead of trusting the agent's word.

The remediation-controller is the only component that holds a forge **write** credential. It replays the changeset
through the GitHub Git Data API (blob → tree → commit → ref) onto a `patchy/<finding>` branch. It then opens the pull
request and moves the finding to `InReview`. If the agent reports that the finding cannot be fixed safely, the finding
is handed off instead.

## 8. Humans close the loop

Merging the PR fires a `pull_request` webhook, and the integration-controller moves the finding to `Remediated`. Other
outcomes:

- Closing the PR without merging, or running out of retries at any stage above, moves the finding to `Failed`. A human
  **retry** returns it to the stage that failed.
- `manual` verdicts and issues closed by a human move the finding to `HandedOff`. A later approval can put it back in
  the queue.

People act through the `/approve` comment, the [status page](../operate/status-page.md) or the
[patchy CLI](../operate/cli.md). All three write the same spec fields (see [Human actions](lifecycle.md#human-actions)).
Patchy keeps completed findings for a TTL (14 days by default) and then deletes them. Per-scope `FindingRollup`
resources keep the all-time statistics: success rates, verdict mix, and token and cost totals per repository, harness
and model.

Watch it all with the shared kubectl category:

```sh
kubectl get patchy -n patchy          # everything patchy owns
kubectl get findings -w               # the pipeline, live
kubectl get investigations,remediations,findingrollups
```

The complete phase table, with the component that writes each transition, is in [Finding lifecycle](lifecycle.md).
