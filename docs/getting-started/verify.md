# Verify the pipeline

Once you have installed the chart, applied the `Integration`/`Forge` resources and made the webhook reachable, follow
one finding from start to finish. The [state machine](../concepts/lifecycle.md) is the reference map, and this page
walks you through it.

## 1. Check the controllers

```sh
kubectl -n patchy get pods
kubectl -n patchy get integrations,forges     # both should show Ready
kubectl -n patchy logs deploy/patchy-integration-controller
```

The pods should be `Running` and logging to stderr. You should see the five pipeline controllers (integration, source,
context, investigation, remediation), the egress-broker and the status-server. Each serves `GET /healthz` and
`GET /readyz` on port 8081.

`Ready: False` on the Integration or Forge means the referenced `patchy-github` Secret is missing or its credential
failed validation. Fix that before anything else. In the GitHub App's **Advanced → Recent Deliveries**, the `ping`
delivery should show `204`. A `401` means the webhook secret in the App does not match the one in `patchy-github`.

## 2. Produce a finding

Push a change that CodeQL flags to a watched repository, or re-run an existing code-scanning analysis. When the
`code_scanning_alert` delivery arrives, the integration-controller creates a `Finding`:

```sh
kubectl -n patchy get findings -w
```

```text
NAME                  REPO                SEVERITY   PRIORITY   PHASE    VERDICT   AGE
finding-7c41d09e2a-1  acme/payments-api   high                  Opened             8s
```

The Finding is projected to a GitHub tracking issue with the short label set (`security-source: ghas`,
`security-advisory: CWE-89`, `security-finding: opened`, `security-severity: high`). During the accumulation window (one
hour by default), more alerts of the same advisory family on the same repository merge into the same Finding. You can
see them in the alert list in `kubectl describe finding`.

## 3. Watch the phases move

- Within moments the context-controller records enrichments and moves the finding `Opened → Enhanced`. The enrichment
  appears as a comment on the tracking issue.
- When the accumulation window has closed and the finding is older than `--finding-min-age` (one hour), the
  investigation-controller admits it. It pins a `Repository` and creates an `Investigation` child. The phase moves to
  `Investigating`, with an agent Job in the sandbox namespace:

```sh
kubectl get patchy -n patchy                      # every patchy kind at once
kubectl -n patchy get repositories,investigations
kubectl -n patchy-agents get jobs
kubectl -n patchy-agents logs job/<job-name> -f   # the PATCHY-EVENT: stream
```

## 4. The verdict

When the analysis finishes, the Finding's `VERDICT` column fills in and the report is posted to the tracking issue. Then
one of four things happens:

| Verdict                          | What you'll see                                                                      |
| -------------------------------- | ------------------------------------------------------------------------------------ |
| False positive (`ignore`)        | GHAS alerts dismissed as _false positive_, issue closed, phase `Dismissed`           |
| Human-only (`manual`)            | Phase `HandedOff`. The owners take it from the issue; `/approve` can still revive it |
| Low confidence / breaking change | Phase `AwaitingApproval`. Comment `/approve` on the issue to release the attempt     |
| High confidence (`remediate`)    | Phase `Queued`, then `Remediating` when the priority scheduler grants a slot         |

## 5. The pull request

A successful remediation pushes branch `patchy/<finding>`, opens a pull request, and moves the finding to `InReview`.
`status.pullRequest` holds the URL, which is also a printed column in `kubectl get findings -o wide`. Review and merge
it like any other PR. Merging moves the finding to `Remediated` and closes the issue. Closing the PR without merging
moves the finding to `Failed`.

Patchy keeps terminal findings for the TTL (14 days by default) and then deletes them. The all-time statistics stay in
the rollups:

```sh
kubectl -n patchy get findingrollups
```

## Local development loop

You can exercise the pipeline locally without any cluster webhook setup:

```sh
make e2e          # envtest + real binaries, recorded webhook fixtures, in-memory GitHub
mise run replay -- -secret-file "$PWD/dev.secret" fixtures/webhooks/code_scanning_alert.created.json
```

`replay` stands in for GitHub on your machine. It signs a recorded fixture with your webhook secret and delivers it to a
running integration-controller. The default URL is `http://localhost:30079/github/webhooks`, the dev overlay's NodePort.
The [dev overlay](../deployment/kustomize.md) goes with this. It has throwaway secrets, 2-minute windows, and the fake
harness so no tokens are spent. On [Colima](../deployment/colima.md), `make dev-colima` runs the full loop.

## Troubleshooting

| Symptom                                         | Likely cause                                                                                                                                            |
| ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Webhook deliveries show `401`                   | Webhook secret mismatch between the App and `patchy-github`, or no Integration exists                                                                   |
| Claude or codex runs failing with broker errors | The egress broker is not ready. Check its `/readyz` (missing `patchy-anthropic` / `patchy-openai` in the release namespace, or unusable cloud identity) |
| Findings stall at `Enhanced`                    | Accumulation window or `--finding-min-age` not elapsed. Check the timestamps in `kubectl describe finding`                                              |
| `ForgeResolved: False`                          | No Forge matches the repository (`NoForgeMatch`), or two match equally (`Ambiguous`)                                                                    |
| Issue projection / artifacts erroring           | The `patchy-github` credential is a placeholder or lacks a permission                                                                                   |
| Findings bounce `Investigating → Enhanced`      | Job launch or stage failed. It is retried up to `--max-attempts` (2), then moves to `Failed`                                                            |
| Egress "works" on kind                          | kindnet ignores NetworkPolicy, so a successful dev apply does not prove the sandbox works                                                               |
