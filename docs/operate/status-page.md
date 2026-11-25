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

- the patchy mark and the view links (**Findings**, **My repositories**, **Metrics**, and **Configuration** for users
  who may see it), the current view underlined;
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
  as a chip you can remove. <kbd>/</kbd> focuses the filter. Repositories add up like severities do: pick several, or
  pick **Repositories starting with** or **Repositories containing** what you typed to cover every repository whose name
  matches.
- The list, showing each finding's severity, phase, confidence, verdict and age. The phase has a live dot while an agent
  run is active, and a `suspended` pill when the finding is suspended.

The list puts the most severe first. It loads 50 findings at a time, and more load as you scroll. Filtering happens on
the server, so the counts in the summary, the rail and the filter always cover every finding, not just the loaded rows.
When findings change while you are scrolled down the list, the rows you are reading stay where they are and an
**Updated. Show the latest** button appears; at the top of the list, changes appear straight away. The list holds at
most 5,000 rows; past that, narrow the filters to see the rest.

The filter is part of the page address (`#/?severity=critical&repoPrefix=payments`), so you can bookmark or share a
filtered list and it opens with the same filter. The parameters are the ones
[`GET /api/findings`](../configuration/status-server.md) takes.

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

### My repositories

The repositories view (`#/repositories`) is for maintainers who care about a handful of repositories, not the whole
estate. You keep a list of repositories, and each row shows where that repository's findings stand.

**The list lives in your browser.** Add a repository by its exact `owner/name`; the field suggests names as you type.
The list is saved in this browser's local storage, not on the server, so it does not follow you to another browser or
machine, and nobody else sees it. To move it, or to share a starting list with your team, use **Export** to download it
as JSON and **Import** to load it in another browser. Clearing the site's data clears the list.

**Each row counts the repository's findings by where they are:**

| Column            | Phases                                |
| ----------------- | ------------------------------------- |
| Open              | `Opened`, `Enhanced`, `Investigating` |
| Awaiting approval | `AwaitingApproval`                    |
| In progress       | `Queued`, `Remediating`               |
| In review         | `InReview`                            |
| Remediated        | `Remediated`                          |
| Failed            | `Failed`                              |
| Closed            | `Dismissed`, `HandedOff`              |

Every count is a link to the findings list, filtered to that repository and those phases, so **In progress** for
`acme/shop` opens `#/?phase=Queued&phase=Remediating&repo=acme%2Fshop`. The counts cover the findings that exist now.
Completed findings expire after the [finding TTL](../configuration/remediation-controller.md), so for the all-time
figures the row links to the repository's [metrics](#metrics) slice whenever it has a `FindingRollup`.

**A repository patchy has never seen gets an access check.** When a repository has no findings and no rollup, the page
asks the integration-controller whether any GitHub Integration can read its alerts. The check tries each github
Integration that is not suspended and has code or secret scanning turned on. When none can read the repository, the row
shows why, for example:

- the GitHub App is not installed on the repository;
- the credential is missing the `security_events: read` permission;
- GitHub Advanced Security (code scanning) is off for the repository.

When an Integration can read it, the repository may simply have no open alerts, or its alerts may predate the App's
webhook. To find out, the row offers a one-repository **Backfill**. It asks you to confirm with a second click. It then
requests the [manual backfill](../integrations/sources/github.md#the-manual-backfill) for that one repository on the
first Integration, by name, that can read it. If a backfill request is already waiting on that Integration, the
repository joins its list instead of starting a new one, and a waiting request for everything already covers it. The
button needs the same `backfill` verb on `integrations` as the configuration view's trigger. Without it, the row says
**Ask an operator to backfill**.

Backfill is **never offered for a repository that already has findings or a rollup**. Patchy already knows about those
repositories, and webhooks keep them up to date, so a backfill would only spend GitHub API requests. The server refuses
such a request too.

The access check is cached for five minutes for each Integration and repository (30 seconds after a temporary error), so
reloading the page does not call GitHub again. When an operator has turned the check off (an empty
`--integration-controller-url` on the status server), the page says the access check is unavailable.

**A separate warning covers pull requests.** The same check looks for the [Forge](../integrations/forges/github.md) that
covers the repository. When no Forge covers it, or its GitHub App lacks `contents: write`, the row warns that patchy
cannot push remediation pull requests there. This does not block a backfill: patchy can still triage the findings, and a
person fixes them.

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

### Archived runs

With [run archival](../deployment/archival.md) on, a collected run's record lives in an S3 or GCS bucket instead of a
ConfigMap, and it outlives the finding. The conversation section looks the same, but the full, uncompacted conversation
is played back: the page asks the status server for a short-lived presigned URL, fetches the gzipped transcript straight
from the bucket, and decompresses it in the browser. Its footer reads "full record from the archive". The investigation
and remediation tabs also gain **Full report** (the untruncated report, frontmatter included) and, for a remediation,
**Download patch** (`git format-patch` output you can apply with `git am`). The server never relays bucket bytes, so the
bucket must allow `GET` from the page's origin (CORS); without it the page says the archived conversation could not be
loaded.

## Actions and what they really do

The action bar renders only the verbs the signed-in user is granted _and_ the finding's state machine allows:

| Action       | Available when                    | What the server writes                                         |
| ------------ | --------------------------------- | -------------------------------------------------------------- |
| **Approve**  | `AwaitingApproval` or `HandedOff` | `spec.approval`, the same record the `/approve` comment writes |
| **Retry**    | `Failed`                          | `spec.retry`, a recovery request                               |
| **Expedite** | any phase up to `Queued`          | `spec.expedite`, a standing urgency mark                       |
| **Suspend**  | any non-terminal phase            | `spec.suspend: true`                                           |
| **Resume**   | a suspended finding               | `spec.suspend: false`                                          |

One action works on Integrations instead of findings. It is the backfill, from the configuration view or for one
repository from the [repositories view](#my-repositories):

| Action       | Available when                                                                                        | What the server writes                                      |
| ------------ | ----------------------------------------------------------------------------------------------------- | ----------------------------------------------------------- |
| **Backfill** | a github Integration with code scanning, unsuspended                                                  | `spec.backfill` on the Integration                          |
| **Backfill** | (one repository) the repository has no findings and no rollup, and an Integration can read its alerts | the repository added to `spec.backfill` on that Integration |

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
   `expedite` / `suspend` / `resume` on `findings.patchy.bitwisemedia.uk`). The repositories view uses the same
   `get findings` gate. The configuration view has its own gate: native `get` on `integrations`. Its backfill trigger
   has its own verb (`backfill` on `integrations.patchy.bitwisemedia.uk`, beside `replay` and `reset`). Agent
   conversations are finding content, so they use the same `get findings` gate. The change-notification stream
   (`/events`) carries no content, only the fact that something changed. It still needs any one of the read grants:

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
