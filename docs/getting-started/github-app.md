# Create the GitHub App

Patchy authenticates as a GitHub App: the controllers mint short-lived, single-repository installation tokens for every
operation instead of holding a long-lived personal access token. One App serves the whole stack.

## Register the App

Go to **Settings → Developer settings → GitHub Apps → New GitHub App** (on your organization, not your user account) and
fill in:

- **GitHub App name** — e.g. `patchy`.
- **Homepage URL** — anything; the repository URL works.
- **Webhook → Active** — checked. The webhook URL and secret come below.

## Repository permissions

Grant exactly these — nothing more:

<div class="nowrap-first" markdown>

| Permission               | Access       | Why                                                     |
| ------------------------ | ------------ | ------------------------------------------------------- |
| **Code scanning alerts** | Read & write | Read alert detail; dismiss false positives              |
| **Issues**               | Read & write | The state machine — open, label, comment, assign, close |
| **Contents**             | Read & write | Clone the repository; push the remediation branch       |
| **Pull requests**        | Read & write | Open the pull request a human reviews                   |
| **Metadata**             | Read         | Mandatory for every App                                 |

</div>

## Webhook events

Subscribe to these four events:

<div class="nowrap-first" markdown>

| Event                 | Consumed by              | Purpose                                                   |
| --------------------- | ------------------------ | --------------------------------------------------------- |
| `code_scanning_alert` | `source-controller`      | New findings arrive and accumulate into issues            |
| `issues`              | `context-controller`     | React to `security-finding: opened` issues                |
| `issue_comment`       | `remediation-controller` | The `/approve` escape hatch for held findings             |
| `pull_request`        | `remediation-controller` | Close the loop when the remediation PR merges (or closes) |

</div>

## The webhook URL

A GitHub App has exactly **one** webhook URL. Point it at the **webhook-controller** — the only patchy component exposed
to the internet:

```text
https://patchy.example.com/webhook
```

The webhook-controller validates each delivery's HMAC signature and routes it to the controllers that consume its event
type. It holds no GitHub credential, so the controllers carrying the App's private key never face the internet directly.
Enable the exposure with the chart's `webhook.host` plus `webhook.ingress` or `webhook.httpRoute` — see
[Deployment → Webhook exposure](../deployment/webhook.md) for both flavours and the managed-platform (EKS, AKS, GKE)
notes.

## Collect the credentials

Three values leave this page and become [Kubernetes Secrets](install.md#create-the-secrets):

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

    Point the controllers at your instance with `PATCHY_GITHUB_BASE_URL` (Helm value `github.baseURL`), e.g.
    `https://ghes.example.com/api/v3`. Everything else is identical.
