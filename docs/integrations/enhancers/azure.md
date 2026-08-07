# Azure resource tags

Patchy has no Azure scanner source — Azure findings arrive through [Wiz](../sources/wiz.md) (or a future source). What
the `azure` Integration provides is the [context enhancer](index.md): it looks a finding's Azure resource up in Azure
Resource Graph, carries its tags onto the finding, and resolves the owning repository from them. It is the Azure sibling
of the [Cloud Asset Inventory](google-cai.md) and [AWS resource-tags](aws.md) enhancers, and like every enhancer it keys
purely on the finding's cloud resource, not on where the finding came from.

## One inventory, one question

The enhancer asks one question — _given this ARM resource ID, what are the resource's tags?_ — and unlike AWS there is
no backend to choose: [Azure Resource Graph](https://learn.microsoft.com/azure/governance/resource-graph/overview) is
free, always on, and tenant-wide. The enhancer runs one KQL query (`Resources | where id =~ …`, case-insensitive because
ARM resource IDs are) and reads the tags off the row. Scope is every subscription the enhancer's identity can read — no
per-subscription enumeration, no estate-wide prerequisite. `resourceTags.managementGroup` optionally narrows the query
to one management group.

Resource Graph is eventually consistent by minutes, which costs nothing here: a finding accumulates for an hour before
enhancement runs.

## The ownership tags

The [shared vocabulary](index.md#the-ownership-labels), spelled as tags:

--8<-- ".snippets/ownership-labels.md"

Like an AWS tag — and unlike a Google Cloud label — an Azure tag value can carry `://` verbatim, though the scheme is
still optional, for a vocabulary shared across clouds. The key names are configurable (`resourceTags.tags`, below) for
estates with an existing convention.

Beyond the repository, the resource's tags themselves become finding attributes (`tag:<key>`, capped at 24, beside
`azure-subscription`, `resource-type` and `location`) — visible on the tracking issue and to the investigating agent,
whether or not a repository resolved.

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

The context-controller reads the block per enhancement, so changes apply without a restart. An `azure` Integration
carries **no `secretRef`** — like its `google-cloud` and `aws` siblings, it holds no key material anywhere. Credentials
come from the Azure default chain, read-only (the built-in `Reader` role on the management group or subscriptions is
enough):

- **On AKS**: [Microsoft Entra Workload ID](https://learn.microsoft.com/azure/aks/workload-identity-overview) — annotate
  the service account with the client id of a federated managed identity, and label the pod so the webhook injects the
  token:

  ```yaml
  contextController:
    serviceAccount:
      annotations:
        azure.workload.identity/client-id: 00000000-0000-0000-0000-000000000000
    podLabels:
      azure.workload.identity/use: "true"
  ```

- **Anywhere else** (GKE included):
  [workload identity federation](https://learn.microsoft.com/entra/workload-id/workload-identity-federation) — a
  federated credential on a Microsoft Entra app or managed identity trusting the cluster's service-account token issuer,
  and `AZURE_CLIENT_ID` + `AZURE_TENANT_ID` + `AZURE_FEDERATED_TOKEN_FILE` (a projected service-account token) on the
  context-controller deployment. The SDK picks them up without patchy configuration.

## When no repository resolves

[The shared semantics apply](index.md#when-no-repository-resolves): a resource without ownership tags — or one Resource
Graph has no record of, because it was deleted or is not indexed yet — is a clean answer; a lookup that _failed_
(throttling, an identity binding still propagating) holds and retries instead.

One wrinkle shared with [AWS](aws.md): a [Wiz Defend](../sources/wiz.md) threat that names no concrete resource falls
back to a synthetic subscription-level pseudo-resource. No inventory records those, so the enhancer stands aside rather
than looking them up.
