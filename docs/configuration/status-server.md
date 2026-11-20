# status-server

The backend of the [status page](../operate/status-page.md). It provides:

- the embedded web dashboard over the `Finding` state machine and the `FindingRollup` statistics
- sign-in
- the five finding actions (approve, retry, expedite, suspend, resume)
- the integration-scoped admin verbs (backfill, replay, reset)

It is a server and not a controller, so it runs no reconcilers and takes no leases. A controller-runtime cache gives it
live watches, and an SSE stream tells open browsers to refetch when anything changes.

```sh
status-server serve --namespace patchy --auth-config /etc/patchy/auth/config.yaml
```

## Routes

Every data route needs an authorised identity. With **no** auth config at all, the server runs _unconfigured_. It serves
only the page, which explains that sign-in is not configured. Auth mode `none` opens everything.

| Route                                                             | Needs                                 | Serves                                                                                                                                                             |
| ----------------------------------------------------------------- | ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `GET /api/findings`                                               | `get` on `findings`                   | The trimmed list projection                                                                                                                                        |
| `GET /api/findings/{name}`                                        | `get` on `findings`                   | One finding in full: description, alerts, enrichments, the phase log and the run reports                                                                           |
| `GET /api/findings/{name}/runs/{kind}/{attempt}/transcript`       | `get` on `findings`                   | An agent conversation as Server-Sent Events (`kind` is `investigation` or `remediation`)                                                                           |
| `GET /api/findings/{name}/runs/{kind}/{attempt}/archive/{object}` | `get` on `findings`                   | A presigned URL (`{url, expiresAt}`) for one [archived](../deployment/archival.md) object: `transcript`, `report`, `patch` or `manifest`. Each one is audit-logged |
| `POST /api/findings/{name}/actions/{verb}`                        | the verb on `findings`                | One of the five [human actions](../concepts/lifecycle.md#human-actions)                                                                                            |
| `GET /api/rollups`                                                | `get` on `findingrollups`             | The metrics summary: total, model and harness rollups, the top 10 repositories                                                                                     |
| `GET /api/rollups/repositories`                                   | `get` on `findingrollups`             | Repository rollups, one page at a time (query below)                                                                                                               |
| `GET /api/config`                                                 | `get` on `findings`                   | The configuration view: Integrations, Forges and their health                                                                                                      |
| `POST /api/integrations/{name}/actions/backfill`                  | `backfill` on `integrations`          | Writes `spec.backfill` on that Integration                                                                                                                         |
| `POST /api/admin/{verb}`                                          | `replay` or `reset` on `integrations` | The namespace-wide demo tools: writes `spec.replay` or `spec.reset`                                                                                                |
| `GET /events`                                                     | any read grant                        | The SSE refetch signal; it carries no content                                                                                                                      |
| `/oauth2/…`, `/logout`                                            | none                                  | Sign-in and sign-out (mode `oidc`)                                                                                                                                 |

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

| Flag                | Env                      | Default         | Purpose                                                                              |
| ------------------- | ------------------------ | --------------- | ------------------------------------------------------------------------------------ |
| `--agent-namespace` | `PATCHY_AGENT_NAMESPACE` | `patchy-agents` | Namespace the agent Jobs run in; live conversations are followed from their pod logs |
| `--auth-config`     | `PATCHY_AUTH_CONFIG`     | none            | Mounted authentication config; absent means the server serves nothing (see below)    |

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

## Authorization

Per-user grants are ordinary Kubernetes RBAC, resolved server-side with `SubjectAccessReview`s for the signed-in user.
Users need no kubeconfig and never talk to the API server.

| Grant                                                                          | Gates                                    |
| ------------------------------------------------------------------------------ | ---------------------------------------- |
| native `get` on `findings`                                                     | Viewing findings and the configuration   |
| native `get` on `findingrollups`                                               | The metrics view                         |
| custom verbs `approve`, `retry`, `expedite`, `suspend`, `resume` on `findings` | The action buttons, one verb per button  |
| custom verb `backfill` on `integrations`                                       | The configuration view's backfill button |
| custom verbs `replay`, `reset` on `integrations`                               | The user menu's demo tools               |

Grants are namespace-scoped and stamped into the payload as each finding's `userActions`. The client intersects them
with the finding's own state machine, and every `POST` is re-checked server-side. See
`deploy/kustomize/base/rbac.users.example.yaml` for ready-made viewer, approver and operator tiers.

The server's own ServiceAccount has narrow permissions. It reads `findings`, `findingrollups`, `integrations` and
`forges`, and writes **spec only**: `spec.approval`, `spec.retry`, `spec.expedite` and `spec.suspend` on findings, and
`spec.backfill`, `spec.replay` and `spec.reset` on integrations. It never writes status and never moves a phase. The
owning controllers react to the spec change. For the reset action, which an access review guards, it can delete the
pipeline kinds (findings, runs, repositories, rollups). It can never delete the configuration. It also holds
cluster-scoped `subjectaccessreviews create` for the reviews, and, with live transcripts on, `pods` and `pods/log` read
in the agent namespace.
