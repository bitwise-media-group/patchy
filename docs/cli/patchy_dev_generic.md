## patchy dev generic

Host a local receiver to test a generic integration

### Synopsis

Run the generic webhook receiver on your workstation: the same server, HMAC
authentication, deduplication, and validation the integration-controller runs,
with findings kept in memory instead of becoming Finding resources. Each
ingested finding immediately exercises the outbound legs you configure — the
enhancer call, then the verdict write-back — so one signed POST tests all
three exchanges of the contract. No cluster is touched.

Rejected deliveries answer 401 with no reason on the wire, exactly as in
production; the reason appears here on stderr instead. Not emulated:
accumulation into Finding resources, tracking issues, and the investigation
that separates enhancement from dismissal — production sends the resolver
write-back only when a finding is dismissed, typically much later.

Without --secret or --secret-file a random secret is generated and printed,
so a first run needs no setup. Every flag also reads from a PATCHY_DEV_*
environment variable and a .patchy.yaml/.yml/.json in the working directory
(flag > environment > file > default).

```
patchy dev generic [flags]
```

### Examples

```
  patchy dev generic
  patchy dev generic --secret dev-secret --enhance-url http://127.0.0.1:9000/enhance
  patchy dev generic --secret-file ./webhook.secret --resolve-url http://127.0.0.1:9000/resolve
  PATCHY_DEV_ENHANCE_URL=http://127.0.0.1:9000/enhance patchy dev generic
  patchy dev generic -o json | jq 'select(.kind=="resolve")'
```

### Options

```
      --addr string                          listen address (port 0 picks an ephemeral one) (default "127.0.0.1:8100")
      --enhance-timeout duration             timeout per enhancer call (default 1m0s)
      --enhance-url string                   enhancer endpoint to call for each ingested finding
  -h, --help                                 help for generic
      --min-severity string                  drop findings below this severity: low, medium, high, or critical
      --name string                          integration name: the webhook path segment and source id (default "dev")
      --no-auto-resolve patchy dev resolve   only retain findings; resolve later with patchy dev resolve
      --resolve-delay duration               pause between a finding's enhance and resolve calls
      --resolve-timeout duration             timeout per resolver call (default 1m0s)
      --resolve-url string                   resolver endpoint for the verdict write-back
      --secret string                        shared HMAC webhook secret
      --secret-file string                   file holding the shared HMAC webhook secret
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

