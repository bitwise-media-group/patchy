# status-server

The backend of the [status page](../operate/status-page.md): the embedded web dashboard over the `Finding` state machine
and the `FindingRollup` statistics, the sign-in surface, the five finding actions (approve, retry, expedite, suspend,
resume), and the integration-scoped admin verbs (backfill, replay, reset). It is a server, not a controller: it runs no
reconcilers and takes no leases. A controller-runtime cache gives it live watches, and an SSE stream tells open browsers
to refetch when anything changes.

```sh
status-server serve --namespace patchy --auth-config /etc/patchy/auth/config.yaml
```

## Routes

Every data route needs an authorised identity. With **no** auth config at all the server runs in the _unconfigured_
posture: it serves nothing but the page, which explains that sign-in is not configured. Auth mode `none` opens
everything.

| Route                                                       | Needs                                 | Serves                                                                                   |
| ----------------------------------------------------------- | ------------------------------------- | ---------------------------------------------------------------------------------------- |
| `GET /api/findings`                                         | `get` on `findings`                   | The trimmed list projection                                                              |
| `GET /api/findings/{name}`                                  | `get` on `findings`                   | One finding in full: description, alerts, enrichments, the phase log and the run reports |
| `GET /api/findings/{name}/runs/{kind}/{attempt}/transcript` | `get` on `findings`                   | An agent conversation as Server-Sent Events (`kind` is `investigation` or `remediation`) |
| `POST /api/findings/{name}/actions/{verb}`                  | the verb on `findings`                | One of the five [human actions](../concepts/lifecycle.md#human-actions)                  |
| `GET /api/rollups`                                          | `get` on `findingrollups`             | The metrics summary: total, model and harness rollups, the top 10 repositories           |
| `GET /api/rollups/repositories`                             | `get` on `findingrollups`             | Repository rollups, one page at a time (query below)                                     |
| `GET /api/config`                                           | `get` on `findings`                   | The configuration view: Integrations, Forges and their health                            |
| `POST /api/integrations/{name}/actions/backfill`            | `backfill` on `integrations`          | Writes `spec.backfill` on that Integration                                               |
| `POST /api/admin/{verb}`                                    | `replay` or `reset` on `integrations` | The namespace-wide demo tools: writes `spec.replay` or `spec.reset`                      |
| `GET /events`                                               | any read grant                        | The SSE refetch signal; it carries no content                                            |
| `/oauth2/…`, `/logout`                                      | none                                  | Sign-in and sign-out (mode `oidc`)                                                       |

The metrics summary stays small because repository rollups grow with the estate; `repositoryCount` says how many there
are. `GET /api/rollups/repositories` takes these query parameters:

| Parameter     | Meaning                                                                                  |
| ------------- | ---------------------------------------------------------------------------------------- |
| `q`           | Case-insensitive name match, ranked by relevance                                         |
| `key`         | One repository, with its `rank` by findings                                              |
| `sort`        | `findings`, `remediated`, `failed`, `spend`, `costPerRemediation`, `name` or `relevance` |
| `order`       | `asc` or `desc`                                                                          |
| `offset`      | Page start                                                                               |
| `limit`       | Page size, at most 100                                                                   |
| `aggregate=1` | Add the summed bucket of every match                                                     |

Agent conversations: a finished run replays its stored ConfigMap; a running one streams from the agent's pod log.
Following a live run needs `pods` and `pods/log` read in `--agent-namespace`, through the API server (the server never
dials an agent pod). Without that grant, completed runs' conversations still serve. See
[Agent conversations](../operate/status-page.md#agent-conversations).

## Flags

The [shared flags](index.md#shared-flags) (`--listen-addr` is the page's own address), plus:

| Flag                | Env                      | Default         | Purpose                                                                              |
| ------------------- | ------------------------ | --------------- | ------------------------------------------------------------------------------------ |
| `--agent-namespace` | `PATCHY_AGENT_NAMESPACE` | `patchy-agents` | Namespace the agent Jobs run in; live conversations are followed from their pod logs |
| `--auth-config`     | `PATCHY_AUTH_CONFIG`     | _(unset)_       | Mounted authentication config; absent means the server serves nothing (see below)    |

## Authentication configuration

`--auth-config` points at a YAML file, conventionally a mounted Secret (`patchy-status-auth`, key `config.yaml`). The
deployments mount it `optional`, so removing the Secret degrades to the unconfigured posture (nothing served) rather
than failing the pod. A present-but-invalid file is a startup error: a broken configuration never silently downgrades to
no authentication.

```yaml
mode: oidc # none | anonymous | oidc
sessionDuration: 168h # absolute session lifetime (default 7 days)
# insecure: true               # drop the cookies' Secure flag — plain-HTTP local dev ONLY
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

There is no server-side session store. The ID token, refresh token, and session start are sealed with AES-256-GCM — the
key is derived (HKDF-SHA256) from the OIDC client secret, so **rotating the client secret signs everyone out** — and
stored in chunked `HttpOnly` cookies (`patchy-auth`, `patchy-auth-1`, …). Every request re-verifies the ID token; an
expired token is renewed via the refresh token in place, but never past `sessionDuration` from the original sign-in.
Three small SPA-readable cookies carry no secrets: `patchy-auth-provider` (how sign-in works), `patchy-auth-error` (the
last failure), `patchy-auth-logout` (pauses `autoLogin` after an explicit sign-out).

The callback URL is derived from `X-Forwarded-Proto` / `X-Forwarded-Host` (or the Host header), which assumes a trusted
fronting proxy — set `oidc.redirectURL` explicitly if yours cannot be trusted to strip those.

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

The server's own ServiceAccount is deliberately narrow. It reads `findings`, `findingrollups`, `integrations` and
`forges`, and writes **spec only**: `spec.approval`, `spec.retry`, `spec.expedite` and `spec.suspend` on findings, and
`spec.backfill`, `spec.replay` and `spec.reset` on integrations. It never writes status and never moves a phase; the
owning controllers react to the spec change. It can delete the pipeline kinds (findings, runs, repositories, rollups)
for the access-review-gated reset, never the configuration. It also holds cluster-scoped `subjectaccessreviews create`
for the reviews, and, with live transcripts on, `pods` and `pods/log` read in the agent namespace.
