# status-server

The backend of the [status page](../operate/status-page.md). It provides:

- the embedded web dashboard over the `Finding` state machine and the `FindingRollup` statistics
- sign-in
- the five finding actions (approve, retry, expedite, suspend, resume)
- the integration-scoped admin verbs (backfill, replay, reset), and the backfill requests operators decide
- the optional [Connect GitHub](../integrations/sources/github.md#connect-github) link and the scoped maintainer verbs

It is a server and not a controller, so it runs no reconcilers and takes no leases. A controller-runtime cache gives it
live watches, and an SSE stream tells open browsers to refetch when anything changes.

```sh
status-server serve --namespace patchy --auth-config /etc/patchy/auth/config.yaml
```

## Routes

Every data route needs an authorised identity. With **no** auth config at all, the server runs _unconfigured_. It serves
only the page, which explains that sign-in is not configured. Auth mode `none` opens everything.

| Route                                                             | Needs                                 | Serves                                                                                                                                                                                                                                                                      |
| ----------------------------------------------------------------- | ------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /api/findings`                                               | `get` on `findings`                   | The findings list, one page at a time (query below)                                                                                                                                                                                                                         |
| `GET /api/findings/suggest`                                       | `get` on `findings`                   | The findings filter's type-ahead: the search count for a term and the matching repositories                                                                                                                                                                                 |
| `GET /api/findings/{name}`                                        | `get` on `findings`                   | One finding in full: description, alerts, enrichments, the phase log and the run reports                                                                                                                                                                                    |
| `GET /api/findings/{name}/runs/{kind}/{attempt}/transcript`       | `get` on `findings`                   | An agent conversation as Server-Sent Events (`kind` is `investigation` or `remediation`)                                                                                                                                                                                    |
| `GET /api/findings/{name}/runs/{kind}/{attempt}/archive/{object}` | `get` on `findings`                   | A presigned URL (`{url, expiresAt}`) for one [archived](../deployment/archival.md) object: `transcript`, `report`, `patch` or `manifest`. Each one is audit-logged                                                                                                          |
| `POST /api/findings/{name}/actions/{verb}`                        | the verb on `findings`                | One of the five [human actions](../concepts/lifecycle.md#human-actions)                                                                                                                                                                                                     |
| `GET /api/rollups`                                                | `get` on `findingrollups`             | The metrics summary: total, model and harness rollups, the top 10 repositories                                                                                                                                                                                              |
| `GET /api/rollups/repositories`                                   | `get` on `findingrollups`             | Repository rollups, one page at a time (query below)                                                                                                                                                                                                                        |
| `GET /api/config`                                                 | `get` on `findings`                   | The configuration view: Integrations, Forges and their health                                                                                                                                                                                                               |
| `GET /api/repositories`                                           | `get` on `findings`                   | The [repositories view](../operate/status-page.md#my-repositories): finding counts by phase and rollup presence for each `repo` (repeatable, at most 50)                                                                                                                    |
| `GET /api/repositories/access`                                    | `get` on `findings`                   | Whether an Integration can read each `repo`'s alerts and a Forge can push to it (at most 20). Only never-seen repositories are checked; the rest report `skipped: known`                                                                                                    |
| `POST /api/repositories/backfill`                                 | `get` on `findings`                   | `{"repository":"owner/name","request":false}`: a [one-repository backfill](#backfills). Runs at once with `backfill` (or `backfill-maintained` on a maintained repository); otherwise, or with `"request":true`, files a request for an operator. `429` inside the cooldown |
| `POST /api/integrations/{name}/actions/backfill`                  | `backfill` on `integrations`          | Creates a BackfillRequest on that Integration (`{"repositories":[…]}` optional)                                                                                                                                                                                             |
| `GET /api/backfill-requests`                                      | `get` on `findings`                   | Every BackfillRequest, newest first (at most 200), and `canDecide`: whether the caller holds `backfill`                                                                                                                                                                     |
| `POST /api/backfill-requests/{name}/{verdict}`                    | `backfill` on `integrations`          | `approve` or `deny` a requested backfill, once, with an optional `{"reason":"…"}` (at most 500 characters)                                                                                                                                                                  |
| `GET /api/github/repositories?integration=`                       | `get` on `findings`                   | The repositories the caller's linked GitHub account maintains through that Integration's App, for the import picker                                                                                                                                                         |
| `POST /api/admin/{verb}`                                          | `replay` or `reset` on `integrations` | The namespace-wide demo tools: writes `spec.replay` or `spec.reset`                                                                                                                                                                                                         |
| `GET /events`                                                     | any read grant                        | The SSE refetch signal; it carries no content                                                                                                                                                                                                                               |
| `/oauth2/…`, `/logout`                                            | none                                  | Sign-in and sign-out (mode `oidc`)                                                                                                                                                                                                                                          |
| `GET /oauth2/github/{integration}/authorize`, `…/callback`        | a signed-in identity                  | The [Connect GitHub](#connect-github) round trip through that Integration's GitHub App                                                                                                                                                                                      |
| `POST /oauth2/github/{integration}/disconnect`                    | none                                  | Drops the link cookie for that Integration                                                                                                                                                                                                                                  |

The findings list never ships whole, because a backlog can run to millions. `GET /api/findings` answers one page of the
filtered list, most severe first (then by phase, newest first, then name), with `matched` (how many match across every
page) and a `nextCursor` to continue it. The first page also carries `facets`: counts by phase, severity and verdict,
the open, running and suspended totals, and the repositories with most findings, all over every finding whatever the
filters. It takes these query parameters:

| Parameter      | Meaning                                                                                  |
| -------------- | ---------------------------------------------------------------------------------------- |
| `phase`        | A phase, such as `AwaitingApproval`. Repeat it to match any of several                   |
| `severity`     | `critical`, `high`, `medium` or `low`. Repeatable                                        |
| `verdict`      | `remediate`, `ignore` or `manual`. Repeatable                                            |
| `repo`         | A repository, `owner/name`. Repeatable                                                   |
| `repoPrefix`   | Repositories whose name starts with this, ignoring case. Repeatable                      |
| `repoContains` | Repositories whose name contains this, ignoring case. Repeatable                         |
| `q`            | Case-insensitive text in the name, title, rule, repository, advisories or owners         |
| `limit`        | Page size, default 50, at most 200. `0` returns only the counts                          |
| `cursor`       | The previous page's `nextCursor`. Pages never repeat or skip a row while findings change |

`GET /api/findings/suggest` takes the same filters, with `q` as the typed term and `limit` (default 6, at most 20) as
the number of repositories. It returns `matched` (what searching for the term would list), the repositories whose name
contains the term with their finding counts, and `prefix` and `contains`: how many repositories and findings the term
would match as a `repoPrefix` or `repoContains` filter.

The three repository parameters add up, like the repeated values of one parameter: a finding passes if it is in any
named repository or any repository a prefix or substring matches. A prefix or substring is matched against the name
after the owner, so in an organization's install `media` does not match every `my-media-org/…` repository. Include a `/`
(`my-media-org/pay`) to match the whole `owner/name`. At most 50 repository values are accepted.

The metrics summary leaves out most repository rollups, because their number grows with your repositories. Its
`repositoryCount` says how many there are. `GET /api/rollups/repositories` takes these query parameters:

| Parameter     | Meaning                                                                                  |
| ------------- | ---------------------------------------------------------------------------------------- |
| `q`           | Case-insensitive name match, ranked by relevance                                         |
| `key`         | One repository, with its `rank` by findings                                              |
| `sort`        | `findings`, `remediated`, `failed`, `spend`, `costPerRemediation`, `name` or `relevance` |
| `order`       | `asc` or `desc`                                                                          |
| `offset`      | Page start                                                                               |
| `limit`       | Page size, at most 100                                                                   |
| `aggregate=1` | Add the summed bucket of every match                                                     |

Agent conversations: a finished run replays its stored ConfigMap, and a running one streams from the agent's pod log. To
follow a live run, the server needs read access to `pods` and `pods/log` in `--agent-namespace`, through the API server.
The server never connects to an agent pod directly. Without that grant, the conversations of completed runs are still
served. See [Agent conversations](../operate/status-page.md#agent-conversations).

## Flags

The [shared flags](index.md#shared-flags) (`--listen-addr` is the page's own address), plus:

| Flag                                  | Env                                        | Default                                                              | Purpose                                                                                                                  |
| ------------------------------------- | ------------------------------------------ | -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `--agent-namespace`                   | `PATCHY_AGENT_NAMESPACE`                   | `patchy-agents`                                                      | Namespace the agent Jobs run in; live conversations are followed from their pod logs                                     |
| `--auth-config`                       | `PATCHY_AUTH_CONFIG`                       | none                                                                 | Mounted authentication config; absent means the server serves nothing (see below)                                        |
| `--integration-controller-url`        | `PATCHY_INTEGRATION_CONTROLLER_URL`        | `http://patchy-integration-controller.patchy.svc.cluster.local:9792` | The integration-controller's internal endpoint for the repository-access check; empty turns the check off                |
| `--integration-controller-token-file` | `PATCHY_INTEGRATION_CONTROLLER_TOKEN_FILE` | `/var/run/secrets/patchy/integration-controller/token`               | Projected ServiceAccount token (audience `patchy-integration-controller`) sent with each check; read again on every call |
| `--repository-backfill-cooldown`      | `PATCHY_REPOSITORY_BACKFILL_COOLDOWN`      | `24h`                                                                | How long after a repository's backfill ran that the repositories view refuses another for it                             |

The [repositories view](../operate/status-page.md#my-repositories) asks the integration-controller whether patchy can
read a never-seen repository's alerts. The server calls `POST /internal/v1/repository-access` on the integration
controller's [internal listener](integration-controller.md#the-repository-access-endpoint), and proves who it is with a
projected ServiceAccount token. Mount one with audience `patchy-integration-controller` at the token file's path; the
kustomize base and the Helm chart both do. The kubelet rotates the token, which is why the server reads the file again
on every call. With `--integration-controller-url` empty the page says the access check is unavailable, and offers no
one-repository backfill. [Connect GitHub](#connect-github) goes through the same endpoint and token, so it is off too.

### Backfills

Backfills are `BackfillRequest` objects the server files for the signed-in user (it records them in `spec.requestedBy`);
the integration-controller walks them. The one-repository backfill takes one of three paths:

| The caller holds                                               | The request                                                  |
| -------------------------------------------------------------- | ------------------------------------------------------------ |
| `backfill` on `integrations`                                   | runs at once (`spec.via` empty)                              |
| `backfill-maintained`, a link, and admin or maintain on GitHub | runs at once (`spec.via: maintained`, with the GitHub login) |
| only `get` on `findings`, or asks with `"request":true`        | waits for an operator (`spec.via: requested`)                |

All three share the same limits. An open request that covers the repository answers `covered` instead of filing a
second. A walk that named the repository and completed less than `--repository-backfill-cooldown` ago answers `429` with
`Retry-After`. A whole-scope walk does not count, because it says nothing about whether the repository was in it. A user
may have at most 10 requests waiting for an operator. The cooldown reads finished requests, so keep it below the
integration-controller's `--backfill-request-ttl` (default `168h`), which deletes them.

To play archived runs back, give the server the same `--archive-provider`, `--archive-bucket`, `--archive-prefix`,
`--archive-region` and `--archive-endpoint` (`PATCHY_ARCHIVE_*`) as the controllers, plus `--archive-presign-ttl`
(`PATCHY_ARCHIVE_PRESIGN_TTL`, default `5m`, at most 7 days): how long each presigned URL it hands the browser stays
valid. The server only signs URLs; the browser fetches the object from the bucket itself, so the bucket needs CORS for
the page's origin. See [Run archival](../deployment/archival.md).

## Authentication configuration

`--auth-config` points at a YAML file, conventionally a mounted Secret (`patchy-status-auth`, key `config.yaml`). The
deployments mount it `optional`. If you remove the Secret, the server falls back to running unconfigured (nothing
served) and the pod keeps running. A file that is present but invalid is a startup error, so a broken configuration
never quietly turns authentication off.

```yaml
mode: oidc # none | anonymous | oidc
sessionDuration: 168h # absolute session lifetime (default 7 days)
# insecure: true               # drop the cookies' Secure flag; plain-HTTP local dev ONLY
anonymous: # mode: anonymous only
  username: status-viewer
  groups: [patchy-viewers]
oidc: # mode: oidc only
  issuerURL: https://sso.example.com
  clientID: patchy-status
  clientSecret: "..." # or clientSecretFile: /path/to/projected/key
  # scopes: [openid, offline_access, profile, email, groups]
  # authURLParams: {}        # extra authorize-endpoint query parameters
  # autoLogin: false         # bounce straight to the provider instead of the sign-in panel
  # redirectURL: ""          # override the derived <scheme>://<host>/oauth2/callback
  claims: # claim NAMES, mapped onto the identity
    username: email # the subject access reviews run for
    groups: groups
    displayName: name
# linkKeyFile: /etc/patchy/auth/linkKey  # modes anonymous and none: keys the Connect GitHub cookies
```

### Modes

| Mode        | Who the visitor is                     | Access reviews | Use                                                                                               |
| ----------- | -------------------------------------- | -------------- | ------------------------------------------------------------------------------------------------- |
| _(no file)_ | nobody                                 | n/a            | Unconfigured: the server serves no data                                                           |
| `none`      | a fixed development identity           | bypassed       | The dev overlay; never expose it                                                                  |
| `anonymous` | the one configured username and groups | run            | Cluster RBAC for that identity decides what everyone sees and does                                |
| `oidc`      | the signed-in user                     | run            | Real SSO. The server is the OAuth2 client (authorization code + PKCE); the SPA never sees a token |

### Sessions and cookies (mode `oidc`)

There is no server-side session store. The server seals the ID token, refresh token and session start time with
AES-256-GCM, and stores them in chunked `HttpOnly` cookies (`patchy-auth`, `patchy-auth-1`, …). The key is derived
(HKDF-SHA256) from the OIDC client secret, so **rotating the client secret signs everyone out**. Every request
re-verifies the ID token. When the token expires, the server renews it with the refresh token, but never past
`sessionDuration` from the original sign-in. Three small cookies that the SPA can read carry no secrets:

- `patchy-auth-provider` (how sign-in works)
- `patchy-auth-error` (the last failure)
- `patchy-auth-logout` (pauses `autoLogin` after an explicit sign-out)

The callback URL is derived from `X-Forwarded-Proto` / `X-Forwarded-Host` (or the Host header). This assumes a trusted
proxy in front of the server. If you cannot trust your proxy to strip those headers, set `oidc.redirectURL` explicitly.

### Connect GitHub

The optional [Connect GitHub](../integrations/sources/github.md#connect-github) link attaches a GitHub identity to the
signed-in user, one per github Integration that sets `spec.github.userAuthorization` and reports
`UserAuthorizationReady`. It runs the GitHub App's user authorization flow (authorization code + PKCE) on
`/oauth2/github/<integration>/authorize` and `/callback`, so register
`https://<status-host>/oauth2/github/<integration>/callback` on the App. The host comes from `oidc.redirectURL` when it
is set, and from the forwarded headers otherwise, as for sign-in.

The server never calls GitHub itself. The code exchange, every token refresh, the repository list and each role check go
through the integration-controller's [GitHub-user endpoints](integration-controller.md#the-github-user-endpoints), which
hold the App's client secret.

The link lives in one sealed, `HttpOnly` cookie per Integration (`patchy-ghlink-<integration>`), bound to the username
it was made for, so a link made by another user of a shared browser is ignored. It lasts no longer than
`sessionDuration` from the moment it was made. Tokens refresh only on requests that call GitHub, and a refresh GitHub
rejects drops the link (the page offers **Reconnect**). The cookie key is derived (HKDF-SHA256, with its own label)
from:

- the OIDC client secret, in mode `oidc`. Nothing to configure. Rotating the secret drops every link as well as every
  session.
- `linkKeyFile`, in modes `anonymous` and `none`, which have no client secret. It names a file of at least 32 bytes
  (surrounding whitespace is trimmed). Without it, linking is off in those modes. Every key of the auth Secret is
  mounted under `/etc/patchy/auth`, so the simplest place for it is a second key there:

```sh
kubectl -n patchy create secret generic patchy-status-auth \
  --from-file=config.yaml=./status-auth.yaml \
  --from-literal=linkKey="$(openssl rand -hex 32)"
```

with `linkKeyFile: /etc/patchy/auth/linkKey` in `config.yaml`. With the Helm chart, use
`statusServer.auth.existingSecret` for this, since the chart-managed Secret holds only `config.yaml`.

## Authorization

Per-user grants are ordinary Kubernetes RBAC, resolved server-side with `SubjectAccessReview`s for the signed-in user.
Users need no kubeconfig and never talk to the API server.

| Grant                                                                          | Gates                                                                                                                  |
| ------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------- |
| native `get` on `findings`                                                     | Viewing findings and the configuration                                                                                 |
| native `get` on `findingrollups`                                               | The metrics view                                                                                                       |
| custom verbs `approve`, `retry`, `expedite`, `suspend`, `resume` on `findings` | The action buttons, one verb per button                                                                                |
| custom verb `backfill` on `integrations`                                       | The backfill buttons (configuration view, and one repository on the repositories view), and deciding backfill requests |
| custom verbs `replay`, `reset` on `integrations`                               | The user menu's demo tools                                                                                             |
| custom verbs `approve-maintained`, `expedite-maintained` on `findings`         | Approve and expedite on findings in repositories the linked GitHub account maintains                                   |
| custom verb `backfill-maintained` on `integrations`                            | The one-repository backfill, at once, on repositories the linked account maintains                                     |

Grants are namespace-scoped and stamped into the payload as each finding's `userActions`. The client intersects them
with the finding's own state machine, and every `POST` is re-checked server-side. That is 14 reviews per identity,
cached for 20 seconds. See `deploy/kustomize/base/rbac.users.example.yaml` for ready-made viewer, approver, operator,
admin and maintainer tiers.

The three maintainer verbs each stand in for a base verb (`backfill`, `approve`, `expedite`), scoped to repositories
where the user's linked GitHub identity is `admin` or `maintain`. A list of those repositories, cached for five minutes,
decides which buttons show; every `POST` asks GitHub for the role again. A finding with no GitHub repository, or a user
with no link on its host, gets only the namespace-wide verb. **Only the status page honours them.** The CLI and the
admission policy do not, because neither can present the GitHub link, so a scoped grant on its own permits no direct API
write.

The server's own ServiceAccount has narrow permissions. It reads `findings`, `findingrollups`, `integrations`, `forges`
and `backfillrequests`, and writes **spec only**: `spec.approval`, `spec.retry`, `spec.expedite` and `spec.suspend` on
findings, `spec.replay` and `spec.reset` on integrations, and new `backfillrequests` with their one-time `spec.decision`
(`create` and `update`). It never writes status and never moves a phase. The owning controllers react to the spec
change. For the reset action, which an access review guards, it can delete the pipeline kinds (findings, runs,
repositories, rollups). It can never delete the configuration. It also holds cluster-scoped
`subjectaccessreviews create` for the reviews, and, with live transcripts on, `pods` and `pods/log` read in the agent
namespace.
