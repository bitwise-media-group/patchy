# context-controller

Runs the enhancer chain over `Opened` findings: ownership and infrastructure context recorded as enrichments and owners
on Finding status, then the `Opened → Enhanced` transition. The integration-controller projects the enrichments as issue
comments — this controller itself has **no GitHub access at all**; it reads and writes Finding resources, nothing else.

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
  `Enhanced` with whatever the chain produced.
- **Owners matter downstream** — the owners recorded on `status.owners` are who a `manual` or held finding is handed to
  when it routes to humans.

## The static context enhancer

The built-in enhancer is a deliberate placeholder for a real CMDB: a YAML map from repository to ownership and
attributes. Without `--static-context-file` the chain is the explicit no-op enhancer.

```yaml
# /etc/patchy/context/cmdb.yaml
repos:
  acme/payments-api:
    owners: [alice, payments-platform]
    attributes:
      tier: "1"
      pci: "true"
```

The dev overlay mounts a sample of exactly this shape from a ConfigMap. Real integrations implement the
[`pkg/enhance`](../extending.md#context-enhancers-pkgenhance) interface.
