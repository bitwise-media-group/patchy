# Google Cloud Security Command Center

Patchy ingests Security Command Center findings and triages them alongside code-scanning alerts. A cloud finding is
about a **resource**, not a file, so the pipeline treats it differently in two places: how it accumulates, and how it
finds a repository to work in.

## How findings arrive

Security Command Center has no webhook. Its only egress is a `NotificationConfig` publishing to a Pub/Sub topic, so a
**push subscription** forwards each notification to patchy's receiver at `/google-cloud/webhooks`. The
[`scc-notifications` Terraform submodule](https://github.com/bitwise-media-group/terraform-google-gke-flux/tree/main/modules/scc-notifications)
provisions that path — topic, notification config, subscription, dead-letter queue and IAM — and outputs the two values
the `Integration` must agree with.

Authentication is the **OIDC token Pub/Sub signs**, not an HMAC. A push subscription cannot compute one: Pub/Sub
composes the message, so the sender never sees the bytes patchy receives. The token is the stronger primitive anyway —
asymmetric, short-lived, and bound to both a service account and an audience. Patchy checks both, because anyone with a
Google Cloud account can mint a Google-signed token; it is the _identity_ and _audience_ that make one meaningful.

```yaml
apiVersion: patchy.bitwisemedia.uk/v1alpha1
kind: Integration
metadata:
  name: google-cloud
  namespace: patchy
spec:
  provider: google-cloud
  googleCloud:
    securityCommandCenter:
      enabled: true
      # Both must match the push subscription exactly; a mismatch fails closed.
      audience: https://patchy.example.co.uk/google-cloud/webhooks
      serviceAccount: patchy-scc-push@x-patchy-app.iam.gserviceaccount.com
      # Numeric organization id, for composing Console links.
      organization: "123456789012"
      # A backstop under the notification config's own filter.
      minSeverity: high
```

A `google-cloud` Integration carries **no `secretRef`**: it holds no credential, and nothing in it calls a Google API.

Deliveries patchy declines are retried by Pub/Sub and then dead-lettered, never silently dropped. Two signals are worth
alerting on: anything in the dead-letter topic, and the subscription's oldest unacknowledged message age — patchy
answers 503 when its delivery queue is full, which Pub/Sub correctly treats as backpressure.

## What patchy keeps

The notification is self-contained, so ingest makes no API call. From it a `Finding` gets:

| Finding field        | From                                                            |
| -------------------- | --------------------------------------------------------------- |
| `spec.cloudResource` | `finding.resourceName` plus the notification's `resource` block |
| `spec.advisories`    | the CVE when there is one, then `category:<CATEGORY>`           |
| `spec.alerts[].id`   | `finding.name`, the full SCC resource name                      |
| `spec.severity`      | `finding.severity`, lowercased                                  |
| `spec.description`   | the whole notification, rendered to markdown                    |

Notifications are skipped when the finding is not `ACTIVE`, is muted, is an `SCC_ERROR` (a detector that could not run —
an operational problem for whoever owns the SCC configuration, not something to triage), or falls below `minSeverity`.

**Accumulation is per resource, not per project.** SCC re-notifies on every update to a finding, and those
re-notifications fold into one `Finding`. Two different buckets with the same misconfiguration stay separate, because
repository resolution is per resource and a family spanning resources could resolve to two different repositories with
no right answer.

## Finding a repository

The whole pipeline past triage needs a SHA-pinned repository to work in, and an SCC notification names none. Patchy
resolves one from **ownership labels on the resource itself**, read through Cloud Asset Inventory by the
context-controller's `google-cloud-labels` enhancer.

The enhancer keys purely on the finding's cloud resource, not on where the finding came from: any finding whose resource
lives on Google Cloud gets the same lookup, whether Security Command Center or [Wiz](wiz.md) ingested it. The two
capabilities are independent halves of the `google-cloud` Integration — an estate sourcing findings from Wiz alone still
configures `cloudAssetInventory` here, without enabling `securityCommandCenter`.

### The ownership labels

```text
scm-repository-org:      acme          # the organization
scm-repository-name:     infra-prod    # the repository
scm-repository-provider: github        # optional; defaults to github
```

Or, where a single value is easier to manage:

```text
scm-repository-url:      github.com/acme/infra-prod
```

The URL form supersedes the triple, and is the only one that can name a self-hosted forge. Google Cloud label values are
lowercase alphanumerics, hyphens and underscores capped at 63 characters — which cannot hold `https://` — so the scheme
is optional and added when absent. Security marks have no such limit and can carry a full URL.

The key names are configurable (`cloudAssetInventory.labels`, below) for estates with an existing convention.

### Enabling it

The enhancer is configured on the `google-cloud` Integration itself, beside (or instead of) the SCC source — the
context-controller reads it from there per enhancement, so changes apply without a restart:

```yaml
spec:
  provider: google-cloud
  googleCloud:
    cloudAssetInventory:
      enabled: true
      # Bounds the asset search: organizations/<id>, folders/<id>, or projects/<id>.
      scope: organizations/123456789012
      # Optional: the forge host composed into a resolved URL (default github.com).
      # repositoryHost: github.example.com
      # Optional: override the label names read off a resource.
      # labels:
      #   org: scm-repository-org
      #   name: scm-repository-name
      #   provider: scm-repository-provider
      #   url: scm-repository-url
```

The credential is workload identity with `roles/cloudasset.viewer` — read-only, and the only cloud credential anywhere
in patchy. No key file exists, and the Integration still carries no `secretRef`:

```yaml
contextController:
  serviceAccount:
    annotations:
      iam.gke.io/gcp-service-account: patchy-assets@x-patchy-app.iam.gserviceaccount.com
```

(Earlier releases configured this through `PATCHY_GCP_*` environment variables on the context-controller; those flags
are gone, and the Integration block above is the only configuration surface.)

### When no repository resolves

Most resources carry no ownership labels, and that is not a failure. Such a finding still ingests, still gets enriched
with its project and resource type, and still reaches a human — it simply cannot be remediated automatically, so the
investigation gate hands it off. Set `github.issues.fallbackRepository` on the tracking Integration to give those
findings a tracking issue somewhere visible; without one they are reachable only through `kubectl` and the status page.

A finding whose lookup _failed_ — as opposed to one that cleanly has no labels — is held and retried instead, so a
transient Asset Inventory outage does not permanently cost it a repository. The hold is bounded by the accumulation
window.

## What is not implemented

**Muting on an `ignore` verdict.** Patchy dismisses a GHAS alert when it judges a finding a false positive; the
equivalent for SCC is muting, which needs `securitycenter.findings.setMute` — a Google Cloud _write_ permission
integration-controller deliberately does not hold. The write-back seam (`pkg/source.Resolver`) is in place and the
`googleCloud.securityCommandCenter.mute` block is accepted and validated but not yet honoured, so enabling it later is
one implementation rather than a refactor.
