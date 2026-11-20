## patchy dev resolve

Send a verdict write-back to a generic resolver

### Synopsis

Send one signed verdict write-back per finding to a resolver endpoint. The
verdict is the one patchy sends in production: ignore, with the reason
"false positive". Dismissal after investigation is the only path that
resolves an alert today.

Production sends the write-back once per finding at dismissal, carrying every
accumulated alert. This command does not emulate accumulation, so it sends one
alert per call. Any 2xx answer is success, and patchy retries failures, so the
endpoint must treat an already-closed alert as success rather than an error.

Payload files use the same envelope the webhook receives. Flags can also come
from PATCHY_DEV_* variables and the patchy config's dev: block.

```
patchy dev resolve <payload-file>... [flags]
```

### Examples

```
  patchy dev resolve --url http://127.0.0.1:9000/resolve --secret dev-secret findings.json
  patchy dev resolve --secret-file ./webhook.secret findings.json   # url from patchy.yaml
```

### Options

```
  -h, --help[=true|false]    help for resolve
      --name string          integration name sent as the request's integration field (default "dev")
      --secret string        shared HMAC secret the endpoint verifies
      --secret-file string   file holding the shared HMAC secret
      --timeout duration     timeout per call (default 1m0s)
      --url string           resolver endpoint to call
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

* [patchy dev](patchy_dev.md)	 - Local test harnesses for generic-integration authors

