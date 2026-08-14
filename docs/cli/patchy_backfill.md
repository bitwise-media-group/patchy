## patchy backfill

Backfill an integration's pre-existing open alerts into findings

### Synopsis

Request a one-shot list-alerts backfill on an integration: the
integration-controller walks the provider's open alerts and ingests the ones
that predate webhook coverage — alerts raised before the GitHub App was
installed on (or associated with) their repository, which no replay of the
delivery log can ever surface. Ingestion is idempotent, so alerts that already
have findings simply fold in.

--repo narrows the walk: an "owner/" prefix covers a whole account, an
"owner/name" entry one repository (and, being a prefix, any name extending
it). With App credentials the filter prunes whole installations; with a PAT
every entry must be an exact owner/name, since a PAT cannot discover
repositories. A walk that reports truncated on status.backfill hit the page
budget — re-run with a narrower prefix to reach the rest.

The command writes spec.backfill only — a controller observes it and runs the
walk. Your RBAC decides whether it lands: the CLI checks the 'backfill' verb on
integrations before writing, and the cluster's admission policy enforces it
regardless of client.

```
patchy backfill <integration> [flags]
```

### Examples

```
  patchy backfill gh
  patchy backfill gh --repo acme/
  patchy backfill gh --repo acme/shop --repo acme/billing
```

### Options

```
  -h, --help               help for backfill
      --repo stringArray   repository filter: an "owner/" prefix or an exact "owner/name" (repeatable; empty = full scope)
```

### Options inherited from parent commands

```
  -A, --all-namespaces             work across every namespace
      --context string             kubeconfig context to use
      --kubeconfig string          path to the kubeconfig file
  -n, --namespace string           namespace to work in (default: the context's)
      --no-color                   disable colour and styling
  -o, --output string              output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration   timeout for a single API call (default 30s)
  -v, --verbose                    log what the CLI is doing to stderr
```

### SEE ALSO

* [patchy](patchy.md)	 - Work with patchy security findings from the terminal

