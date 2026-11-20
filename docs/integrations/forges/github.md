# GitHub

A `Forge` answers one question: _how do I clone and push this repository?_ It is the only resource whose credential ever
touches repository contents. The source-controller uses it to download the archive that investigations run against. The
remediation-controller uses it to push the agent's changeset and open the pull request. GitHub (github.com or GitHub
Enterprise Server) is the forge supported today.

!!! info "One provider, two resources"

    This page covers the GitHub **Forge**, which gives repository read and write access. Webhook ingestion, tracking
    issues and dismissals belong to the separate [GitHub **Integration**](../sources/github.md). One GitHub App can back
    both. The [integrations overview](../index.md) maps every provider's roles.

## Repository access

Patchy matches a `Forge` by host equality first, then the optional `orgs` allowlist, then the optional repository-name
regexes. **The most-constrained match wins.** So a narrow `Forge` for one sensitive org overrides a broad default, and
you do not need ordering rules.

```yaml
apiVersion: patchy.bitwisemedia.uk/v1alpha1
kind: Forge
metadata:
  name: github
  namespace: patchy
spec:
  provider: github
  secretRef:
    name: patchy-github
  # orgs: [acme]                          # optional allowlist
  # repositories: ["^acme/payments-.*$"]  # optional regexes
  interval: 10m
```

Agent pods never hold this credential, or any other. source-controller downloads the archive at a pinned SHA and serves
it from its own artifact endpoint. remediation-controller replays the agent's changeset through the Git Data API. See
[the isolation model](../../concepts/isolation.md).

## Credentials

The Secret accepts the same two shapes as the [Integration's](../sources/github.md#credentials): `appID` + `privateKey`
for a GitHub App, or `token` for development. The two resources can share one Secret, or you can split read and write
across two Apps. A `Forge`'s Secret needs no `webhookSecret`, because nothing here receives deliveries. The credential
is revalidated on `spec.interval` and reported on the `Ready` condition.

## GitHub Enterprise Server

Point the resource at your instance. Everything else stays the same. (The
[Integration](../sources/github.md#github-enterprise-server) takes the same field.)

```yaml
spec:
  baseURL: https://ghes.example.com/api/v3
```

## Egress proxy

GitHub Enterprise Cloud organizations often enforce an [organization IP allowlist]. Then GitHub is reachable only
through a centrally managed forward proxy. `spec.proxy` routes **all** of this forge's GitHub traffic through that
proxy: API calls, archive downloads (`codeload.github.com`) and pushes.

```yaml
spec:
  proxy:
    url: http://proxy.corp.example:3128
```

If the proxy requires basic authentication, put the credentials in the forge's credential Secret under the optional keys
`proxyUsername` and `proxyPassword`. Never put them in the URL. The schema rejects embedded userinfo at admission.

**Precedence.** A `spec.proxy` overrides the process `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` environment for this forge's
traffic. That includes `NO_PROXY`, which does not create exceptions to an explicit setting. When `spec.proxy` is unset,
the environment applies. You can set it cluster-wide with the
[chart's `proxy` values](../../getting-started/install.md). The [Integration](../sources/github.md#egress-proxy)
authenticates with its own credential. If your organization uses an IP allowlist, set the proxy on **both** resources.

[organization IP allowlist]:
  https://docs.github.com/en/organizations/keeping-your-organization-secure/managing-security-settings-for-your-organization/managing-allowed-ip-addresses-for-your-organization
