## patchy mirror validate

Prove the committed store is current, verified and clean

### Synopsis

Validate entries without touching the tree: regenerate the derived state
out-of-tree and byte-compare it with what is committed (a stale tree means
someone edited intent without running upgrade), verify upstream provenance,
scan the locked images with the enabled scanners, and lint the manifests
and CVE allowlists (statement + expiry within the policy horizon, rendered
output pulling only from the mirror unless allowed with a reason).

Wall-clock steps — tracked-tag picks, allowlist expiry stamping — never run
here, so the byte-identity gate stays deterministic between a commit and CI
validating it.

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
      --all            validate every entry in the store
  -h, --help           help for validate
      --only strings   run only these stages: regen, verify, scan, lint
```

### Options inherited from parent commands

```
  -A, --all-namespaces             work across every namespace
      --context string             kubeconfig context to use
  -C, --directory string           mirror store directory (walks up to mirror.yaml; default: the working directory)
      --kubeconfig string          path to the kubeconfig file
  -n, --namespace string           namespace to work in (default: the context's)
      --no-color                   disable colour and styling
  -o, --output string              output format: table, wide, json, yaml, name, or markdown (default "table")
      --request-timeout duration   timeout for a single API call (default 30s)
  -v, --verbose                    log what the CLI is doing to stderr
      --workers int                concurrent registry operations per stage (default 4)
```

### SEE ALSO

* [patchy mirror](patchy_mirror.md)	 - Mirror upstream helm charts and OCI artifacts into a platform registry

