# Verify the pipeline

With the chart installed, the App configured, and the webhooks reachable, follow one finding all the way through. The
[label reference](../labels.md) is the map; this page is the guided walk.

## 1. Check the controllers

```sh
kubectl -n patchy get pods
kubectl -n patchy logs deploy/patchy-source-controller
```

All three pods should be `Running` and logging to stderr. Each serves `GET /healthz` (liveness) and `GET /readyz`
(readiness) on port 8080. In the GitHub App's **Advanced → Recent Deliveries**, the `ping` delivery should show `204` —
a `401` means the webhook secret in the App and in `patchy-webhook-secret` disagree.

## 2. Produce a finding

Push a change that CodeQL flags (or re-run an existing code-scanning analysis) on a watched repository. When the
`code_scanning_alert` delivery lands, the source-controller opens an issue carrying the full label set:

```text
security-source: ghas
security-advisory: CWE-89
security-alert: 42
security-finding: opened
security-accumulation: open
```

Further alerts of the same advisory type against the same repository fold into the same issue (one extra
`security-alert: <n>` label each) for the accumulation window — one hour by default.

## 3. Watch the labels move

```sh
gh issue view <n> --json labels --jq '.labels[].name'
```

- Within ~2 minutes the context-controller comments with ownership context and flips
  `security-finding: opened → context-enhanced`.
- After the window closes, the source-controller flips `security-accumulation: open → complete`.
- Once the issue is an hour old (`--issue-min-age`), the remediation-controller takes the lease
  (`security-finding: classifying`) and launches an agent Job:

```sh
kubectl -n patchy-agents get jobs
kubectl -n patchy-agents logs job/<job-name> -f   # the PATCHY-EVENT: stream
```

## 4. The classified route

When classification lands, the issue gains the verdict labels — severity, priority, recommendation, confidence, and the
token usage — and one of four things happens:

| Verdict                          | What you'll see                                                                           |
| -------------------------------- | ----------------------------------------------------------------------------------------- |
| False positive (`ignore`)        | GHAS alerts dismissed as _false positive_, issue closed                                   |
| Human-only (`manual`)            | Issue assigned to the repository owners                                                   |
| Low confidence / breaking change | Assigned to owners with `/approve` instructions — comment `/approve` to force the attempt |
| High confidence (`remediate`)    | The same pod continues into remediation                                                   |

## 5. The pull request

A successful remediation pushes branch `patchy/issue-<n>`, opens a pull request, and moves the issue to
`security-finding: in-review`. Review and merge it like any other PR — merging flips the issue to
`security-finding: remediated` and closes it. Closing the PR unmerged routes the issue to `attempted` and assigns the
owners.

## Local development loop

No cluster webhook plumbing is needed to exercise the controllers locally:

```sh
make e2e          # real binaries, recorded webhook fixtures, in-memory GitHub
mise run replay -- -secret-file dev.secret e2e/fixtures/webhooks/code_scanning_alert.created.json
```

`replay` signs a recorded fixture with your webhook secret and delivers it to a locally running controller — the local
stand-in for GitHub. The dev overlay (`deploy/kustomize/overlays/dev`) pairs with this: NodePort services, throwaway
secrets, 2-minute windows, and the fake harness so no tokens are spent.

## Troubleshooting

<div class="nowrap-first" markdown>

| Symptom                                  | Likely cause                                                                    |
| ---------------------------------------- | ------------------------------------------------------------------------------- |
| Webhook deliveries show `401`            | Webhook secret mismatch between the App and `patchy-webhook-secret`             |
| Agent pods `CreateContainerConfigError`  | `patchy-anthropic` secret missing in `patchy-agents`                            |
| Issues stall at `context-enhanced`       | Accumulation window or `--issue-min-age` not elapsed yet — check the timestamps |
| Issues bounce back to `context-enhanced` | Job launch failed; the controller retries up to `--max-attempts` (2)            |
| Egress "works" on kind                   | kindnet ignores NetworkPolicy — a green dev apply is not a working sandbox      |

</div>
