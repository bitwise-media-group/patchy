## patchy approve

Approve a finding, or a backfill request awaiting an operator

### Synopsis

Approve a finding, releasing a hold or reviving a handed-off finding.

The action writes only to the finding's spec. A controller sees the change and
moves the phase. Repeating an action that has already taken effect does nothing,
so it is safe to re-run.

Your RBAC decides whether the write is allowed. The CLI checks the 'approve'
verb before writing, and the cluster's admission policy enforces it for every
client.

approve also decides a backfill request filed with `patchy backfill --request`:
`patchy approve backfillrequest <name>` lets it run, and `patchy deny
backfillrequest <name>` refuses it. A decision is final, and only a request
waiting for an operator takes one. Deciding needs the 'backfill' verb on
integrations rather than 'approve' on findings. An explicit --note becomes the
decision's reason; -l is not supported for backfill requests.

```
patchy approve (finding|backfillrequest) <name...> [flags]
```

### Examples

```
  patchy approve finding my-finding
  patchy approve finding -l patchy.bitwisemedia.uk/severity=critical
  patchy approve finding my-finding --dry-run
  patchy approve backfillrequest backfill-x7k2p
  patchy approve bfr backfill-x7k2p --note "scoped to acme/shop, go ahead"
```

### Options

```
      --dry-run[=true|false]   report what would change without writing
  -h, --help[=true|false]      help for approve
      --note string            note to record with the approval (default "Approved from the patchy CLI.")
  -l, --selector string        act on every finding matching this label selector
  -y, --yes[=true|false]       do not prompt when acting on more than one finding
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

