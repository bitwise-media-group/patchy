# AWS resource tags

Patchy has no AWS scanner source. AWS findings arrive through [Wiz](../sources/wiz.md) (or a future source). The `aws`
Integration provides the [context enhancer](index.md) instead. It looks up a finding's AWS resource in an
organization-level inventory, copies the resource's tags onto the finding, and resolves the owning repository from them.
It is the AWS counterpart of the [Google Cloud Asset Inventory enhancer](google-cai.md). Like every enhancer, it works
only from the finding's cloud resource and ignores where the finding came from.

## Two inventories, one question

The enhancer asks one question: _given this ARN, what are the resource's tags?_ AWS offers two organization-level
services that can answer it. Which one you have depends on your deployment, so the Integration names one of them, and
only one:

**AWS Config, through an organization aggregator** (`configAggregator`). The enhancer runs one SQL query
(`SELECT … WHERE arn = …`) against the aggregator. `arn` and `tags` are standard properties of the recorded
configuration items. This backend suits estates, usually larger ones, that already run AWS Config across every account
for compliance. Prerequisites: Config recording enabled in every member account and region the findings cover, and an
[organization aggregator](https://docs.aws.amazon.com/config/latest/developerguide/aggregate-data.html) in the account
the enhancer authenticates to.

**AWS Resource Explorer, through an organization-wide view** (`resourceExplorer`). The enhancer searches the view with
the exact-identifier filter (`id:<arn>`) and reads tags from the result. It is free and does not need Config across the
organization, so it suits smaller organizations. Prerequisites:

- Resource Explorer
  [indexes turned on](https://docs.aws.amazon.com/resource-explorer/latest/userguide/manage-service-multi-account.html)
  in the member accounts.
- An aggregator index.
- An **organization-wide view that includes the tags property**. A view without it would return no tags for every
  lookup. So the enhancer rejects such a view when it builds the client, instead of quietly returning less.

Resource Explorer has two coverage gaps that Config does not have: it never indexes IAM resource tags, and newly created
resources take a few minutes to appear.

Both inventories are eventually consistent, so a change can take a while to show up. That costs nothing here, because a
finding accumulates for an hour before enhancement runs. Nothing downstream can tell the backends apart: the enrichment
is identical.

## The ownership tags

The [shared vocabulary](index.md#the-ownership-labels), written as tags:

--8<-- ".snippets/ownership-labels.md"

Unlike a Google Cloud label, an AWS tag value can contain `://` as is. The scheme is still optional, so one vocabulary
can be shared across clouds. The key names are configurable (`resourceTags.tags`, below) for estates that already have a
naming convention.

The resource's own tags also become finding attributes (`tag:<key>`, up to 24 of them, alongside `aws-account`,
`resource-type` and `location`). They are visible on the tracking issue and to the investigating agent, whether or not a
repository resolved.

## Enabling it

```yaml
apiVersion: patchy.bitwisemedia.uk/v1alpha1
kind: Integration
metadata:
  name: aws
  namespace: patchy
spec:
  provider: aws
  aws:
    resourceTags:
      enabled: true
      # Exactly one backend:
      configAggregator:
        name: org-aggregator
        region: eu-west-2
      # ... or ...
      # resourceExplorer:
      #   viewARN: arn:aws:resource-explorer-2:eu-west-2:123456789012:view/org-view/11111111-2222-3333-4444-555555555555
      # Optional: the forge host composed into a resolved URL (default github.com).
      # repositoryHost: github.example.com
      # Optional: override the tag names read off a resource.
      # tags:
      #   org: scm-repository-org
      #   name: scm-repository-name
      #   provider: scm-repository-provider
      #   url: scm-repository-url
```

The context-controller reads the block on each enhancement, so changes apply without a restart. An `aws` Integration has
**no `secretRef`**. Like its `google-cloud` counterpart, it holds no key material anywhere. Credentials come from the
SDK default chain and are read-only (`config:SelectAggregateResourceConfig` + `config:DescribeConfigurationAggregators`,
or `resource-explorer-2:Search` + `resource-explorer-2:GetView`):

**On EKS**, either of these works, and the SDK default chain picks up whichever is present:

- [EKS Pod Identity](https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html): install the
  `eks-pod-identity-agent` add-on and create a pod identity association for the context-controller's service account. No
  patchy configuration is needed. One deployment note: the SDK fetches credentials over HTTP from the node-local agent
  at `169.254.170.23:80`. Patchy's NetworkPolicy allows that egress from the controllers. If you add your own, tighter
  policy, keep that rule.
- [IRSA](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html): annotate the service
  account with the role:

  ```yaml
  contextController:
    serviceAccount:
      annotations:
        eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/patchy-resource-tags
  ```

If both are configured, IRSA wins, because the chain resolves web-identity credentials before container credentials.

**Anywhere else** (GKE included), use
[web-identity federation](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_oidc.html). You need an
IAM OIDC provider that trusts the cluster's service-account token issuer. You also need `AWS_ROLE_ARN` +
`AWS_WEB_IDENTITY_TOKEN_FILE` (a projected service-account token) on the context-controller deployment. The SDK picks
both up without any patchy configuration.

## When no repository resolves

[The shared semantics apply](index.md#when-no-repository-resolves). A resource without ownership tags is a clean answer.
So is a resource the inventory has no record of, which happens more often with Resource Explorer's indexing gaps. A
lookup that _failed_ (throttling, an identity binding still propagating, a misconfigured aggregator or view) holds and
retries instead.

One AWS-specific detail: a [Wiz Defend](../sources/wiz.md) threat that names no concrete resource falls back to a
synthetic account-level pseudo-resource. No inventory records those, so the enhancer skips them instead of looking them
up.
