## patchy can-i

Show which actions your RBAC allows

### Synopsis

Show what you may do to findings in this namespace.

Each action is a custom RBAC verb, granted independently — holding 'approve'
says nothing about 'suspend'. With no argument this prints the whole matrix,
which is the fastest answer to "why was that refused?".

```
patchy can-i [verb] [flags]
```

### Examples

```
  patchy can-i
  patchy can-i approve
  patchy can-i approve -n patchy
```

### Options

```
  -h, --help   help for can-i
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

