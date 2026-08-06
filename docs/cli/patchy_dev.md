## patchy dev

Local test harnesses for generic-integration authors

### Synopsis

Test a generic integration against the real patchy contract without a cluster:
`dev generic` hosts the receiver locally and drives the enhancer and resolver
calls; `dev enhance` and `dev resolve` fire a single outbound exchange for
authors whose process is only an enhancer or resolver.

Every flag also resolves from a PATCHY_DEV_* environment variable and an
optional .patchy.yaml (or .yml/.json) in the working directory, with explicit
flags winning over the environment, and the environment over the file.

### Options

```
  -h, --help   help for dev
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
* [patchy dev enhance](patchy_dev_enhance.md)	 - Send an enhancement request to a generic enhancer
* [patchy dev generic](patchy_dev_generic.md)	 - Host a local receiver to test a generic integration
* [patchy dev resolve](patchy_dev_resolve.md)	 - Send a verdict write-back to a generic resolver

