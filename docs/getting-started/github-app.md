# Create the GitHub App

Patchy authenticates as a GitHub App. For every operation, the controllers mint short-lived installation tokens scoped
to a single repository, instead of holding a long-lived personal access token. One App serves every patchy component.
Later, you can split read and write identities across two Apps, one per custom resource.

## Register the App

Go to **Settings → Developer settings → GitHub Apps → New GitHub App** on your organization (not your user account) and
fill in:

- **GitHub App name**: for example `patchy`.
- **Homepage URL**: anything. The repository URL works.
- **Webhook → Active**: checked. You set the webhook URL and secret below.

## Repository permissions

Grant these permissions and no others:

| Permission               | Access       | Why                                                          |
| ------------------------ | ------------ | ------------------------------------------------------------ |
| **Code scanning alerts** | Read & write | Read alert detail; dismiss false positives                   |
| **Issues**               | Read & write | The tracking projection: open, label, comment, close         |
| **Contents**             | Read & write | Download the repository archive; push the remediation branch |
| **Pull requests**        | Read & write | Open the pull request a human reviews                        |
| **Metadata**             | Read         | Mandatory for every App                                      |

## Webhook events

Subscribe to these four events:

| Event                 | Purpose                                                              |
| --------------------- | -------------------------------------------------------------------- |
| `code_scanning_alert` | New findings arrive and accumulate into `Finding` resources          |
| `issues`              | Human signals on the tracking issue: close hands off, reopen revives |
| `issue_comment`       | The `/approve` release for held findings                             |
| `pull_request`        | Close the loop when the remediation PR merges (or closes unmerged)   |

The **integration-controller** consumes all four. It is the only webhook receiver in the system. Webhooks do not drive
pipeline progress; the reconcile loops do. The webhook path carries only ingestion and human-in-the-loop signals.

## The webhook URL

A GitHub App has only **one** webhook URL. Point it at the integration-controller, the only patchy component exposed to
the internet:

```text
https://patchy.example.com/github/webhooks
```

Before anything else happens, the integration-controller checks each delivery against the webhook secrets of your
configured `Integration` resources. To expose it, set the chart's `webhook.host` plus `webhook.ingress` or
`webhook.httpRoute`. See [Deployment → Webhook exposure](../deployment/webhook.md) for both options and for notes on
managed platforms (EKS, AKS, GKE).

## Collect the credentials

You collect three values on this page. They go into one [Kubernetes Secret](install.md#create-the-secrets),
`patchy-github`:

1. **Webhook secret**: generate one now and paste it into the App's **Webhook secret** field:

   ```sh
   openssl rand -hex 32
   ```

2. **App ID**: shown on the App's **General** page after you create it.

3. **Private key**: **General → Private keys → Generate a private key** downloads a `.pem` file.

The `Forge` or `Integration` that references this Secret may name a forward proxy that needs basic authentication
([egress proxy](../integrations/forges/github.md#egress-proxy)). If so, the same Secret also holds the optional keys
`proxyUsername` and `proxyPassword`. The proxy credentials never go in the proxy URL itself.

## Install the App

Finally, choose **Install App** on your organization and select the repositories patchy should watch (the ones with code
scanning enabled). The controllers find the installation for each repository automatically, so you do not need to
configure an installation ID.

!!! info "GitHub Enterprise Server"

    Point the custom resources at your instance with `spec.github.baseURL` on the `Integration` and `spec.baseURL`
    on the `Forge`, for example `https://ghes.example.com/api/v3`. Everything else stays the same.
