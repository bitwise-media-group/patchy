## patchy can-i

Show which actions your RBAC allows

### Synopsis

Show what you may do in this namespace.

Each action is a custom RBAC verb, granted on its own. Holding 'approve' says
nothing about 'suspend'. The per-finding verbs (approve, retry, expedite,
suspend, resume) apply to findings. The integration-scoped verbs (backfill,
replay, reset) apply to integrations. With no argument this prints every verb
and your answer for each, which is the fastest way to see why something was
refused.

```
patchy can-i [verb] [flags]
```

### Examples

```
  patchy can-i
  patchy can-i approve
  patchy can-i backfill
  patchy can-i approve -n patchy
```

### Options

```
  -h, --help[=true|false]   help for can-i
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

