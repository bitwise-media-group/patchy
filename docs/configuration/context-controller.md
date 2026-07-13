# context-controller

Reacts to `security-finding: opened` issues, runs the enhancer chain, and flips the state to `context-enhanced`. The
enrichment lands as a comment so the source-controller keeps sole ownership of the issue body.

```sh
context-controller serve --webhook-secret-file /etc/patchy/webhook/secret \
  --github-app-id 123456 --github-app-private-key-file /etc/patchy/github-app/private-key.pem \
  --static-context-file /etc/patchy/context.yaml
```

## Flags

All [shared flags](index.md#shared-flags-all-three-controllers), plus:

<div class="nowrap-first" markdown>

| Flag                    | Env                          | Default | Purpose                                                                  |
| ----------------------- | ---------------------------- | ------- | ------------------------------------------------------------------------ |
| `--static-context-file` | `PATCHY_STATIC_CONTEXT_FILE` | —       | YAML file mapping repositories to owners/attributes (fake-CMDB enhancer) |
| `--enhance-grace`       | `PATCHY_ENHANCE_GRACE`       | `2m`    | How old an `opened` issue must be before the reconcile pass enhances it  |

</div>

## Behavior

- **Webhook path** — `issues` deliveries (`opened` / `labeled`) carrying `security-finding: opened` are enhanced
  immediately.
- **Reconcile path** — a sweep every `--reconcile-interval` catches issues the webhook missed, once they are older than
  `--enhance-grace` (so the webhook path gets first crack).
- **Enhancer failures log and continue** — a broken enhancer never blocks the state transition; the issue still moves to
  `context-enhanced`.

## The static context enhancer

The built-in enhancer is a deliberate placeholder for a real CMDB: a YAML map from repository to ownership and
attributes. Without `--static-context-file` the chain is a no-op.

```yaml
# /etc/patchy/context.yaml
repos:
  acme/payments-api:
    owners: [alice, payments-platform]
    attributes:
      tier: "1"
      pci: "true"
```

The owners recorded here are who the remediation-controller assigns when a finding routes to humans. Real integrations
implement the [`pkg/enhance`](../extending.md#context-enhancers-pkgenhance) interface.
