# GitHub

A `Forge` answers one question — _how do I clone and push this repository?_ It is the only resource whose credential
ever touches repository contents: the source-controller uses it to download the archive investigations run against, and
the remediation-controller uses it to push the agent's changeset and open the pull request. GitHub — github.com or
GitHub Enterprise Server — is the supported forge today.

!!! info "One provider, two resources"

    This page is the GitHub **Forge** — repository read and write access. Webhook ingestion, tracking issues and
    dismissals are the separate [GitHub **Integration**](../sources/github.md); one GitHub App can back both. The
    [integrations overview](../index.md) maps every provider's roles.

## Repository access

A `Forge` is matched by host equality, then the optional `orgs` allowlist, then the optional repository-name regexes;
**the most-constrained match wins**, so a narrow `Forge` for one sensitive org overrides a broad default without
ordering rules.

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
it from its own artifact endpoint; remediation-controller replays the agent's changeset through the Git Data API. See
[the isolation model](../../deployment/isolation.md).

## Credentials

The Secret accepts the same two shapes as the [Integration's](../sources/github.md#credentials) — `appID` + `privateKey`
for a GitHub App, or `token` for development — and the two resources may share one Secret or split read and write across
two Apps. A `Forge`'s Secret needs no `webhookSecret`: nothing here receives deliveries. The credential is revalidated
on `spec.interval` and reported on the `Ready` condition.

## GitHub Enterprise Server

Point the resource at your instance and everything else is identical (the
[Integration](../sources/github.md#github-enterprise-server) takes the same field):

```yaml
spec:
  baseURL: https://ghes.example.com/api/v3
```

## Egress proxy

GitHub Enterprise Cloud estates commonly enforce an [organization IP allowlist], reachable only through a centrally
managed forward proxy. `spec.proxy` routes **all** of this forge's GitHub traffic — API calls, archive downloads
(`codeload.github.com`), pushes — through that proxy:

```yaml
spec:
  proxy:
    url: http://proxy.corp.example:3128
```

If the proxy requires basic authentication, put the credentials in the forge's credential Secret under the optional keys
`proxyUsername` and `proxyPassword` — never in the URL; the schema rejects embedded userinfo at admission.

**Precedence.** A `spec.proxy` overrides the process `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` environment for this forge's
traffic — including `NO_PROXY`, which does not punch holes in an explicit setting. Unset, the environment applies (set
cluster-wide via the [chart's `proxy` values](../../getting-started/install.md)). Remember the
[Integration](../sources/github.md#egress-proxy) authenticates with its own credential: in an allowlisted estate, set
the proxy on **both** resources.

[organization IP allowlist]:
  https://docs.github.com/en/organizations/keeping-your-organization-secure/managing-security-settings-for-your-organization/managing-allowed-ip-addresses-for-your-organization
