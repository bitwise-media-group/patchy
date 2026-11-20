## patchy backfill

Backfill an integration's pre-existing open alerts into findings

### Synopsis

Request a one-shot backfill of open alerts on an integration. The
integration-controller lists the provider's open alerts and ingests the ones
that predate webhook coverage. These are alerts raised before the GitHub App
was installed on (or associated with) their repository, so no replay of the
delivery log can surface them. Ingestion is idempotent: an alert that already
has a finding merges into it.

--repo narrows the walk. An "owner/" prefix covers a whole account. An
"owner/name" entry covers one repository and, because it is a prefix, any name
that starts with it. With App credentials the filter skips whole installations.
With a PAT every entry must be an exact owner/name, because a PAT cannot
discover repositories. If status.backfill reports the walk as truncated, it hit
the page budget: re-run with a narrower prefix to reach the rest.

The command writes only spec.backfill. A controller sees the change and runs
the walk. Your RBAC decides whether the write is allowed. The CLI checks the
'backfill' verb on integrations before writing, and the cluster's admission
policy enforces it for every client.

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
  -h, --help[=true|false]   help for backfill
      --repo stringArray    repository filter: an "owner/" prefix or an exact "owner/name" (repeatable; empty = full scope)
```

### Options inherited from parent commands

```
  -A, --all-namespaces[=true|false]   work across every namespace
      --context string                kubeconfig context to use
      --kubeconfig string             path to the kubeconfig file
  -n, --namespace string              namespace to work in (default: the context's)
      --no-color[=true|false]         disable colour and styling
  -o, --output string                 output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration      timeout for a single API call (default 30s)
  -v, --verbose[=true|false]          log what the CLI is doing to stderr
```

### SEE ALSO

* [patchy](patchy.md)	 - Work with patchy security findings from the terminal

