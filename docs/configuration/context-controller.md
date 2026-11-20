# context-controller

Runs the enhancer chain over `Opened` findings. An enhancer looks up ownership and infrastructure context for a finding.
The controller records that context as enrichments and owners on the Finding's status, then moves the finding from
`Opened` to `Enhanced`. The integration-controller projects each enrichment's attributes as `security-context` issue
labels and its markdown as a sticky issue comment. This controller has **no GitHub access at all**. It reads and writes
Finding resources, reads Integrations, and reads the signing Secrets of generic Integrations by name. It does nothing
else.

```sh
context-controller serve --namespace patchy --static-context-file /etc/patchy/context/cmdb.yaml
```

## Flags

The [shared flags](index.md#shared-flags), plus:

| Flag                     | Env                           | Default | Purpose                                                                                                                                            |
| ------------------------ | ----------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--static-context-file`  | `PATCHY_STATIC_CONTEXT_FILE`  | none    | YAML file mapping repositories to owners/attributes (fake-CMDB enhancer)                                                                           |
| `--enhance-concurrency`  | `PATCHY_ENHANCE_CONCURRENCY`  | `4`     | Findings enhanced in parallel (each finding's chain still runs serially)                                                                           |
| `--enhance-max-outbound` | `PATCHY_ENHANCE_MAX_OUTBOUND` | `8`     | Concurrent outbound generic enhancer calls across **all** findings; `0` means unbounded                                                            |
| `--enhancer-config-ttl`  | `PATCHY_ENHANCER_CONFIG_TTL`  | `10s`   | How long the generic endpoint list and signing secrets are cached; a rotated secret takes effect within this window (`0` re-reads per enhancement) |

## Behavior

- **Watch-driven**: the controller runs a Finding reconciler filtered to phase `Opened`. There is no webhook and no
  polling interval to tune.
- **Enhancer failures log and continue**: a broken enhancer never blocks the transition. The finding still moves to
  `Enhanced` with whatever the chain produced. There is one exception. If the repository lookup for a cloud finding
  _failed_ (as opposed to finding no labels), the finding stays at `Opened` and the lookup is retried. The retries stop
  when the accumulation window ends.
- **Owners matter later**: when a `manual` or held finding routes to humans, it goes to the owners recorded on
  `status.owners`.

## The Google Cloud Asset Inventory enhancer

The `google-cloud-labels` enhancer reads the ownership labels on a cloud resource through Cloud Asset Inventory. From
those labels it resolves the finding's repository, plus project, type and location attributes. It takes **no flags**.
Its configuration is the `cloudAssetInventory` block on the `google-cloud` Integration, which it reads from the cluster
for each enhancement. See [Google Cloud Asset Inventory](../integrations/enhancers/google-cai.md#enabling-it). It acts
on any finding whose cloud resource is on Google Cloud, whichever source ingested it. It does nothing when no
Integration turns it on. The only thing this controller needs for it is a credential: workload identity with
`roles/cloudasset.viewer` on the controller's ServiceAccount.

## The AWS resource tags enhancer

The `aws-resource-tags` enhancer does the same job for AWS. It reads the ownership tags on the resource from an
organization-level inventory: an AWS Config aggregator or a Resource Explorer view, whichever your organization has.
From those tags it resolves the repository, plus account, type and location attributes. It also adds the resource's own
tags as `tag:<key>` attributes. It takes **no flags**. Its configuration is the `resourceTags` block on the `aws`
Integration, read for each enhancement. See [AWS resource tags](../integrations/enhancers/aws.md). The credential works
the same way as for Google Cloud: the SDK default chain on the controller's ServiceAccount (EKS Pod Identity or IRSA on
EKS, web-identity federation elsewhere). Access is read-only and needs no Secret.

## The Azure resource tags enhancer

The `azure-resource-tags` enhancer covers Azure. It reads the ownership tags on the resource from Azure Resource Graph,
which covers the whole tenant, so there is no backend to choose. From those tags it resolves the repository, plus
subscription, type and location attributes. It also adds the resource's own tags as `tag:<key>` attributes. It takes
**no flags**. Its configuration is the `resourceTags` block on the `azure` Integration, read for each enhancement. See
[Azure resource tags](../integrations/enhancers/azure.md). The credential works the same way: the Azure default chain on
the controller's ServiceAccount (Microsoft Entra Workload ID on AKS, workload identity federation elsewhere). Access is
read-only and needs no Secret.

## The generic HTTP enhancer

The `generic` chain entry is a fan-out over many endpoints rather than a single enhancer. It calls the enhancer endpoint
of **every** `provider: generic` Integration whose `enhance` capability is on. Each request is signed with that
Integration's own `webhookSecret` and limited by its own `timeout`. Each enrichment is attributed to the Integration's
name, which is also the sticky-comment identity and the key for attribute precedence. Integrations run in name order,
after the cloud lookups and before the static file. It takes **no flags**. Everything lives in the `enhance` block on
each generic Integration, read for each enhancement. See [Generic (HTTP)](../integrations/enhancers/generic.md) for the
request and response contract. If one endpoint fails or times out, only that integration's contribution is skipped. The
other enrichments still land.

## The static context enhancer

The built-in enhancer is a placeholder for a real CMDB (configuration management database). It is a YAML map from
repository to ownership and attributes. Without `--static-context-file`, the chain holds only the cloud enhancers, and
each of those does nothing unless an Integration turns it on.

```yaml
# /etc/patchy/context/cmdb.yaml
repos:
  acme/payments-api:
    owners: [alice, payments-platform]
    attributes: # semi-structured facts → security-context labels
      tier: "1"
      pci: "true"
    markdown: | # optional free-form content → sticky issue comment
      Payments API is PCI-scoped; page #payments-oncall before touching auth.
```

The dev overlay mounts a sample with this shape from a ConfigMap. Real integrations implement the
[`pkg/enhance`](../extending.md#context-enhancers-pkgenhance) interface.
