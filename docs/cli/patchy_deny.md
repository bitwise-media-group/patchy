## patchy deny

Deny a backfill request that is awaiting an operator

### Synopsis

Deny a backfill request filed with `patchy backfill --request`. The request
moves to Denied and never runs. --reason is shown to the requester.

The command writes only the request's spec.decision, and a decision is final:
a request that was already approved cannot be denied. Repeating a denial does
nothing, so it is safe to re-run. `patchy approve backfillrequest <name>` is
the other half.

Deciding a request needs the 'backfill' verb on integrations. The CLI checks it
before writing, and the cluster's admission policy enforces it for every
client.

```
patchy deny backfillrequest <name...> [flags]
```

### Examples

```
  patchy deny backfillrequest backfill-x7k2p
  patchy deny bfr backfill-x7k2p --reason "the whole org is too broad; ask for acme/shop"
```

### Options

```
      --dry-run[=true|false]   report what would change without writing
  -h, --help[=true|false]      help for deny
      --reason string          why the request was denied, shown to the requester
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

