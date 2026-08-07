# AWS resource tags

Patchy has no AWS scanner source — AWS findings arrive through [Wiz](../sources/wiz.md) (or a future source). What the
`aws` Integration provides is the [context enhancer](index.md): it looks a finding's AWS resource up in an
organization-level inventory, carries its tags onto the finding, and resolves the owning repository from them. It is the
AWS sibling of the [Cloud Asset Inventory enhancer](google-cai.md), and like every enhancer it keys purely on the
finding's cloud resource, not on where the finding came from.

## Two inventories, one question

The enhancer asks one question — _given this ARN, what are the resource's tags?_ — and AWS offers two organization-level
services that can answer it. Which one an estate has is a deployment fact, so the Integration names exactly one:

**AWS Config, through an organization aggregator** (`configAggregator`). The enhancer runs one SQL query
(`SELECT … WHERE arn = …`) against the aggregator; `arn` and `tags` are first-class properties of the recorded
configuration items. The fit for estates — typically larger ones — that already run AWS Config estate-wide for
compliance. Prerequisites: Config recording enabled in every member account and region the findings cover, and an
[organization aggregator](https://docs.aws.amazon.com/config/latest/developerguide/aggregate-data.html) in the account
the enhancer authenticates to.

**AWS Resource Explorer, through an organization-wide view** (`resourceExplorer`). The enhancer searches the view with
the exact-identifier filter (`id:<arn>`) and reads tags off the result. Free, and available without estate-wide Config —
the fit for smaller organizations. Prerequisites: Resource Explorer
[indexes turned on](https://docs.aws.amazon.com/resource-explorer/latest/userguide/manage-service-multi-account.html) in
the member accounts, an aggregator index, and an **organization-wide view that includes the tags property** — a view
without it would answer every lookup tagless, so the enhancer refuses such a view at client build rather than silently
degrading. Two coverage caveats Config does not share: IAM resource tags are never indexed, and newly created resources
take a few minutes to appear.

Both inventories are eventually consistent, which costs nothing here: a finding accumulates for an hour before
enhancement runs. Nothing downstream can tell the backends apart — the enrichment is identical.

## The ownership tags

The [shared vocabulary](index.md#the-ownership-labels), spelled as tags:

--8<-- ".snippets/ownership-labels.md"

Unlike a Google Cloud label, an AWS tag value can carry `://` verbatim — though the scheme is still optional, for a
vocabulary shared across clouds. The key names are configurable (`resourceTags.tags`, below) for estates with an
existing convention.

Beyond the repository, the resource's tags themselves become finding attributes (`tag:<key>`, capped at 24, beside
`aws-account`, `resource-type` and `location`) — visible on the tracking issue and to the investigating agent, whether
or not a repository resolved.

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

The context-controller reads the block per enhancement, so changes apply without a restart. An `aws` Integration carries
**no `secretRef`** — like its `google-cloud` sibling, it holds no key material anywhere. Credentials come from the SDK
default chain, read-only (`config:SelectAggregateResourceConfig` + `config:DescribeConfigurationAggregators`, or
`resource-explorer-2:Search` + `resource-explorer-2:GetView`):

- **On EKS**, either works — the SDK default chain picks up whichever is present:

  - [EKS Pod Identity](https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html) — install the
    `eks-pod-identity-agent` add-on and create a pod identity association for the context-controller's service account;
    no patchy configuration at all. One deployment note: the SDK fetches credentials from the node-local agent at
    `169.254.170.23:80` over HTTP, and patchy's NetworkPolicy allows that egress from the controllers — if you layer
    your own tighter policy, keep that rule.
  - [IRSA](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html) — annotate the service
    account with the role:

    ```yaml
    contextController:
      serviceAccount:
        annotations:
          eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/patchy-resource-tags
    ```

  If both are configured, IRSA wins: the chain resolves web-identity credentials before container credentials.

- **Anywhere else** (GKE included):
  [web-identity federation](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_oidc.html) — an IAM OIDC
  provider trusting the cluster's service-account token issuer, and `AWS_ROLE_ARN` + `AWS_WEB_IDENTITY_TOKEN_FILE` (a
  projected service-account token) on the context-controller deployment. The SDK picks both up without patchy
  configuration.

## When no repository resolves

[The shared semantics apply](index.md#when-no-repository-resolves): a resource without ownership tags — or one the
inventory has no record of, which Resource Explorer's indexing gaps make more common — is a clean answer; a lookup that
_failed_ (throttling, an identity binding still propagating, a misconfigured aggregator or view) holds and retries
instead.

One AWS-specific wrinkle: a [Wiz Defend](../sources/wiz.md) threat that names no concrete resource falls back to a
synthetic account-level pseudo-resource. No inventory records those, so the enhancer stands aside rather than looking
them up.
