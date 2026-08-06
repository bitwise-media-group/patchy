## patchy dev enhance

Send an enhancement request to a generic enhancer

### Synopsis

Fire one signed enhancement request per finding at an enhancer endpoint,
without hosting anything: the way to test an integration that is only an
enhancer and never delivers findings itself.

Payload files use the same envelope the webhook receives (version v1, event
findings), validated by the same code, so a fixture works in both places. The
request carries exactly what production sends for a fresh finding: title,
description, repository and cloud resource — no issue number, no labels.

Flags also resolve from PATCHY_DEV_* and .patchy.yaml/.yml/.json (the same
keys the harness reads: dev.enhance-url, dev.secret-file, ...).

```
patchy dev enhance <payload-file>... [flags]
```

### Examples

```
  patchy dev enhance --url http://127.0.0.1:9000/enhance --secret dev-secret findings.json
  patchy dev enhance --secret-file ./webhook.secret findings.json   # url from .patchy.yaml
  patchy dev enhance --url http://127.0.0.1:9000/enhance --secret s -o json findings.json | jq .
```

### Options

```
  -h, --help                 help for enhance
      --name string          integration name sent as the request's integration field (default "dev")
      --secret string        shared HMAC secret the endpoint verifies
      --secret-file string   file holding the shared HMAC secret
      --timeout duration     timeout per call (default 1m0s)
      --url string           enhancer endpoint to call
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

* [patchy dev](patchy_dev.md)	 - Local test harnesses for generic-integration authors

