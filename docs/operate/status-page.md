# The status page

The status page is where people work with Patchy outside the tracking issues. It is a web dashboard served by the
[status-server](../configuration/status-server.md). It shows:

- every `Finding` in the namespace
- the two human gates (`AwaitingApproval` holds and `HandedOff` revival)
- the all-time metrics from the `FindingRollup` objects

The page is one self-contained HTML file compiled into the binary. It updates live: the server watches the Finding and
FindingRollup resources and tells open browsers over Server-Sent Events (SSE) when to fetch the data again.

The same findings, actions and RBAC are available from a terminal through the [patchy CLI](cli.md). The CLI reads and
writes the same custom resources, so the page and the CLI always show the same state.

## The top bar

The bar across every view holds, left to right:

- the patchy mark and the view links (**Findings**, **Metrics**, and **Configuration** for users who may see it), the
  current view underlined;
- the data mode: **Live** with a pulsing dot while the SSE stream is connected, **Snapshot** when it is not, or **Demo
  data** when the page runs on its built-in mock data;
- the Kubernetes namespace the page is showing;
- in demo mode only, a **Preview as** switcher (viewer, approver, operator) that shows the page as each RBAC tier sees
  it;
- the signed-in name, which opens the [user menu](#the-user-menu);
- the light and dark theme toggle. The choice is remembered in the browser.

On narrow screens all of it folds into one menu button.

## Views

The screenshots below show the [canned dev data](../deployment/dev-fake.md#canned-status-page-data). Every finding in
that data is suspended, which is why each row has a `suspended` pill.

### Findings

The findings view has four parts:

- A one-line summary of what is in progress and what needs a person. Each part of the summary filters the list to those
  findings.
- The eleven-phase lifecycle rail with live counts. Select a phase to filter by it.
- The same type-ahead filter as Metrics: free text, severity, phase, verdict and repository. Each applied filter shows
  as a chip you can remove. <kbd>/</kbd> focuses the filter.
- The list, showing each finding's severity, phase, confidence, verdict and age. The phase has a live dot while an agent
  run is active, and a `suspended` pill when the finding is suspended.

On narrow screens the rows stack and the navigation folds into a menu.

![The findings board](../assets/images/status-findings.jpg#only-dark)
![The findings board](../assets/images/status-findings-light.jpg#only-light)

### Finding detail

The detail view shows the advisory header with status and severity, tabs (Overview · Alerts · Timeline · Investigation ·
Remediation), the metadata sidebar (owners, repository, source, tracking issue, advisories, dates), and the action bar.
A completed remediation shows the merged PR. Terminal states show failure reasons, dismissal verdicts, and hand-off
routes. The investigation and remediation tabs each end in a **Conversation** section; see
[Agent conversations](#agent-conversations).

![A finding awaiting approval, with the action bar](../assets/images/status-finding-detail.jpg#only-dark)
![A finding awaiting approval, with the action bar](../assets/images/status-finding-detail-light.jpg#only-light)

### Metrics

The metrics view shows all-time statistics from the `FindingRollup` objects: how findings ended, verdict mix, per-stage
token/cost/duration totals, the monthly trend, and ranked repository, model and harness tables. One filter applies to
everything on the view. Type to find a repository (it is built for organisations with tens of thousands), a model, or a
harness. Or pick **All repositories matching "…"** to see the combined figures for every repository whose name contains
a term. Press <kbd>/</kbd> to focus the filter. Each slice has its own link (`#/metrics/repository/<owner%2Frepo>`); old
`#/rollups` links still resolve. It needs `get` on `findingrollups`.

![The metrics view](../assets/images/status-metrics.jpg#only-dark)
![The metrics view](../assets/images/status-metrics-light.jpg#only-light)

### Configuration

The configuration view is one page. It opens with a health summary, which lists anything not ready, failing or in
conflict and links to its card. Next comes the flow from integrations to enhancers to forges. Then it shows the
namespace's configured Integrations, Forges, and derived context enhancers, with:

- provider and capabilities
- credential Secret names (names only, never contents)
- readiness
- the failed-delivery sweep's last outcome
- the [manual backfill](../integrations/sources/github.md#the-manual-backfill) trigger with its last run's report

The nav link appears only for users granted `get` on `integrations`. The page is read-only apart from the backfill
trigger. You manage the resources themselves in the cluster.

## Agent conversations

Each run's investigation and remediation tab carries the agent's turn-by-turn conversation: its messages, its reasoning,
every tool it called and what came back. While a run is in flight the section streams live; afterwards it replays the
stored record.

This works because the agent pod converts its own CLI's output into one shared format. Claude's content blocks and
codex's items both become the same kind of turn. The pod writes these turns to stdout under a `PATCHY-TURN:` prefix,
alongside the `PATCHY-EVENT:` stage result. The owning controller reads that log once when the Job finishes. It writes
the conversation to a ConfigMap whose owner reference is the run. A run is owned by its Finding, so the
[finding TTL](../configuration/remediation-controller.md) deletes the transcript along with everything else. A
conversation is kept for as long as the finding it explains, and no longer.

A few behaviours are worth knowing:

- **The live stream is the full conversation; the stored record is a summary.** While a run is in flight the page shows
  every turn. There is no limit on the number of turns. A single turn is capped at 64 KiB, and the run as a whole at 8
  MiB. That cap is a safety limit: the kubelet rotates a container log at 10 MiB, and the stage result must stay
  readable after it. When the run is collected, the controller shrinks what it streamed to fit its ConfigMap: at most
  500 turns of at most 2 KiB each, and 512 KiB in total. It keeps the start and the end of the conversation, with one "N
  turns elided from stored record" notice between them. Any value matching a credential in the pod's environment is
  replaced with `«redacted»` before it leaves the pod. A conversation cut short by either bound says so, in the section
  footer and on the turn that was cut. The stream bounds are `PATCHY_TRANSCRIPT_MAX_TURN_BYTES`,
  `PATCHY_TRANSCRIPT_MAX_TURNS` (`0`, the default, is unbounded) and `PATCHY_TRANSCRIPT_MAX_TOTAL_BYTES`; the stored
  bounds are fixed.
- **Tool calls keep their body.** An edit shows as a diff, a file write shows its content, and a shell command shows the
  command, each syntax-highlighted; a file read is highlighted by the file's language, with its line numbers in a
  gutter. Fenced code blocks in the agent's prose, the report and the finding description are highlighted too.
- **A dropped connection resumes.** Every turn carries its sequence number as the event id. A viewer that reconnects
  picks up after the last turn it saw, with no gap and no repeats. This also applies to a viewer that fell too far
  behind the live stream and was told to reconnect.
- **You cannot use the upstream session id to fetch a conversation.** `stage.sessionID` records what the agent CLI
  called its session. That is useful for matching against model-provider logs. But the id only works with a session file
  in the pod's `$HOME`, which is an `emptyDir` that is deleted with the pod. Nothing can fetch a conversation from the
  provider afterwards, which is why patchy stores it.

Live streaming needs the status server to read pod logs in the agent namespace through the API server. It never connects
to an agent pod, so the agents' default-deny ingress policy is unchanged. Turn it off with
`statusServer.liveTranscripts=false` (helm) or by dropping the `patchy-status-server-agent-logs` Role and its binding
(kustomize); completed runs' conversations still serve, since those come from their own ConfigMaps.

## Actions and what they really do

The action bar renders only the verbs the signed-in user is granted _and_ the finding's state machine allows:

| Action       | Available when                    | What the server writes                                         |
| ------------ | --------------------------------- | -------------------------------------------------------------- |
| **Approve**  | `AwaitingApproval` or `HandedOff` | `spec.approval`, the same record the `/approve` comment writes |
| **Retry**    | `Failed`                          | `spec.retry`, a recovery request                               |
| **Expedite** | any phase up to `Queued`          | `spec.expedite`, a standing urgency mark                       |
| **Suspend**  | any non-terminal phase            | `spec.suspend: true`                                           |
| **Resume**   | a suspended finding               | `spec.suspend: false`                                          |

One action works on Integrations instead of findings. It is the configuration view's backfill trigger:

| Action       | Available when                                       | What the server writes             |
| ------------ | ---------------------------------------------------- | ---------------------------------- |
| **Backfill** | a github Integration with code scanning, unsuspended | `spec.backfill` on the Integration |

The status server never moves a phase. It writes the spec field, and the controller that owns the edge makes the
transition, the same way it does for a `/approve` issue comment or `patchy approve`. What each action then does to the
finding (which state a retry recovers to, what expedite skips) is in
[Human actions](../concepts/lifecycle.md#human-actions).

## The user menu

Select the signed-in name in the top bar to open a menu with sign-out and two demo actions:

- **Replay deliveries** (RBAC verb `replay`)
- **Reset all data** (verb `reset`)

Both are custom verbs on `integrations.patchy.bitwisemedia.uk`, and each is hidden unless you have its grant. Together
they re-run the full pipeline from ingestion. [Demo mode](../deployment/dev-fake.md#the-demo-loop-replay-and-reset)
covers what they write and when to grant them.

## Access model

Access has two tiers:

1. **Nothing is public.** Without an [auth configuration](../configuration/status-server.md) the page shows "sign-in is
   not configured" and no data leaves the cluster. Auth mode `none` is the one exception. It turns authentication off
   and opens everything (development only).
2. **Every view requires sign-in + RBAC.** Metrics need `get` on `findingrollups`, because repository names and
   per-repository spend should not be visible to anyone who can reach the page. A signed-in user sees findings only if
   RBAC grants `get` on `findings`, and each action button only with the matching custom verb (`approve` / `retry` /
   `expedite` / `suspend` / `resume` on `findings.patchy.bitwisemedia.uk`). The configuration view has its own gate:
   native `get` on `integrations`. Its backfill trigger has its own verb (`backfill` on
   `integrations.patchy.bitwisemedia.uk`, beside `replay` and `reset`). Agent conversations are finding content, so they
   use the same `get findings` gate. The change-notification stream (`/events`) carries no content, only the fact that
   something changed. It still needs any one of the read grants:

```yaml
rules:
  - apiGroups: [patchy.bitwisemedia.uk]
    resources: [findings, findingrollups]
    verbs: [get, list]
  - apiGroups: [patchy.bitwisemedia.uk]
    resources: [findings]
    verbs: [approve] # this user gets the Approve button, nothing else
```

Bind roles like this to the values of the OIDC `username`/`groups` claims, using RoleBindings in the patchy namespace. A
grant applies to every finding in that namespace. Ready-made viewer / approver / operator / admin tiers ship in
`deploy/kustomize/base/rbac.users.example.yaml`, and the helm chart renders them with
`statusServer.rbac.userRoles=true`.

## Exposing the page

The Service is `patchy-status-server:8080`. Put your Ingress or Gateway in front of it on its **own hostname**. Keeping
it apart from the webhook endpoint separates human sign-in from HMAC-signed machine traffic (helm: `statusServer.host` +
`statusServer.ingress` / `statusServer.httpRoute`). For a quick look without any exposure:

```sh
kubectl -n patchy port-forward svc/patchy-status-server 8080:8080
# → http://localhost:8080  (with the dev overlay's `mode: none` auth: full access)
```

Want the page populated without running the pipeline? The dev overlay ships
[canned data](../deployment/dev-fake.md#canned-status-page-data): one finding per lifecycle phase, plus metrics.
