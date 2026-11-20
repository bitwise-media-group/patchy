# Issue labels

Two different things in patchy are called labels. **Issue labels** are the `security-*` labels on a finding's tracking
issue. They are a one-way projection of the [Finding's state](lifecycle.md) for humans and issue searches. **Resource
labels** are Kubernetes labels on the custom resources themselves, used by selectors. Patchy never reads either kind
back into state. The Finding's spec and status are the source of truth.

## The projected labels

The integration-controller renders the tracking issue's labels from the Finding (`internal/labels`). Every label is
`<key>: <value>`, truncated to GitHub's 50-character limit. Multi-valued keys emit one label per value. Patchy only
touches labels in the `security-` namespace, ignores foreign or malformed `security-*` labels, and does not manage label
colours or descriptions.

| Key                       | Cardinality         | Values                                                                                                                                                                      |
| ------------------------- | ------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `security-source`         | single              | The source handler that ingested the finding, e.g. `ghas`, `gcp-scc`, `wiz-issues`, `wiz-defend`, or a generic Integration's name                                           |
| `security-advisory`       | multi (one per id)  | The CWE, CVE and GHSA identifiers                                                                                                                                           |
| `security-finding`        | single              | The phase, kebab-cased: `opened`, `enhanced`, `investigating`, `queued`, `awaiting-approval`, `remediating`, `in-review`, `remediated`, `dismissed`, `handed-off`, `failed` |
| `security-severity`       | single              | `low` \| `medium` \| `high` \| `critical` (assigned by the scanner)                                                                                                         |
| `security-priority`       | single              | `low` \| `medium` \| `high` \| `critical` (derived from the investigation)                                                                                                  |
| `security-recommendation` | single              | `remediate` \| `ignore` \| `manual` (the investigation's verdict)                                                                                                           |
| `security-context`        | multi (one per key) | `<key>=<value>` for each enrichment attribute the [context enhancers](../integrations/enhancers/index.md) recorded, e.g. `security-context: tier=1`                         |

When two enhancers record the same attribute key, the first enhancer in the chain wins. The chain runs the most
authoritative source first, the same precedence it uses for owners.

The projection is kept small on purpose. Labels exist for issue searches and human triage. Alert numbers, confidence,
budgets, attempt counts and token accounting live on the custom resources, so none of them appear as labels.

## Resource labels

Patchy stamps a few Kubernetes labels on its own resources so that `kubectl` and the [patchy CLI](../operate/cli.md) can
select on them. The values copy spec fields, and the spec stays the source of truth.

| Label                                | On                                    | Value                            |
| ------------------------------------ | ------------------------------------- | -------------------------------- |
| `patchy.bitwisemedia.uk/severity`    | Finding                               | `spec.severity`                  |
| `patchy.bitwisemedia.uk/source`      | Finding                               | The source handler id            |
| `patchy.bitwisemedia.uk/integration` | Finding                               | The Integration that ingested it |
| `patchy.bitwisemedia.uk/finding`     | Investigation, Remediation, agent Job | The owning Finding's name        |
| `patchy.bitwisemedia.uk/attempt`     | Investigation, Remediation            | The attempt ordinal              |

```sh
kubectl get findings -l patchy.bitwisemedia.uk/severity=critical
kubectl get investigations,remediations -l patchy.bitwisemedia.uk/finding=<name>
```

Hashed values (the repository URL and accumulation-key hashes) also exist as labels for the controllers' own selectors.
They are not meant for people.
