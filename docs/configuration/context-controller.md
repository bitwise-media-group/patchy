# context-controller

Runs the enhancer chain over `Opened` findings: ownership and infrastructure context recorded as enrichments and owners
on Finding status, then the `Opened → Enhanced` transition. The integration-controller projects each enrichment's
attributes as `security-context` issue labels and its markdown as a sticky issue comment — this controller itself has
**no GitHub access at all**; it reads and writes Finding resources, reads Integrations, and reads generic Integrations'
signing Secrets by name, nothing else.

```sh
context-controller serve --namespace patchy --static-context-file /etc/patchy/context/cmdb.yaml
```

## Flags

The [shared flags](index.md#shared-flags-all-five-controllers), plus:

<div class="nowrap-first" markdown>

| Flag                    | Env                          | Default | Purpose                                                                  |
| ----------------------- | ---------------------------- | ------- | ------------------------------------------------------------------------ |
| `--static-context-file` | `PATCHY_STATIC_CONTEXT_FILE` | —       | YAML file mapping repositories to owners/attributes (fake-CMDB enhancer) |

</div>

## Behavior

- **Watch-driven** — a Finding reconciler filtered to phase `Opened`; no webhook, no polling interval to tune.
- **Enhancer failures log and continue** — a broken enhancer never blocks the transition; the finding still moves to
  `Enhanced` with whatever the chain produced. The exception is a cloud finding whose repository lookup _failed_ (rather
  than cleanly finding no labels): it is held at `Opened` and retried, bounded by the accumulation window.
- **Owners matter downstream** — the owners recorded on `status.owners` are who a `manual` or held finding is handed to
  when it routes to humans.

## The Google Cloud labels enhancer

The `google-cloud-labels` enhancer resolves a cloud finding's repository (and project/type/location attributes) from the
ownership labels on the resource itself, read through Cloud Asset Inventory. It takes **no flags**: its configuration is
the `cloudAssetInventory` block on the `google-cloud` Integration, read from the cluster per enhancement — see
[Google Cloud SCC](../integrations/google-cloud-scc.md#enabling-it). It acts on any finding whose cloud resource lives
on Google Cloud, whichever source ingested it, and stands aside entirely when no Integration enables the capability. The
only deployment concern this controller keeps is the credential: workload identity with `roles/cloudasset.viewer` on the
controller's ServiceAccount.

## The AWS resource tags enhancer

The `aws-resource-tags` enhancer is the AWS sibling: it resolves the repository (and account/type/location attributes,
plus the resource's own tags as `tag:<key>` attributes) from the ownership tags on the resource, read from an
organization-level inventory — an AWS Config aggregator or a Resource Explorer view, whichever the estate has. Like its
sibling it takes **no flags**: its configuration is the `resourceTags` block on the `aws` Integration, read per
enhancement — see [AWS resource tags](../integrations/aws.md). The credential concern is the same shape: the SDK default
chain on the controller's ServiceAccount (EKS Pod Identity or IRSA on EKS, web-identity federation elsewhere),
read-only, no Secret.

## The Azure resource tags enhancer

The `azure-resource-tags` enhancer completes the trio: it resolves the repository (and subscription/type/location
attributes, plus the resource's own tags as `tag:<key>` attributes) from the ownership tags on the resource, read from
Azure Resource Graph — tenant-wide, no backend to choose. Like its siblings it takes **no flags**: its configuration is
the `resourceTags` block on the `azure` Integration, read per enhancement — see
[Azure resource tags](../integrations/azure.md). The credential concern is the same shape: the Azure default chain on
the controller's ServiceAccount (Microsoft Entra Workload ID on AKS, workload identity federation elsewhere), read-only,
no Secret.

## The generic HTTP enhancer

The `generic` chain entry is a fan-out, not one enhancer: it calls the enhancer endpoint of **every**
`provider: generic` Integration whose `enhance` capability is on — each request signed with that Integration's own
`webhookSecret`, bounded by its own `timeout`, and its enrichment attributed to that Integration's name (the
sticky-comment identity and attribute-precedence key). Integrations run in name order, after the cloud lookups and
before the static file. Like its siblings it takes **no flags**: everything is the `enhance` block on each generic
Integration, read per enhancement — see [Generic (HTTP)](../integrations/generic.md) for the request/response contract.
One endpoint failing (or timing out) skips only that integration's contribution; the others' enrichments still land.

## The static context enhancer

The built-in enhancer is a deliberate placeholder for a real CMDB: a YAML map from repository to ownership and
attributes. Without `--static-context-file` the chain is the cloud enhancers alone (each standing aside unless an
Integration enables it).

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

The dev overlay mounts a sample of exactly this shape from a ConfigMap. Real integrations implement the
[`pkg/enhance`](../extending.md#context-enhancers-pkgenhance) interface.
