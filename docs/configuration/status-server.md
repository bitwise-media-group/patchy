# status-server

The status page backend: the embedded web dashboard over the `Finding` state machine and the `FindingRollup` statistics,
the sign-in surface, and the three human actions (approve, suspend, resume). It is a server, not a controller — it runs
no reconcilers and takes no leases; a controller-runtime cache gives it live watches, and an SSE stream tells open
browsers to refetch when anything changes.

```sh
status-server serve --namespace patchy --auth-config /etc/patchy/auth/config.yaml
```

The exposure contract is deliberately asymmetric — see [the status page](../status-ui.md) for the UI tour and the RBAC
grammar:

- **Rollup statistics are public.** `GET /api/rollups` (and the SSE stream) serve without a session.
- **The findings surface always requires authentication.** `GET /api/findings` and every action `POST` demand a
  signed-in identity whose RBAC passes the corresponding access review. With no auth config at all the server runs in
  the _unconfigured_ posture: rollups only, and the page explains that sign-in is not configured.

## Flags

The [shared flags](index.md#shared-flags-all-five-controllers) (`--listen-addr` is the page's own address here — there
is no webhook), plus:

<div class="nowrap-first" markdown>

| Flag            | Env                  | Default         | Purpose                                                          |
| --------------- | -------------------- | --------------- | ---------------------------------------------------------------- |
| `--namespace`   | `PATCHY_NAMESPACE`   | `POD_NAMESPACE` | Namespace the Findings and FindingRollups live in                |
| `--kubeconfig`  | `PATCHY_KUBECONFIG`  | in-cluster      | Kubeconfig path for running outside the cluster                  |
| `--health-addr` | `PATCHY_HEALTH_ADDR` | `:8081`         | healthz/readyz probe listen address                              |
| `--auth-config` | `PATCHY_AUTH_CONFIG` | _(unset)_       | Mounted authentication config; absent ⇒ rollups-only (see below) |

</div>

## Authentication configuration

`--auth-config` points at a YAML file, conventionally a mounted Secret (`patchy-status-auth`, key `config.yaml` — the
deployments mount it `optional`, so removing the Secret degrades to rollups-only rather than failing the pod). A
present-but-invalid file is a startup error: a broken configuration never silently downgrades to no authentication.

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

- **`none`** — every request is a fixed development identity with authorization bypassed entirely. The dev overlay ships
  this; never expose it.
- **`anonymous`** — every request is the one configured identity, but access reviews still run: cluster RBAC for that
  username/groups decides what every visitor may see and do.
- **`oidc`** — the real SSO flow. The server itself is the OAuth2 client (authorization-code + PKCE); the SPA never sees
  a token.

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

Per-user grants are ordinary Kubernetes RBAC, resolved server-side with `SubjectAccessReview`s for the signed-in user
(users need no kubeconfig and never talk to the API server):

- native `get` on `findings` gates **viewing** the findings surface;
- the **custom verbs** `approve`, `suspend`, `resume` on `findings.patchy.bitwisemedia.uk` gate the action buttons, one
  verb per button.

Grants are namespace-scoped and stamped into the payload as each finding's `userActions`; the client intersects them
with the finding's own state machine, and every `POST` is re-checked server-side. See
`deploy/kustomize/base/rbac.users.example.yaml` for ready-made viewer / approver / operator tiers.

The server's own ServiceAccount is deliberately narrow: `findings` read + **spec** write (approve records
`spec.approval`; suspend/resume toggle `spec.suspend` — it never writes `findings/status` and never moves a phase; the
owning controllers react to the spec change), `findingrollups` read, and cluster-scoped `subjectaccessreviews create`
for the reviews.
