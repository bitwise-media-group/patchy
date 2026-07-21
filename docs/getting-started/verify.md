# Verify the pipeline

With the chart installed, the `Integration`/`Forge` resources applied, and the webhook reachable, follow one finding all
the way through. The [state machine](../labels.md) is the map; this page is the guided walk.

## 1. Check the controllers

```sh
kubectl -n patchy get pods
kubectl -n patchy get integrations,forges     # both should show Ready
kubectl -n patchy logs deploy/patchy-integration-controller
```

The five controller pods should be `Running` and logging to stderr; each serves `GET /healthz` and `GET /readyz` on
port 8081. `Ready: False` on the Integration or Forge means the referenced `patchy-github` Secret is missing or its
credential failed validation — fix that before anything else. In the GitHub App's **Advanced → Recent Deliveries**, the
`ping` delivery should show `204` — a `401` means the webhook secret in the App and in `patchy-github` disagree.

## 2. Produce a finding

Push a change that CodeQL flags (or re-run an existing code-scanning analysis) on a watched repository. When the
`code_scanning_alert` delivery lands, the integration-controller creates a `Finding`:

```sh
kubectl -n patchy get findings -w
```

```text
NAME              REPO                SEVERITY   PRIORITY   PHASE    VERDICT   AGE
ghas-cwe-89-1a2b  acme/payments-api   high                  Opened             8s
```

The Finding is projected to a GitHub tracking issue carrying the trimmed label set (`security-source: ghas`,
`security-advisory: CWE-89`, `security-finding: opened`, `security-severity: high`). Further alerts of the same advisory
family against the same repository fold into the same Finding for the accumulation window — one hour by default —
visible as the alert list in `kubectl describe finding`.

## 3. Watch the phases move

- Within moments the context-controller records enrichments and moves `Opened → Enhanced`; the enrichment appears as a
  comment on the tracking issue.
- Once the accumulation window has closed and the finding is older than `--finding-min-age` (one hour), the
  investigation-controller admits it: a `Repository` is pinned, an `Investigation` child is created, and the phase moves
  to `Investigating` with an agent Job in the sandbox namespace:

```sh
kubectl get patchy -n patchy                      # every patchy kind at once
kubectl -n patchy get repositories,investigations
kubectl -n patchy-agents get jobs
kubectl -n patchy-agents logs job/<job-name> -f   # the PATCHY-EVENT: stream
```

## 4. The verdict

When the analysis lands, the Finding's `VERDICT` column fills in, the report is posted to the tracking issue, and one of
four things happens:

| Verdict                          | What you'll see                                                                       |
| -------------------------------- | ------------------------------------------------------------------------------------- |
| False positive (`ignore`)        | GHAS alerts dismissed as _false positive_, issue closed, phase `Dismissed`            |
| Human-only (`manual`)            | Phase `HandedOff` — the owners take it from the issue; `/approve` can still revive it |
| Low confidence / breaking change | Phase `AwaitingApproval` — comment `/approve` on the issue to release the attempt     |
| High confidence (`remediate`)    | Phase `Queued`, then `Remediating` when the priority scheduler grants a slot          |

## 5. The pull request

A successful remediation pushes branch `patchy/<finding>`, opens a pull request, and moves the finding to `InReview`
(`status.pullRequest` carries the URL — also a printed column: `kubectl get findings -o wide`). Review and merge it like
any other PR — merging moves the finding to `Remediated` and closes the issue. Closing the PR unmerged lands it at
`Failed`.

Terminal findings hang around for the TTL (14 days by default) and are then deleted; the all-time statistics survive in
the rollups:

```sh
kubectl -n patchy get findingrollups
```

## Local development loop

No cluster webhook plumbing is needed to exercise the pipeline locally:

```sh
make e2e          # envtest + real binaries, recorded webhook fixtures, in-memory GitHub
mise run replay -- -secret-file dev.secret e2e/fixtures/webhooks/code_scanning_alert.created.json
```

`replay` signs a recorded fixture with your webhook secret and delivers it to a running integration-controller (default
URL `http://localhost:30079/github/webhooks`, the dev overlay's NodePort) — the local stand-in for GitHub. The
[dev overlay](../deployment/kustomize.md) pairs with this: throwaway secrets, 2-minute windows, and the fake harness so
no tokens are spent; on [Colima](../deployment/colima.md) the whole loop is `make dev-colima`.

## Troubleshooting

<div class="nowrap-first" markdown>

| Symptom                                    | Likely cause                                                                                         |
| ------------------------------------------ | ---------------------------------------------------------------------------------------------------- |
| Webhook deliveries show `401`              | Webhook secret mismatch between the App and `patchy-github`, or no Integration exists                |
| Agent pods `CreateContainerConfigError`    | `patchy-anthropic` secret missing in `patchy-agents`                                                 |
| Findings stall at `Enhanced`               | Accumulation window or `--finding-min-age` not elapsed — check `kubectl describe finding` timestamps |
| `ForgeResolved: False`                     | No Forge matches the repository (`NoForgeMatch`), or two match equally (`Ambiguous`)                 |
| Issue projection / artifacts erroring      | The `patchy-github` credential is a placeholder or lacks a permission                                |
| Findings bounce `Investigating → Enhanced` | Job launch or stage failed; retried up to `--max-attempts` (2), then `Failed`                        |
| Egress "works" on kind                     | kindnet ignores NetworkPolicy — a green dev apply is not a working sandbox                           |

</div>
