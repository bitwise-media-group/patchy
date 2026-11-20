# Azure resource tags

Patchy has no Azure scanner source. Azure findings arrive through [Wiz](../sources/wiz.md) (or a future source). The
`azure` Integration provides the [context enhancer](index.md). It looks up a finding's Azure resource in Azure Resource
Graph, copies the resource's tags onto the finding, and finds the owning repository from them. It is the Azure
counterpart of the [Google Cloud Asset Inventory](google-cai.md) and [AWS resource-tags](aws.md) enhancers. Like every
enhancer, it acts on the finding's cloud resource only, whatever source the finding came from.

## One inventory, one question

The enhancer asks one question: _given this ARM resource ID, what are the resource's tags?_ Unlike AWS, there is no
backend to choose. [Azure Resource Graph](https://learn.microsoft.com/azure/governance/resource-graph/overview) is free,
always on, and spans the tenant. The enhancer runs one KQL query (`Resources | where id =~ …`) and reads the tags off
the row. The query is case-insensitive because ARM resource IDs are. The scope is every subscription the enhancer's
identity can read. You do not need to list subscriptions or set anything up across your Azure estate first.
`resourceTags.managementGroup` can narrow the query to one management group.

Resource Graph can lag behind real changes by a few minutes. That does not matter here, because a finding accumulates
for an hour before enhancement runs.

## The ownership tags

The [shared vocabulary](index.md#the-ownership-labels), spelled as tags:

--8<-- ".snippets/ownership-labels.md"

An Azure tag value can carry `://` as written, like an AWS tag and unlike a Google Cloud label. The scheme is still
optional, so one vocabulary can be shared across clouds. If you already have a naming convention, you can change the key
names (`resourceTags.tags`, below).

The resource's tags also become finding attributes (`tag:<key>`, up to 24), alongside `azure-subscription`,
`resource-type` and `location`. The tracking issue and the investigating agent can see them, whether or not a repository
resolved.

## Enabling it

```yaml
apiVersion: patchy.bitwisemedia.uk/v1alpha1
kind: Integration
metadata:
  name: azure
  namespace: patchy
spec:
  provider: azure
  azure:
    resourceTags:
      enabled: true
      # Optional: narrow the scope to one management group
      # (default: every subscription the identity can read).
      # managementGroup: platform-mg
      # Optional: the forge host composed into a resolved URL (default github.com).
      # repositoryHost: github.example.com
      # Optional: override the tag names read off a resource.
      # tags:
      #   org: scm-repository-org
      #   name: scm-repository-name
      #   provider: scm-repository-provider
      #   url: scm-repository-url
```

The context-controller reads the block on each enhancement, so changes apply without a restart. An `azure` Integration
has **no `secretRef`**. Like the `google-cloud` and `aws` Integrations, it holds no key material anywhere. Credentials
come from the Azure default chain and need read access only. The built-in `Reader` role on the management group or
subscriptions is enough.

- **On AKS**, use [Microsoft Entra Workload ID](https://learn.microsoft.com/azure/aks/workload-identity-overview).
  Annotate the service account with the client id of a federated managed identity. Label the pod so the webhook injects
  the token:

  ```yaml
  contextController:
    serviceAccount:
      annotations:
        azure.workload.identity/client-id: 00000000-0000-0000-0000-000000000000
    podLabels:
      azure.workload.identity/use: "true"
  ```

- **Anywhere else** (including GKE), use
  [workload identity federation](https://learn.microsoft.com/entra/workload-id/workload-identity-federation). Create a
  federated credential on a Microsoft Entra app or managed identity that trusts the cluster's service-account token
  issuer. Then set `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` and `AZURE_FEDERATED_TOKEN_FILE` (a projected service-account
  token) on the context-controller deployment. The SDK picks them up with no patchy configuration.

## When no repository resolves

[The shared rules apply](index.md#when-no-repository-resolves). Some resources have no ownership tags. Resource Graph
may have no record of others, because they were deleted or are not indexed yet. Both count as a clear answer. A lookup
that _failed_ (throttling, an identity binding still propagating) is held and retried instead.

One special case also applies to [AWS](aws.md). A [Wiz Defend](../sources/wiz.md) threat that names no specific resource
falls back to a synthetic resource at subscription level. No inventory records these, so the enhancer skips them instead
of looking them up.
