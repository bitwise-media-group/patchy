# Create the GitHub App

Patchy authenticates as a GitHub App: the controllers mint short-lived, single-repository installation tokens for every
operation instead of holding a long-lived personal access token. One App serves the whole stack (though you may split
read and write identities across two Apps — one per custom resource — later).

## Register the App

Go to **Settings → Developer settings → GitHub Apps → New GitHub App** (on your organization, not your user account) and
fill in:

- **GitHub App name** — e.g. `patchy`.
- **Homepage URL** — anything; the repository URL works.
- **Webhook → Active** — checked. The webhook URL and secret come below.

## Repository permissions

Grant exactly these — nothing more:

<div class="nowrap-first" markdown>

| Permission               | Access       | Why                                                          |
| ------------------------ | ------------ | ------------------------------------------------------------ |
| **Code scanning alerts** | Read & write | Read alert detail; dismiss false positives                   |
| **Issues**               | Read & write | The tracking projection — open, label, comment, close        |
| **Contents**             | Read & write | Download the repository archive; push the remediation branch |
| **Pull requests**        | Read & write | Open the pull request a human reviews                        |
| **Metadata**             | Read         | Mandatory for every App                                      |

</div>

## Webhook events

Subscribe to these four events:

<div class="nowrap-first" markdown>

| Event                 | Purpose                                                               |
| --------------------- | --------------------------------------------------------------------- |
| `code_scanning_alert` | New findings arrive and accumulate into `Finding` resources           |
| `issues`              | Human signals on the tracking issue — close hands off, reopen revives |
| `issue_comment`       | The `/approve` release for held findings                              |
| `pull_request`        | Close the loop when the remediation PR merges (or closes unmerged)    |

</div>

All four are consumed by the **integration-controller** — the only webhook receiver in the system. Pipeline progress
itself is not webhook-driven: the reconcile loops carry it, and the webhook path is ingestion and human-in-the-loop
signals.

## The webhook URL

A GitHub App has exactly **one** webhook URL. Point it at the integration-controller — the only patchy component exposed
to the internet:

```text
https://patchy.example.com/github/webhooks
```

The integration-controller validates each delivery against the webhook secrets of your configured `Integration`
resources before anything else happens. Enable the exposure with the chart's `webhook.host` plus `webhook.ingress` or
`webhook.httpRoute` — see [Deployment → Webhook exposure](../deployment/webhook.md) for both flavours and the
managed-platform (EKS, AKS, GKE) notes.

## Collect the credentials

Three values leave this page and become one [Kubernetes Secret](install.md#create-the-secrets), `patchy-github`:

1. **Webhook secret** — generate one now and paste it into the App's **Webhook secret** field:

   ```sh
   openssl rand -hex 32
   ```

2. **App ID** — shown on the App's **General** page after creation.

3. **Private key** — **General → Private keys → Generate a private key** downloads a `.pem` file.

## Install the App

Finally, **Install App** on your organization and select the repositories patchy should watch (the ones with code
scanning enabled). The controllers resolve the installation per repository automatically — no installation ID needs
configuring.

!!! info "GitHub Enterprise Server"

    Point the custom resources at your instance with `spec.github.baseURL` on the `Integration` and `spec.baseURL`
    on the `Forge`, e.g. `https://ghes.example.com/api/v3`. Everything else is identical.
