# Demo tooling

Three pieces exist purely to show patchy off — end to end, repeatably, and without real credentials: the **dev-fake
overlay** (the whole pipeline against in-cluster fakes), the status page's **replay/reset demo loop**, and **canned
status-page data**. None of them ship in a production install; the first and last belong to the
[dev overlays](kustomize.md#the-overlays), and the demo loop hides behind RBAC verbs granted sparingly.

## Credential-less end to end: the dev-fake overlay

To watch **every custom resource** progress — `Finding` through `Repository`, `Investigation`, `Remediation`, the pull
request, `FindingRollup`, and the TTL sweep — without a GitHub token _or_ a model key, the `dev-fake` overlay replaces
both external dependencies:

- **GitHub** becomes the e2e suite's in-memory API, run **in the cluster** as part of the overlay (`patchy-fakegithub`,
  built from `e2e/fakegithub` by `dev-colima`). It serves everything the controllers call: alert detail, the issue
  projection, repository tarballs, the Git Data push, and pull requests. The overlay points the `Integration`/`Forge`
  CRs at its Service DNS name and adds the one egress NetworkPolicy rule k3s needs; a NodePort (30990) exposes the same
  API to your host for inspection.
- **The model** becomes a scripted agent image (`hack/fake-agent`): a shell script named `agent-runner` that prints the
  same two stdout streams the real runner does — a paced `PATCHY-TURN` conversation followed by the stage's
  `PATCHY-EVENT` result. The Jobs are real — init container, artifact fetch, digest check — only the verdict is scripted
  (remediate at 0.92 confidence, so findings flow straight through the queue). Because the turns are real turns, each
  run's conversation is captured into its transcript `ConfigMap` and streams live onto the status page while the Job is
  still running, and both stages carry a full report.

The walkthrough below assumes the [Colima setup](colima.md); on kind, swap `dev-colima` for the equivalent build/load
steps.

```sh
PATCHY_OVERLAY=dev-fake make dev-colima    # builds the fake images too

mise run replay -- -dev-secret \
  fixtures/webhooks/code_scanning_alert.created.json

kubectl get patchy -n patchy -w            # every patchy kind, live
```

Within a couple of minutes (2-minute accumulation window + minimum age) the Finding walks
`Opened → Enhanced → Investigating → Queued → Remediating → InReview`: the `Repository` pins the fake's head SHA and
serves its artifact, the `Investigation` and `Remediation` children appear with their Jobs, and the push + PR land in
the fake — visible on its API:

```sh
curl -s http://localhost:30990/api/v3/repos/acme/shop/issues | jq '.[].title'   # the projected issue
curl -s http://localhost:30990/api/v3/repos/acme/shop/pulls  | jq '.[].number'  # the remediation PR
```

(30990 is the fake's Service NodePort, forwarded to `localhost` by colima like the webhook's 30079.)

Close the loop by "merging" the PR — the merge signal resolves the Finding by its branch name, so substitute the real
finding name into the recorded fixture:

```sh
name=$(kubectl -n patchy get findings -o jsonpath='{.items[0].metadata.name}')
sed "s/patchy\/finding-cccccccccc-1/patchy\/$name/" \
  e2e/fixtures/webhooks/pull_request.merged.json > /tmp/merged.json
mise run replay -- -dev-secret /tmp/merged.json
```

Each stage's conversation and report are on the status page under the finding — open the run while its Job is still
alive and the turns stream in as the script emits them (it paces itself for exactly that reason). The same turns are
persisted, so they stay readable after the Job's TTL takes the pod:

```sh
kubectl -n patchy get cm -l patchy.bitwisemedia.uk/finding="$name"    # one transcript per run
```

The Finding goes `Remediated`, `kubectl get findingrollups -n patchy -o yaml` shows the counts and (scripted) token/cost
totals ticking, and after the dev TTL (30 minutes) the Finding and its children are deleted while the rollups remain.
The fake's state is in-memory — `kubectl -n patchy rollout restart deployment patchy-fakegithub` for a clean slate
(every `dev-colima` re-run restarts it too; live Findings notice their tracking issue vanished and re-project a fresh
one) — and other routes (`ignore`, `manual`, an `await_approval` hold) are one edit away in
`hack/fake-agent/agent-runner`.

## The demo loop: replay and reset

The [status page's](../status-ui.md#the-user-menu) user menu holds two namespace-wide actions, each behind its own
custom RBAC verb and hidden without the grant:

| Action                | Verb     | What happens                                                                                                                                                                                                                        |
| --------------------- | -------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Replay deliveries** | `replay` | Writes `spec.replay` on the active Integrations; the integration-controller then redelivers the App webhook's delivery log — successes included — through the [redelivery sweep](webhook.md#missed-deliveries-the-redelivery-sweep) |
| **Reset all data**    | `reset`  | Deletes every pipeline resource in the namespace — findings, investigation/remediation runs, pinned repositories, rollups. Configuration (Integrations, Forges) survives. Two clicks to confirm                                     |

Reset then Replay re-runs the entire pipeline from ingestion — the demo loop. Grant these verbs sparingly; the
`patchy-findings-admin` example tier in `rbac.users.example.yaml` bundles them with the operator verbs.

## Canned status-page data

The dev overlay ships representative canned data so the status page is worth looking at without running the pipeline:
`deploy/kustomize/overlays/dev/findings-demo.yaml` carries one `Finding` per lifecycle phase (the
[screenshots](../status-ui.md#views)) plus `FindingRollup` statistics for every scope. `kubectl apply -k` creates the
objects but cannot write their status subresource, so seed the phases and rollup buckets with:

```sh
mise run status-demo
```

The data is deliberately inert — every finding is **suspended** (the enhancer, gate, and spawner all skip suspended
findings), none carries a `trackingRef` (so nothing is projected to GitHub), and terminal phases carry no `completedAt`
(so the rollup/TTL loop neither aggregates nor reaps them). Actions still work against it: resume one of the canned
findings and the controllers will pick it up for real, which is a fine way to watch the pipeline run.
