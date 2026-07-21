# Patchy

An end-to-end workflow for triaging and remediating security findings from multiple sources, using Kubernetes custom
resources as the state machine and GitHub issues as a human-facing projection.

## Key requirements

1. The initial solution ingests finding reports from GitHub Advanced Security (namely CodeQL findings), but the
   solution is tool-agnostic through a plugin architecture:
   - provider webhooks deliver code-scanning alerts to a single internet-facing receiver;
   - each alert is retrieved in full and folded into a `Finding` custom resource carrying all relevant context
     (advisories, rule, severity, locations);
   - every Finding is projected to a GitHub tracking issue labelled with its source, its CVE/CWE/GHSA advisory
     identifiers, and its current phase — for humans and issue searches, never parsed back into state.
2. Multiple alerts of the same finding type (CVE/CWE/GHSA) against the same repository accumulate into a single
   Finding for up to 1 hour.
3. Once the accumulation window closes and at least an hour has passed, the pipeline picks the finding up for
   automated analysis.
4. A context-enhancement stage runs over freshly opened findings:
   - it may pull information from a CMDB to gather ownership and infrastructure relationships, recorded as
     enrichments on the Finding and projected as issue comments;
   - the enhancement logic is a placeholder behind the `pkg/enhance` plugin seam, but the machinery — pick up a
     finding, enrich it, advance its phase — is real.
5. An investigation agent analyses each eligible finding:
   - the agent runtime downloads the finding contents into a consistent, templated markdown file;
   - the repository is provided as a SHA-pinned tree — investigation and remediation are guaranteed the same code;
   - `claude -p` is bootstrapped with a prompt to assess the finding; the agent has no internet access and no
     GitHub/API credentials of any kind;
   - the agent writes a report with parseable YAML frontmatter: `exploitability`, `likelihood`, and `impact`
     ratings (each `none|low|medium|high|critical` with a justification), a `recommendation`
     (`ignore` for false positives, `remediate` for agentic remediation, `manual` for human remediation),
     `priority`, `severity`, and a `confidence` value in [0, 1] — for `remediate`, the likelihood of full
     remediation without breaking functionality;
   - backwards-compatible fixes are always favoured; if a better-but-breaking solution exists the report says so
     and the pipeline holds for a human `/approve`;
   - a `remediate` verdict also suggests the model, token budget, and max turns for the remediation stage, which
     the controller clamps to operator-configured ceilings and an allowlist.
6. The investigation-controller routes each verdict:
   - the report is always projected onto the tracking issue, and the ratings feed a 0–100 scheduling priority
     (severity 30% / exploitability 30% / likelihood 20% / impact 20%, tunable);
   - `ignore` dismisses the GHAS alerts and closes the issue;
   - `manual` hands the finding to the repository owners for triage;
   - `remediate` below the confidence threshold (default 0.75) — or holding a breaking-change note — waits for a
     human `/approve` comment;
   - `remediate` at or above the threshold queues the finding for remediation.
7. Remediation runs in priority order under bounded concurrency: a second `claude -p` stage receives the finding
   markdown, the pinned repository tree, and the investigation report, under the clamped token budget and turn
   ceiling. It produces a summary report (frontmatter: `success`, `confidence`) and a `commit.sh` that commits the
   changeset.
8. The remediation-controller verifies the claim against the repository (commit.sh must run cleanly and leave real
   commits), replays the changeset through the GitHub API onto a `patchy/<finding>` branch, opens the pull request,
   and moves the finding to in-review. Merging is left to humans; the merge webhook completes the finding.
9. Completed findings are kept for a TTL (default 14 days) and then deleted; per-scope `FindingRollup` resources
   retain the all-time statistics (success rates, verdict mix, token and cost totals per repository, harness, and
   model) with exactly-once accounting.

## Architecture

Go, one module, separate binaries per concern (not monolithic), all hosted in Kubernetes. OpenTelemetry for
logging, tracing, and metrics; structured logging via `log/slog`.

**The source of truth is the Kubernetes API.** The `patchy.bitwisemedia.uk/v1alpha1` custom resources —
`Integration`, `Forge`, `Finding`, `Repository`, `Investigation`, `Remediation`, `FindingRollup` — carry all
pipeline state; etcd is the only state store. GitHub issues are a one-way, human-facing projection: labels and
comments are rendered from the Finding, and human actions (issue close, `/approve` comments, PR merge) flow back
in as webhook signals, never by re-parsing issue state.

The agent execution harness is adapted from evolve (harness builds argv, runner executes, harness parses stdout).
Claude is the first harness; others may follow, and the investigation harness is configurable independently of the
remediation harness. The finding handoff, both prompts, and both report contracts are templated and consistent
across the estate.

### Components

- **integration-controller** — the single internet-facing entry point, driven by `Integration` resources. Inbound:
  validates provider webhooks (per-Integration HMAC secrets) and ingests scanner alerts into Findings through the
  `pkg/source` handler seam (accumulation, duplicate merge). Outbound: projects Findings to tracking issues
  (body, labels, enrichment and report comments) and applies human signals (close, `/approve`, PR merge/close)
  back onto Findings.
- **source-controller** — `Forge` + `Repository` reconcilers. Validates forge credentials, resolves
  each Repository to its covering Forge, pins the head SHA exactly once, downloads the forge's tarball archive at
  that SHA (pure HTTP; controllers carry no git binary), and serves it from an artifact endpoint agents fetch
  credential-lessly (unguessable URL, digest-verified).
- **context-controller** — the enhancer chain over freshly opened Findings (`pkg/enhance` plugins; CMDB
  placeholder). Writes only Finding status; holds no GitHub credential.
- **investigation-controller** — the gate (admits accumulated, aged findings; materializes the Repository and one
  immutable `Investigation` per attempt) and the analysis scheduler (bounded concurrency, severity order,
  verdict routing).
- **remediation-controller** — queue admission (approvals, revivals), the priority scheduler (bounded
  concurrency, aging against starvation), agent Job execution, changeset push + pull request (the only holder of
  a forge write credential), and the rollup/TTL loop.
- **agent-runner** — the in-pod coding-agent runtime: one stage per Job (`investigate` or `remediate`), reports
  as `PATCHY-EVENT:` JSONL on stdout. Never talks to GitHub or the Kubernetes API; has no credentials beyond the
  model key.

### The state machine

`Finding.status.phase`: `Opened → Enhanced → Investigating → Queued → Remediating → InReview → Remediated`, with
`AwaitingApproval` before `Queued` when a human must approve, and `Dismissed` / `HandedOff` / `Failed` as the
other terminal phases (`HandedOff` is revivable by a later approval). Accumulation is a condition, not a phase —
alerts fold in concurrently with enhancement. Each edge has exactly one writer component; the transition table
lives in `api/v1alpha1/transitions.go` and is enforced by `SetPhase`.

### Isolation model

The agent pod is the least-trusted component and holds **no forge credential at all** — not even in the init
container. The repository arrives as a digest-verified tarball from source-controller over the cluster network;
the model API key is the only secret in the pod; NetworkPolicies (plus optional Cilium FQDN / Istio allowlists)
restrict egress to the artifact server and the model API. All GitHub side effects — issue projection, alert
dismissal, branch push, pull requests — happen controller-side with short-lived, per-repository scoped tokens.

## Projected labels

The tracking-issue projection stamps a trimmed, human-facing label vocabulary (rendered from the Finding; the CR
is the state):

```txt
security-source: "ghas"                          # the source handler that ingested the finding
security-advisory: <CWE#,CVE#,GHSA#>             # one label per advisory identifier
security-finding: "opened|enhanced|investigating|queued|remediating|in-review|remediated|awaiting-approval|dismissed|handed-off|failed"
security-severity: "low|medium|high|critical"
security-priority: "low|medium|high|critical"
security-recommendation: "remediate|ignore|manual"
```

Machine metadata that used to ride on labels — alert numbers, accumulation state, confidence, budgets, attempt
counts, per-stage token/cost accounting — lives on the custom resources (`kubectl get findings`,
`kubectl get investigations`, `kubectl get findingrollups`).
