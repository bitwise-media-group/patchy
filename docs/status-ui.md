# The status page

Patchy's human surface beyond the tracking issues: a web dashboard served by the
[status-server](configuration/status-server.md) showing every `Finding` in the namespace, the two human gates
(`AwaitingApproval` holds and `HandedOff` revival), and the all-time `FindingRollup` statistics. The page is a single
embedded artifact — one self-contained HTML file compiled into the binary — with live updates: the server watches the
Finding and FindingRollup resources and nudges open browsers over Server-Sent Events to refetch.

The same findings, the same actions and the same RBAC are available from a terminal via the [patchy CLI](cli.md) — it
reads and writes the identical custom resources, so the two surfaces never disagree about state.

## Views

The screenshots below show the [canned dev data](#canned-data-in-dev) — every finding deliberately suspended, hence the
`suspended` pill on each row.

- **Findings** — stat tiles, the eleven-phase lifecycle rail with live counts (click a phase to filter),
  severity/verdict/repository/text filters, and the board: severity, phase (with a live-dot while an agent run is active
  and a `suspended` pill), confidence, verdict, and age per finding.

  ![The findings board](assets/images/status-findings.jpg)

- **Finding detail** — the advisory header with status and severity, tabs (Overview · Alerts · Timeline · Investigation
  · Remediation), the metadata sidebar (owners, repository, source, tracking issue, advisories, dates), and the action
  bar. A completed remediation shows the merged PR; terminal states surface failure reasons, dismissal verdicts, and
  hand-off routes. The investigation and remediation tabs each end in a **Conversation** section — see
  [Agent conversations](#agent-conversations).

  ![A finding awaiting approval, with the action bar](assets/images/status-finding-detail.jpg)

- **Rollups** — all-time statistics by total, repository, harness, and model scope: terminal-phase and verdict mixes,
  per-stage token/cost/duration aggregates, and the monthly trend. **This view is public** — it renders without signing
  in.

  ![The rollup statistics](assets/images/status-rollups.jpg)

## Agent conversations

Each run's investigation and remediation tab carries the agent's turn-by-turn conversation: its messages, its reasoning,
every tool it called and what came back. While a run is in flight the section streams live; afterwards it replays the
stored record.

What makes that possible is that the agent pod normalises its own CLI's output — claude's content blocks, codex's items,
copilot's messages all become the same turn vocabulary — and emits it on stdout under a `PATCHY-TURN:` prefix, alongside
the `PATCHY-EVENT:` stage result. The owning controller reads that log once when the Job finishes and writes the
conversation to a ConfigMap owner-referenced to the run. Since a run is owned by its Finding, the
[finding TTL](configuration/remediation-controller.md) cascade deletes the transcript with everything else: a
conversation is retained exactly as long as the finding it explains, and no longer.

Two limits are worth knowing:

- **Turns are bounded and redacted.** Each turn's text is capped (default 2 KiB), a run is capped at 500 turns and 512
  KiB total, and any value matching a credential in the pod's environment is replaced with `«redacted»` before it leaves
  the pod. A conversation cut short by those bounds says so, in the section footer and on the last turn. Override with
  `PATCHY_TRANSCRIPT_MAX_TURN_BYTES`, `PATCHY_TRANSCRIPT_MAX_TURNS`, `PATCHY_TRANSCRIPT_MAX_TOTAL_BYTES`.
- **The upstream session id is not a handle.** `stage.sessionID` records what the agent CLI called its session, which is
  useful for correlating against model-provider logs — but it resolves only against a session file in the pod's `$HOME`,
  which is an `emptyDir` that dies with the pod. Nothing can fetch a conversation from the provider after the fact,
  which is why patchy stores it.

Live streaming needs the status server to read pod logs in the agent namespace, through the API server — it never dials
an agent pod, so the agents' default-deny ingress policy is untouched. Turn it off with
`statusServer.liveTranscripts=false` (helm) or by dropping the `patchy-status-server-agent-logs` Role and its binding
(kustomize); completed runs' conversations still serve, since those come from their own ConfigMaps.

## Actions and what they really do

The action bar renders only the verbs the signed-in user is granted _and_ the finding's state machine allows:

| Action       | Available when                    | What the server writes                                          |
| ------------ | --------------------------------- | --------------------------------------------------------------- |
| **Approve**  | `AwaitingApproval` or `HandedOff` | `spec.approval` — the same record the `/approve` comment writes |
| **Retry**    | `Failed`                          | `spec.retry` — a recovery request                               |
| **Expedite** | any phase up to `Queued`          | `spec.expedite` — a standing urgency mark                       |
| **Suspend**  | any non-terminal phase            | `spec.suspend: true`                                            |
| **Resume**   | a suspended finding               | `spec.suspend: false`                                           |

The status server never moves a phase. Approving records the approval; the remediation-controller's spawner then drives
`AwaitingApproval → Queued` (or revives `HandedOff → Queued` when the approval is newer than the finding's completion)
exactly as it does for a `/approve` issue comment — the state machine's one-writer-per-edge rule holds no matter which
surface the human used.

**Retry** recovers a `Failed` finding to the state immediately before the failure: a failed investigation reverts to
`Enhanced` (the gate opens the next attempt), a failed remediation — or a pull request closed without merging — re-
queues to `Queued` (the spawner creates the next attempt). Each edge keeps its single writer: the investigation gate
drives `Failed → Enhanced`, the remediation spawner `Failed → Queued`. A retry is consumed by the recovery itself; if
the finding fails again, another retry is required.

**Expedite** marks the finding urgent for its whole lifetime: the investigation gate skips the accumulation window and
minimum-age wait, and both schedulers rank the finding's runs ahead of all non-expedited work. It does not bypass an
`AwaitingApproval` hold — that remains approve's job.

## The user menu: demo tooling

The signed-in name in the top bar drops a menu holding sign-out and two namespace-wide actions, each behind its own
custom RBAC verb and hidden without the grant:

| Action                | Verb     | What happens                                                                                                                                                                                                                                   |
| --------------------- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Replay deliveries** | `replay` | Writes `spec.replay` on the active Integrations; the integration-controller then redelivers the App webhook's delivery log — successes included — through the [redelivery sweep](deployment/webhook.md#missed-deliveries-the-redelivery-sweep) |
| **Reset all data**    | `reset`  | Deletes every pipeline resource in the namespace — findings, investigation/remediation runs, pinned repositories, rollups. Configuration (Integrations, Forges) survives. Two clicks to confirm                                                |

Reset then Replay re-runs the entire pipeline from ingestion — the demo loop. Grant these verbs sparingly; the
`patchy-findings-admin` example tier in `rbac.users.example.yaml` bundles them with the operator verbs.

## Access model

Two tiers, on purpose:

1. **Rollups are public.** Aggregate statistics carry no finding content; they serve without authentication so the page
   is useful as a wallboard even with no auth configured.
2. **Findings require sign-in + RBAC.** Without an [auth configuration](configuration/status-server.md), the findings
   views show "sign-in is not configured" and nothing else leaves the cluster. With one, a signed-in user sees findings
   only if RBAC grants `get` on `findings`, and each action button only with the matching custom verb (`approve` /
   `retry` / `expedite` / `suspend` / `resume` on `findings.patchy.bitwisemedia.uk`). Agent conversations are finding
   content and ride the same `get findings` gate; the change-notification stream (`/events`) stays public precisely
   because it carries no content at all, only the fact that something changed:

```yaml
rules:
  - apiGroups: [patchy.bitwisemedia.uk]
    resources: [findings, findingrollups]
    verbs: [get, list]
  - apiGroups: [patchy.bitwisemedia.uk]
    resources: [findings]
    verbs: [approve] # this user gets the Approve button, nothing else
```

Bind roles like this to the values of the OIDC `username`/`groups` claims (RoleBindings in the patchy namespace; grants
apply uniformly across its findings). Ready-made viewer / approver / operator / admin tiers ship in
`deploy/kustomize/base/rbac.users.example.yaml`, and the helm chart renders them with
`statusServer.rbac.userRoles=true`.

## Exposing the page

The Service is `patchy-status-server:8080`. Front it with your Ingress or Gateway on its **own hostname** — keeping it
apart from the webhook endpoint separates human authn from HMAC-signed machine traffic (helm: `statusServer.host` +
`statusServer.ingress` / `statusServer.httpRoute`). For a quick look without any exposure:

```sh
kubectl -n patchy port-forward svc/patchy-status-server 8080:8080
# → http://localhost:8080  (with the dev overlay's `mode: none` auth: full access)
```

## Canned data in dev

The dev overlay ships representative canned data so the page is worth looking at without running the pipeline:
`deploy/kustomize/overlays/dev/findings-demo.yaml` carries one `Finding` per lifecycle phase (the screenshots above)
plus `FindingRollup` statistics for every scope. `kubectl apply -k` creates the objects but cannot write their status
subresource, so seed the phases and rollup buckets with:

```sh
mise run status-demo
```

The data is deliberately inert — every finding is **suspended** (the enhancer, gate, and spawner all skip suspended
findings), none carries a `trackingRef` (so nothing is projected to GitHub), and terminal phases carry no `completedAt`
(so the rollup/TTL loop neither aggregates nor reaps them). Actions still work against it: resume one of the canned
findings and the controllers will pick it up for real, which is a fine way to watch the pipeline run.
