## patchy mirror validate

Check that the committed store is current, verified and clean

### Synopsis

Validate entries without changing the tree. validate runs four checks:

  - It regenerates the derived files out-of-tree and byte-compares them with
    what is committed. A stale tree means someone edited intent without running
    upgrade.
  - It verifies upstream provenance.
  - It scans the locked images with the enabled scanners.
  - It lints the manifests and CVE allowlists. Each allowlist entry needs a
    statement and an expiry within the policy horizon. Rendered output may pull
    only from the mirror, unless an exception is allowed with a reason.

Steps that read the clock (tracked-tag picks, allowlist expiry stamping) never
run here. That keeps the byte comparison deterministic between a commit and the
CI run that validates it.

```
patchy mirror validate [name]... [flags]
```

### Examples

```
  patchy mirror validate --all
  patchy mirror validate --all -o markdown   # reviewer summary (PR comment, step summary)
  patchy mirror validate --all --only scan -o json
  patchy mirror validate opentelemetry-collector --only regen,lint
```

### Options

```
      --all[=true|false]    validate every entry in the store
  -h, --help[=true|false]   help for validate
      --only strings        run only these stages: regen, verify, scan, lint
```

### Options inherited from parent commands

```
  -A, --all-namespaces[=true|false]   work across every namespace
      --context string                kubeconfig context to use
  -C, --directory string              mirror store directory, relative to the working directory (default: the working directory)
      --kubeconfig string             path to the kubeconfig file
  -n, --namespace string              namespace to work in (default: the context's)
      --no-color[=true|false]         disable colour and styling
  -o, --output string                 output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration      timeout for a single API call (default 30s)
  -v, --verbose[=true|false]          log what the CLI is doing to stderr
      --workers int                   concurrent registry operations per stage (default 4)
```

### SEE ALSO

* [patchy mirror](patchy_mirror.md)	 - Mirror upstream helm charts and OCI artifacts into a platform registry

