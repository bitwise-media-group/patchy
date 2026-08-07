# Google Cloud labels

The `google-cloud-labels` enhancer [resolves a repository](index.md) for findings about Google Cloud resources: it reads
the ownership labels off the resource itself, through Cloud Asset Inventory, and carries the resource's project, type
and location onto the finding as attributes. Like every enhancer it keys purely on the finding's cloud resource — any
finding whose resource lives on Google Cloud gets the same lookup, whether [SCC](../sources/google-scc.md) or
[Wiz](../sources/wiz.md) ingested it.

!!! info "One provider, two capabilities"

    This enhancer and the [SCC source](../sources/google-scc.md) are independent halves of the one `google-cloud`
    Integration — an estate sourcing findings from Wiz alone still configures `cloudAssetInventory` here, without
    enabling `securityCommandCenter`. The [integrations overview](../index.md) maps every provider's roles.

## The ownership labels

The [shared vocabulary](index.md#the-ownership-labels), spelled as resource labels:

--8<-- ".snippets/ownership-labels.md"

Google Cloud label values are lowercase alphanumerics, hyphens and underscores capped at 63 characters — which cannot
hold `https://` — so the scheme is optional and added when absent. Security marks have no such limit and can carry a
full URL. The key names are configurable (`cloudAssetInventory.labels`, below) for estates with an existing convention.

## Enabling it

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

A resource without ownership labels is a clean answer, not a failure — see
[when no repository resolves](index.md#when-no-repository-resolves) for what happens next, on this cloud and every
other.
