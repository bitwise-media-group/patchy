## patchy browse

Browse to a resource's page in a browser

### Synopsis

Open the human-facing page for a resource in your browser: the tracking issue
for a finding or investigation, the pull request for a remediation, the
repository for a repository.

The verb is `browse` rather than `open` because every other patchy verb acts on
the pipeline — and `open` would read as moving a finding into the Opened phase.

```
patchy browse <resource> <name> [flags]
```

### Examples

```
  patchy browse finding my-finding
  patchy browse remediation my-finding-rem-1
  patchy browse finding my-finding --print-url
```

### Options

```
  -h, --help        help for browse
      --print-url   print the URL instead of opening it
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

