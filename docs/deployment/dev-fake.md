# Demo mode

Three pieces exist only to demonstrate patchy. They run end to end, repeat the same way each time, and need no real
credentials:

- the **dev-fake overlay**, which runs every stage of the pipeline against in-cluster fakes
- the status page's **replay/reset demo loop**
- **canned status-page data**

None of them ship in a production install. The first and last belong to the [dev overlays](kustomize.md#the-overlays).
The demo loop sits behind RBAC verbs that you should grant to few people.

## Credential-less end to end: the dev-fake overlay

The `dev-fake` overlay lets you watch **every custom resource** progress with no GitHub token _and_ no model key. That
covers `Finding`, then `Repository`, `Investigation`, `Remediation`, the pull request, `FindingRollup` and the TTL
sweep. The overlay replaces both external dependencies:

- **GitHub** becomes the e2e suite's in-memory API. It runs **in the cluster** as part of the overlay
  (`patchy-fakegithub`, built from `e2e/fakegithub` by `dev-colima`). It serves everything the controllers call: alert
  detail, the issue projection, repository tarballs, the Git Data push, and pull requests. The overlay points the
  `Integration`/`Forge` CRs at its Service DNS name and adds the one egress NetworkPolicy rule k3s needs. A NodePort
  (30990) exposes the same API to your host so you can inspect it.
- **The model** becomes a scripted agent image (`hack/fake-agent`). This is a shell script named `agent-runner` that
  prints the same two stdout streams as the real runner: a paced `PATCHY-TURN` conversation, then the stage's
  `PATCHY-EVENT` result. The Jobs are real, with the init container, artifact fetch and digest check. Only the verdict
  is scripted (remediate at 0.92 confidence, so findings go straight through the queue). The turns are real turns, so
  each run's conversation is saved to its transcript `ConfigMap`. It also streams live onto the status page while the
  Job runs, and both stages carry a full report.

The walkthrough below assumes the [Colima setup](colima.md). On kind, replace `dev-colima` with the equivalent build and
load steps.

```sh
PATCHY_OVERLAY=dev-fake make dev-colima    # builds the fake images too

mise run replay -- -dev-secret \
  fixtures/webhooks/code_scanning_alert.created.json

kubectl get patchy -n patchy -w            # every patchy kind, live
```

Within a couple of minutes (the 2-minute accumulation window plus the minimum age), the Finding moves through
`Opened → Enhanced → Investigating → Queued → Remediating → InReview`. Along the way:

- The `Repository` pins the fake's head SHA and serves its artifact.
- The `Investigation` and `Remediation` children appear with their Jobs.
- The push and PR land in the fake, and you can see them on its API:

```sh
curl -s http://localhost:30990/api/v3/repos/acme/shop/issues | jq '.[].title'   # the projected issue
curl -s http://localhost:30990/api/v3/repos/acme/shop/pulls  | jq '.[].number'  # the remediation PR
```

(30990 is the fake's Service NodePort. Colima forwards it to `localhost`, as it does the webhook's 30079.)

To close the loop, "merge" the PR. The merge signal finds the Finding by its branch name, so put the real finding name
into the recorded fixture:

```sh
name=$(kubectl -n patchy get findings -o jsonpath='{.items[0].metadata.name}')
sed "s/patchy\/finding-cccccccccc-1/patchy\/$name/" \
  e2e/fixtures/webhooks/pull_request.merged.json > /tmp/merged.json
mise run replay -- -dev-secret /tmp/merged.json
```

Each stage's conversation and report appear on the status page under the finding. Open the run while its Job is still
alive and the turns stream in as the script prints them (the script paces itself for this reason). The turns are also
saved, so you can still read them after the Job's TTL removes the pod:

```sh
kubectl -n patchy get cm -l patchy.bitwisemedia.uk/finding="$name"    # one transcript per run
```

The Finding moves to `Remediated`. `kubectl get findingrollups -n patchy -o yaml` shows the counts and the (scripted)
token and cost totals going up. After the dev TTL (30 minutes), patchy deletes the Finding and its children, and the
rollups remain.

The fake keeps its state in memory. For a clean slate, run
`kubectl -n patchy rollout restart deployment patchy-fakegithub`. Every `dev-colima` re-run restarts it too. Live
Findings notice that their tracking issue is gone and project a fresh one. To try other routes (`ignore`, `manual`, an
`await_approval` hold), make one edit in `hack/fake-agent/agent-runner`.

## The demo loop: replay and reset

The [status page's](../operate/status-page.md#the-user-menu) user menu holds two actions that act on every resource in
the namespace. Each one needs its own custom RBAC verb, and the menu hides it if you lack the grant:

| Action                | Verb     | What happens                                                                                                                                                                                                                                                                     |
| --------------------- | -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Replay deliveries** | `replay` | Writes `spec.replay` on the active Integrations. The integration-controller then redelivers every delivery in the App webhook's delivery log, including successful ones, through the [redelivery sweep](../integrations/sources/github.md#replaying-every-delivery)              |
| **Reset all data**    | `reset`  | Writes `spec.reset`. The integration-controller then deletes every pipeline resource in the namespace (findings, runs, pinned repositories, rollups) and clears the receiver's delivery dedup window. Configuration (Integrations, Forges) survives. You confirm with two clicks |

!!! danger "Reset changes GitHub too"

    Reset does more than wipe the cluster. Against a real GitHub it **permanently deletes** every finding's tracking
    issue (the credential needs admin-level repository permission for this). It also **reopens** the code-scanning
    alerts that patchy dismissed. Use it on demo repositories only.

Reset followed by Replay runs the pipeline again from ingestion. This is the demo loop. Grant these verbs to few people.
The `patchy-findings-admin` example tier in `rbac.users.example.yaml` bundles them with the operator verbs.

## Canned status-page data

The dev overlay ships sample data so the status page has something to show without running the pipeline.
`deploy/kustomize/overlays/dev/findings-demo.yaml` holds one `Finding` per lifecycle phase (the
[screenshots](../operate/status-page.md#views)) plus `FindingRollup` statistics for every scope. `kubectl apply -k`
creates the objects but cannot write their status subresource. Seed the phases and rollup buckets with:

```sh
mise run status-demo
```

The data is inert on purpose:

- Every finding is **suspended**. The enhancer, gate and spawner all skip suspended findings.
- No finding has a `trackingRef`, so nothing is projected to GitHub.
- Terminal phases have no `completedAt`, so the rollup/TTL loop neither counts nor deletes them.

Actions still work on this data. Resume one of the canned findings and the controllers pick it up for real. This is a
good way to watch the pipeline run.
