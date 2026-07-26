## patchy describe

Show the full detail of one resource

### Synopsis

Show everything known about one resource: state, timeline, and what can be done to it.

```
patchy describe <resource> <name> [flags]
```

### Examples

```
  patchy describe finding my-finding
  patchy describe investigation my-finding-inv-1
```

### Options

```
  -h, --help   help for describe
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

