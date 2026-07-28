# context-controller

Runs the enhancer chain over `Opened` findings: ownership and infrastructure context recorded as enrichments and owners
on Finding status, then the `Opened → Enhanced` transition. The integration-controller projects each enrichment's
attributes as `security-context` issue labels and its markdown as a sticky issue comment — this controller itself has
**no GitHub access at all**; it reads and writes Finding resources, nothing else.

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

## The static context enhancer

The built-in enhancer is a deliberate placeholder for a real CMDB: a YAML map from repository to ownership and
attributes. Without `--static-context-file` the chain is the Google Cloud labels enhancer alone (which stands aside
unless an Integration enables it).

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
