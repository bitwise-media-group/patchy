# Google Cloud Asset Inventory

The `google-cloud-labels` enhancer [resolves a repository](index.md) for findings about Google Cloud resources. It reads
the ownership labels on the resource itself, through Cloud Asset Inventory. It also copies the resource's project, type
and location onto the finding as attributes. Like every enhancer, it looks only at the finding's cloud resource. Any
finding whose resource is on Google Cloud gets the same lookup, whether [SCC](../sources/google-scc.md) or
[Wiz](../sources/wiz.md) ingested it.

!!! info "One provider, two capabilities"

    This enhancer and the [SCC source](../sources/google-scc.md) are independent halves of the one `google-cloud`
    Integration. If you get findings only from Wiz, you can still configure `cloudAssetInventory` here without enabling
    `securityCommandCenter`. The [integrations overview](../index.md) maps every provider's roles.

## The ownership labels

The [shared vocabulary](index.md#the-ownership-labels), spelled as resource labels:

--8<-- ".snippets/ownership-labels.md"

Google Cloud label values can hold only lowercase letters, digits, hyphens and underscores, up to 63 characters. They
cannot hold `https://`, so the scheme is optional, and patchy adds it when it is missing. Security marks have no such
limit and can carry a full URL. If your organization already has a naming convention, you can change the key names
(`cloudAssetInventory.labels`, below).

## Enabling it

You configure the enhancer on the `google-cloud` Integration itself, beside (or instead of) the SCC source. The
context-controller reads it from there for each enhancement, so changes apply without a restart:

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

The credential is workload identity with `roles/cloudasset.viewer`, which is read-only. No key file exists, and the
Integration still carries no `secretRef`:

```yaml
contextController:
  serviceAccount:
    annotations:
      iam.gke.io/gcp-service-account: patchy-assets@x-patchy-app.iam.gserviceaccount.com
```

(Earlier releases configured this through `PATCHY_GCP_*` environment variables on the context-controller. Those flags
are gone. The Integration block above is now the only place to configure it.)

A resource without ownership labels is a valid answer and does not count as a failure. See
[when no repository resolves](index.md#when-no-repository-resolves) for what happens next, on this cloud and every
other.
