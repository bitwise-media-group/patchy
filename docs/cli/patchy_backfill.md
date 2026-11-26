## patchy backfill

Backfill an integration's pre-existing open alerts into findings

### Synopsis

File a one-shot backfill of open alerts on an integration. The command creates
a BackfillRequest. The integration-controller walks the provider's open alerts
and ingests the ones that predate webhook coverage. These are alerts raised
before the GitHub App was installed on (or associated with) their repository,
so no replay of the delivery log can surface them. Ingestion is idempotent: an
alert that already has a finding merges into it.

--repo narrows the walk. An "owner/" prefix covers a whole account. An
"owner/name" entry covers one repository and, because it is a prefix, any name
that starts with it. With App credentials the filter skips whole installations.
With a PAT every entry must be an exact owner/name, because a PAT cannot
discover repositories. If the request reports the walk as truncated, it hit
the page budget: file another with a narrower prefix to reach the rest.

A request that runs at once needs the 'backfill' verb on integrations. The CLI
checks it before writing, and the cluster's admission policy enforces it for
every client. Without the verb, pass --request: the request then waits in
AwaitingApproval until an operator approves it (`patchy approve
backfillrequest <name>`) or denies it (`patchy deny backfillrequest <name>`).

Watch a request with `patchy get backfillrequests` or `patchy describe
backfillrequest <name>`.

```
patchy backfill <integration> [flags]
```

### Examples

```
  patchy backfill gh
  patchy backfill gh --repo acme/
  patchy backfill gh --repo acme/shop --repo acme/billing
  patchy backfill gh --repo acme/shop --request
```

### Options

```
  -h, --help[=true|false]      help for backfill
      --repo stringArray       repository filter: an "owner/" prefix or an exact "owner/name" (repeatable; empty = full scope)
      --request[=true|false]   ask an operator to approve the backfill instead of running it at once (needs no backfill verb)
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

